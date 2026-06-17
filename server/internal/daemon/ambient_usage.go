package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Collector is a pluggable source of ambient (task-less) token usage: the local
// CLI sessions a user runs directly, never dispatched through agent_task_queue
// and so invisible to every task-based report today.
//
// Adding a new CLI (e.g. Codex) is a drop-in: implement Collector in a new file
// and append it to defaultAmbientCollectors. The loop, the wire type
// (AmbientUsageEntry), the endpoint, and the server rollup are all untouched —
// the whole point of the framework (completion criterion "pluggable").
//
// Scan MUST be pure with respect to persistence: it reads new usage since
// prevState — its own opaque, serialized watermark, nil/empty on first run —
// and returns the entries plus the nextState to persist. The loop commits
// nextState only AFTER a successful upload, so a failed post is retried from the
// same watermark. The daemon side is best-effort by design; the server's
// composite-key ON CONFLICT is the authoritative dedup, so a re-send is always
// a safe no-op.
type Collector interface {
	// Source is the adapter id stamped on every entry (e.g. "claude"). It also
	// selects which runtime provider the usage is attributed to.
	Source() string
	Scan(ctx context.Context, prevState json.RawMessage) (entries []AmbientUsageEntry, nextState json.RawMessage, err error)
}

// ambientBackfillVersion is the schema version of the one-time historical
// backfill. It is stamped on each collector state once that state's backfill
// pass has run. A state whose BackfillVersion is below this constant triggers a
// single windowed backfill on the next scan — which covers BOTH a brand-new
// daemon (empty state) AND an already-seeded daemon upgrading into this version
// (forward-only watermark present, BackfillVersion 0). Bumping the constant
// re-runs the backfill fleet-wide without anyone deleting state files.
const ambientBackfillVersion = 1

// defaultAmbientCollectors is the registry of active collectors. New() assigns
// it to d.collectorsFn; runAmbientUsage goes through that field so tests can
// substitute a deterministic fake.
func (d *Daemon) defaultAmbientCollectors() []Collector {
	return []Collector{
		newClaudeCollector(d.logger, d.cfg.AmbientBackfillDays),
		newCodexCollector(d.logger, d.cfg.AmbientBackfillDays),
	}
}

// collectorStore is the on-disk { source -> opaque collector state }, persisted
// next to the daemon identity so the forward-only watermark survives a bounce.
type collectorStore struct {
	Collectors map[string]json.RawMessage `json:"collectors"`
}

func (d *Daemon) ambientStatePath() (string, error) {
	dir, err := cli.ProfileDir(d.cfg.Profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ambient-usage-state.json"), nil
}

func (d *Daemon) loadCollectorStore() collectorStore {
	empty := collectorStore{Collectors: map[string]json.RawMessage{}}
	path, err := d.ambientStatePath()
	if err != nil {
		return empty
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// Missing/unreadable → empty state. First run seeds forward-only.
		return empty
	}
	var store collectorStore
	if err := json.Unmarshal(data, &store); err != nil || store.Collectors == nil {
		return empty
	}
	return store
}

func (d *Daemon) saveCollectorStore(store collectorStore) {
	path, err := d.ambientStatePath()
	if err != nil {
		return
	}
	data, err := json.Marshal(store)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		d.logger.Warn("ambient-usage: mkdir state dir failed", "error", err)
		return
	}
	// Temp + rename so a crash mid-write can't leave a half-written watermark
	// that loses the whole map (which would re-seed and silently drop history).
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		d.logger.Warn("ambient-usage: write state failed", "error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		d.logger.Warn("ambient-usage: rename state failed", "error", err)
	}
}

// selectAmbientRuntime picks the runtime ambient usage from `source` is
// attributed to. Transcripts are machine-global but the endpoint is per-runtime,
// so we attribute to a runtime of the matching provider that this daemon
// manages, chosen deterministically by id.
//
// v1 LIMITATION (N4): if this daemon serves runtimes of one provider across
// multiple workspaces, ALL machine-global ambient usage lands on the single
// runtime chosen here. Cross-workspace machine-global attribution is explicitly
// out of v1 scope. Returns "" when no matching runtime is registered yet.
func (d *Daemon) selectAmbientRuntime(source string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make([]string, 0, len(d.runtimeIndex))
	for id, rt := range d.runtimeIndex {
		if rt.Provider == source {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return ids[0]
}

// ambientUsageLoop periodically scans local CLI transcripts for token usage
// from sessions never dispatched as tasks, and reports it per-runtime. Mirrors
// gcLoop: a short startup delay (let registration settle so a runtime exists to
// attribute to), then a fixed-interval tick.
func (d *Daemon) ambientUsageLoop(ctx context.Context) {
	if !d.cfg.AmbientUsageEnabled {
		d.logger.Info("ambient-usage: disabled")
		return
	}
	d.logger.Info("ambient-usage: started", "interval", d.cfg.AmbientUsageInterval)

	if err := sleepWithContext(ctx, 30*time.Second); err != nil {
		return
	}
	d.runAmbientUsage(ctx)

	ticker := time.NewTicker(d.cfg.AmbientUsageInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.runAmbientUsage(ctx)
		}
	}
}

// runAmbientUsage performs one scan+report cycle per collector. State is
// committed only after a successful upload, so a transient post failure is
// retried from the same watermark next tick (lossless, best-effort).
func (d *Daemon) runAmbientUsage(ctx context.Context) {
	store := d.loadCollectorStore()
	changed := false

	for _, c := range d.collectorsFn() {
		target := d.selectAmbientRuntime(c.Source())
		if target == "" {
			// Nothing to attribute to yet — skip without advancing the
			// watermark so nothing is lost; retry once a runtime registers.
			continue
		}

		entries, next, err := c.Scan(ctx, store.Collectors[c.Source()])
		if err != nil {
			d.logger.Warn("ambient-usage: scan failed", "source", c.Source(), "error", err)
			continue
		}

		if len(entries) > 0 {
			if err := d.client.ReportRuntimeUsage(ctx, target, entries); err != nil {
				d.logger.Warn("ambient-usage: report failed",
					"source", c.Source(), "runtime_id", target, "count", len(entries), "error", err)
				continue // do not commit; retry from the same watermark next tick
			}
			d.logger.Info("ambient-usage: reported",
				"source", c.Source(), "runtime_id", target, "count", len(entries))
		}

		store.Collectors[c.Source()] = next
		changed = true
	}

	if changed {
		d.saveCollectorStore(store)
	}
}
