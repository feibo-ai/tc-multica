package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// codexSource is the collector / provider id for Codex CLI rollout transcripts.
const codexSource = "codex"

// codexCollector scans ~/.codex/{sessions,archived_sessions}/**/rollout-*.jsonl
// for per-session cumulative token usage. It is the second concrete Collector;
// it mirrors claudeCollector — adding it changed nothing in the framework, only
// appended one entry to defaultAmbientCollectors.
//
// Counting model differs from Claude. Codex emits a `token_count` event whose
// `info.total_token_usage` is a per-session RUNNING TOTAL (cumulative), and the
// same total is typically emitted multiple times per turn. So we never sum
// events. Per session we reconstruct the time-ordered sequence of distinct
// cumulative SNAPSHOTS, hold a watermark = the last cumulative already emitted,
// and emit one DELTA (snapshot − watermark) per snapshot above the watermark,
// stamped with that snapshot's own timestamp. This keeps each turn's tokens on
// its real event time instead of collapsing a whole session onto one timestamp,
// and Σ(deltas) over a session equals (final cumulative − baseline) exactly.
//
// Privacy doctrine (decisions/2026-06-03-local-log-privacy.md): a rollout line
// is decoded ONLY into codexRolloutLine, whose fields are numbers and ids. The
// prompt / cwd / instructions keys on the line are never named, so encoding/json
// never populates them — message content is structurally unable to leave here.
type codexCollector struct {
	logger *slog.Logger
	root   string // ~/.codex; overridable in tests
	// backfillDays is the one-time historical window (days). <=0 disables
	// backfill and restores the legacy forward-only seed (seed at current
	// cumulative, emit nothing on first scan).
	backfillDays int
	// now returns the reference time used to derive the backfill cutoff;
	// defaults to time.Now, overridable in tests for a fixed instant.
	now func() time.Time
}

func newCodexCollector(logger *slog.Logger, backfillDays int) *codexCollector {
	root := ""
	if home, err := os.UserHomeDir(); err == nil {
		root = filepath.Join(home, ".codex")
	}
	return &codexCollector{logger: logger, root: root, backfillDays: backfillDays, now: time.Now}
}

func (c *codexCollector) Source() string { return codexSource }

// codexFileState is the per-file watermark: how far we have read and the stat we
// last saw, so an unchanged file is skipped without opening it. Mirrors
// claudeFileState.
type codexFileState struct {
	Offset    int64 `json:"offset"`
	MTimeNano int64 `json:"mtime_nano"`
	Size      int64 `json:"size"`
}

// codexSessionWatermark is the last-emitted cumulative tuple for one session.
// The next snapshot delta is (snapshot cumulative − this).
type codexSessionWatermark struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	CacheRead int64 `json:"cache_read"`
}

func (w codexSessionWatermark) total() int64 { return w.Input + w.Output + w.CacheRead }

// codexState is the collector's opaque, serialized watermark. Seeded gates the
// legacy forward-only seed (kept for backfillDays<=0 and as a marker the state
// has been written). BackfillVersion gates the one-time windowed backfill: a
// state below ambientBackfillVersion runs a single historical pass that, per
// session, baselines the watermark at the last pre-cutoff cumulative and emits
// every in-window snapshot delta. The monotonic "cum:"+total key makes a re-scan
// a server-side no-op, so an upgrade of an already-Seeded daemon backfills the
// window without double-counting.
type codexState struct {
	Seeded          bool                             `json:"seeded"`
	BackfillVersion int                              `json:"backfill_version"`
	Files           map[string]codexFileState        `json:"files"`
	Sessions        map[string]codexSessionWatermark `json:"sessions"`
}

// codexCumSnapshot is one cumulative reading observed in a token_count event:
// the per-session running totals at that event's timestamp. The collector
// reconstructs the time-ordered, deduped sequence of these per session and emits
// a delta per snapshot above the watermark.
type codexCumSnapshot struct {
	input   int64
	output  int64 // output_tokens + reasoning_output_tokens
	cache   int64
	eventAt string
}

func (s codexCumSnapshot) total() int64 { return s.input + s.output + s.cache }

// codexRolloutLine is the ONLY shape a rollout line is decoded into — numbers
// and ids, never content. Mirrors the codexSessionTokenCount struct in
// pkg/agent/codex.go (copied here to avoid exporting pkg/agent internals), with
// the session_meta id and a top-level timestamp added.
type codexRolloutLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   *struct {
		Type string `json:"type"`
		ID   string `json:"id"` // session_meta: the session UUID
		Info *struct {
			TotalTokenUsage *codexTokenUsage `json:"total_token_usage"`
			LastTokenUsage  *codexTokenUsage `json:"last_token_usage"`
			Model           string           `json:"model"`
		} `json:"info"`
		Model string `json:"model"` // turn_context: the model
	} `json:"payload"`
}

// codexTokenUsage mirrors the token tuple in pkg/agent/codex.go.
type codexTokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheReadInputTokens  int64 `json:"cache_read_input_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

// codexSessionScan accumulates the per-session facts gathered from one or more
// rollout files during a single scan, before deltas are computed.
type codexSessionScan struct {
	id    string
	model string // turn_context.payload.model (precedence) or token_count info.model
	// snapshots holds every cumulative reading observed across the session's
	// files this scan, unordered. Scan sorts + dedups them into a monotonic
	// sequence before emitting per-snapshot deltas.
	snapshots []codexCumSnapshot
	// active marks a session seen under sessions/ (vs archived_sessions/). When a
	// session UUID appears in both dirs, the active copy wins.
	active bool
}

func (c *codexCollector) Scan(ctx context.Context, prevState json.RawMessage) ([]AmbientUsageEntry, json.RawMessage, error) {
	state := codexState{
		Files:    map[string]codexFileState{},
		Sessions: map[string]codexSessionWatermark{},
	}
	if len(prevState) > 0 {
		_ = json.Unmarshal(prevState, &state) // corrupt state → treat as first run
		if state.Files == nil {
			state.Files = map[string]codexFileState{}
		}
		if state.Sessions == nil {
			state.Sessions = map[string]codexSessionWatermark{}
		}
	}
	firstRun := !state.Seeded
	// needsBackfill triggers exactly one windowed historical pass: a brand-new
	// daemon (empty state, BackfillVersion 0) and an already-Seeded daemon that
	// upgraded into this version (BackfillVersion 0) both qualify. Disabled when
	// backfillDays<=0, which keeps the legacy forward-only seed intact.
	needsBackfill := c.backfillDays > 0 && state.BackfillVersion < ambientBackfillVersion
	var cutoff time.Time
	if needsBackfill {
		cutoff = c.now().Add(-time.Duration(c.backfillDays) * 24 * time.Hour)
	}

	// Per-session accumulation across both subtrees. A session resume can write a
	// new file but Codex keys cumulative usage per session UUID, so we fold all
	// files' snapshots of a session into one time-ordered sequence.
	sessions := map[string]*codexSessionScan{}
	present := map[string]struct{}{}

	for _, sub := range []string{"sessions", "archived_sessions"} {
		if c.root == "" {
			break
		}
		active := sub == "sessions"
		dir := filepath.Join(c.root, sub)
		// WalkDir safety contract, mirrored from ambient_claude.go / diskusage.go:
		// never descend into .git, never follow symlinks, only regular
		// rollout-*.jsonl files.
		_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entry → skip, never fatal
			}
			if ctx.Err() != nil {
				return filepath.SkipAll
			}
			if entry.IsDir() {
				if entry.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			name := entry.Name()
			if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
				return nil
			}
			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() {
				return nil
			}
			present[path] = struct{}{}

			size := info.Size()
			mtime := info.ModTime().UnixNano()

			// Always parse from the start: the cumulative model needs the
			// session's full snapshot sequence (a session can span multiple
			// files; an unchanged file still contributes its snapshots to the
			// session total), and the per-snapshot watermark — not the file
			// offset — is what gates emission. The mtime/size stat is recorded
			// for the bounded-map drop logic only.
			c.scanFile(path, active, sessions)
			state.Files[path] = codexFileState{Offset: size, MTimeNano: mtime, Size: size}
			return nil
		})
	}

	// Drop watermarks for files that no longer exist so the map stays bounded.
	for path := range state.Files {
		if _, ok := present[path]; !ok {
			delete(state.Files, path)
		}
	}

	var entries []AmbientUsageEntry
	liveSessions := map[string]struct{}{}
	for id, s := range sessions {
		liveSessions[id] = struct{}{}
		// Build the monotonic, deduped cumulative sequence for this session.
		seq := codexMonotonicSnapshots(s.snapshots)
		if len(seq) == 0 {
			continue // no token_count seen → nothing to watermark or emit
		}
		final := seq[len(seq)-1]

		// Resolve the per-session baseline watermark = the last cumulative
		// already accounted for; deltas are emitted for snapshots above it.
		var prev codexSessionWatermark
		switch {
		case needsBackfill:
			// Windowed backfill: baseline at the last snapshot strictly before
			// the cutoff (the last pre-window cumulative). A session whose first
			// snapshot is already in-window baselines at zero (emit all); a
			// session entirely before the window baselines at its final
			// cumulative (emit nothing). This makes the first cross-boundary
			// delta = (first in-window cumulative − last pre-window cumulative)
			// and Σ(deltas) == final − pre-window baseline.
			base := codexBaselineBeforeCutoff(seq, cutoff)
			prev = codexSessionWatermark{Input: base.input, Output: base.output, CacheRead: base.cache}
		case firstRun:
			// Legacy forward-only seed (backfillDays<=0): baseline at the current
			// final cumulative, emit nothing. Seeding at zero would dump the whole
			// session history as one delta on the next scan.
			prev = codexSessionWatermark{Input: final.input, Output: final.output, CacheRead: final.cache}
		default:
			// Steady state: baseline at the persisted watermark (zero for a
			// never-seen session).
			prev = state.Sessions[id]
		}

		// Skip a session the server would reject anyway (empty model or id). We
		// still record its watermark below so a later scan that resolves a model
		// does not treat the whole accumulated history as new growth.
		emit := s.id != "" && s.model != ""

		// Walk snapshots in order, emitting one delta per snapshot whose
		// cumulative total exceeds the running watermark. The watermark advances
		// snapshot by snapshot so each turn lands on its own EventAt; the
		// monotonic "cum:"+total key dedups re-scans server-side.
		for _, snap := range seq {
			if snap.total() <= prev.total() {
				continue // not past the watermark (duplicate or pre-baseline)
			}
			if emit {
				entries = append(entries, AmbientUsageEntry{
					SessionID:        s.id,
					MessageID:        s.id,
					RequestID:        "cum:" + strconv.FormatInt(snap.total(), 10),
					Provider:         codexSource,
					Model:            s.model,
					EventAt:          snap.eventAt,
					InputTokens:      clampDelta(snap.input, prev.Input),
					OutputTokens:     clampDelta(snap.output, prev.Output),
					CacheReadTokens:  clampDelta(snap.cache, prev.CacheRead),
					CacheWriteTokens: 0, // Codex has no separate cache-write signal
					Source:           codexSource,
				})
			}
			prev = codexSessionWatermark{Input: snap.input, Output: snap.output, CacheRead: snap.cache}
		}
		// Persist the advanced watermark (== final cumulative once all qualifying
		// snapshots are consumed, or the seed/backfill baseline when nothing was
		// past it), so the next steady-state scan computes deltas from here and an
		// idempotent re-scan emits nothing.
		state.Sessions[id] = prev
	}

	// Drop watermarks for sessions whose files all disappeared, to keep state
	// bounded. SAFE for the normal flows: archiving moves a file
	// sessions/→archived_sessions/ but BOTH dirs are walked every scan, so the
	// session UUID never leaves liveSessions and its watermark survives the move
	// (TestCodexCollector_ArchiveMovePreservesWatermark); a permanently-deleted
	// session never reappears. KNOWN v1 EDGE: if a UUID vanishes from both dirs
	// for a full scan and later reappears (external move-out-and-back, or a
	// non-atomic archive caught mid-scan), it re-seeds at prev=0 and re-emits its
	// cumulative once — a bounded over-count. Accepted over unbounded state
	// growth; revisit with a grace-TTL if it ever bites in practice.
	for id := range state.Sessions {
		if _, ok := liveSessions[id]; !ok {
			delete(state.Sessions, id)
		}
	}

	state.Seeded = true
	if needsBackfill {
		state.BackfillVersion = ambientBackfillVersion
	}
	next, err := json.Marshal(state)
	if err != nil {
		return nil, prevState, err
	}
	return entries, next, nil
}

// codexMonotonicSnapshots sorts the raw cumulative readings into time order and
// returns a strictly-increasing-by-total sequence, collapsing duplicate and
// out-of-order readings. Sorting is by (eventAt, total) so equal timestamps keep
// the lower cumulative first; the walk then keeps only readings whose total
// exceeds the running max, carrying the per-field running max forward so a single
// field that regresses while the total grows can never lower an emitted count.
func codexMonotonicSnapshots(raw []codexCumSnapshot) []codexCumSnapshot {
	if len(raw) == 0 {
		return nil
	}
	sorted := make([]codexCumSnapshot, len(raw))
	copy(sorted, raw)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].eventAt != sorted[j].eventAt {
			return sorted[i].eventAt < sorted[j].eventAt
		}
		return sorted[i].total() < sorted[j].total()
	})

	out := make([]codexCumSnapshot, 0, len(sorted))
	var maxIn, maxOut, maxCache, maxTotal int64
	for _, s := range sorted {
		maxIn = max64(maxIn, s.input)
		maxOut = max64(maxOut, s.output)
		maxCache = max64(maxCache, s.cache)
		total := maxIn + maxOut + maxCache
		if total <= maxTotal {
			continue // duplicate or non-advancing reading
		}
		maxTotal = total
		out = append(out, codexCumSnapshot{input: maxIn, output: maxOut, cache: maxCache, eventAt: s.eventAt})
	}
	return out
}

// codexBaselineBeforeCutoff returns the cumulative of the last snapshot strictly
// before cutoff — the backfill baseline. If the first snapshot is already at or
// after cutoff (session created in-window) it returns the zero snapshot so the
// whole session is emitted; seq must be the monotonic sequence.
func codexBaselineBeforeCutoff(seq []codexCumSnapshot, cutoff time.Time) codexCumSnapshot {
	var base codexCumSnapshot
	for _, s := range seq {
		if codexEventAtBefore(s.eventAt, cutoff) {
			base = s
			continue
		}
		break
	}
	return base
}

// codexEventAtBefore reports whether the RFC3339 timestamp ts is strictly before
// cutoff. An unparseable timestamp is treated as in-window (NOT before cutoff),
// so a snapshot with a garbled time is emitted rather than silently folded into
// the pre-window baseline.
func codexEventAtBefore(ts string, cutoff time.Time) bool {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return false
	}
	return t.Before(cutoff)
}

// clampDelta returns cum-prev, floored at zero. Cumulative is monotonic so this
// is only defensive against a reset/rotation emitting a negative count.
func clampDelta(cum, prev int64) int64 {
	if cum < prev {
		return 0
	}
	return cum - prev
}

// scanFile reads a whole rollout file and folds its token_count snapshots into
// the per-session accumulator. It always reads from the start: the cumulative
// model needs the session's full snapshot sequence (sort/dedup happens later in
// Scan), and re-reading is cheap. Per-line failures are skipped, never fatal.
func (c *codexCollector) scanFile(path string, active bool, sessions map[string]*codexSessionScan) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 10*1024*1024)

	var (
		sessionID  string
		model      string // from turn_context (precedence)
		infoModel  string // from token_count info.model (fallback)
		snapshots  []codexCumSnapshot
		sawSession bool
	)

	for scanner.Scan() {
		raw := scanner.Bytes()
		// Fast pre-filter: only three event kinds carry anything we read.
		if !bytesHasCodexKeyword(raw) {
			continue
		}
		var line codexRolloutLine
		if err := json.Unmarshal(raw, &line); err != nil || line.Payload == nil {
			continue
		}
		switch {
		case line.Type == "session_meta" && line.Payload.ID != "":
			sessionID = line.Payload.ID
			sawSession = true
		case line.Type == "turn_context" && line.Payload.Model != "":
			model = line.Payload.Model
		case line.Payload.Type == "token_count" && line.Payload.Info != nil:
			u := line.Payload.Info.TotalTokenUsage
			if u == nil {
				u = line.Payload.Info.LastTokenUsage
			}
			if u == nil {
				continue // the empty "info":null warm-up event carries no usage
			}
			cache := u.CachedInputTokens
			if cache == 0 {
				cache = u.CacheReadInputTokens
			}
			// One snapshot per token_count event, carrying its own timestamp.
			// Ordering/dedup/monotonicity are resolved later across the session's
			// files in codexMonotonicSnapshots.
			snapshots = append(snapshots, codexCumSnapshot{
				input:   u.InputTokens,
				output:  u.OutputTokens + u.ReasoningOutputTokens,
				cache:   cache,
				eventAt: line.Timestamp,
			})
			if line.Payload.Info.Model != "" {
				infoModel = line.Payload.Info.Model
			}
		}
	}

	if !sawSession || sessionID == "" {
		return // can't attribute usage without a session UUID
	}

	resolvedModel := model
	if resolvedModel == "" {
		resolvedModel = infoModel
	}

	s := sessions[sessionID]
	if s == nil {
		s = &codexSessionScan{id: sessionID}
		sessions[sessionID] = s
	}
	// Active (sessions/) wins over archived for the model/usage source when the
	// same session id appears in both dirs.
	if active && !s.active {
		s.active = true
	}
	preferActive := active || !s.active

	if resolvedModel != "" && (preferActive || s.model == "") {
		s.model = resolvedModel
	}
	// Fold this file's snapshots into the session's pool (a resumed session may
	// span files; the full sequence is reconstructed across all of them).
	s.snapshots = append(s.snapshots, snapshots...)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// bytesHasCodexKeyword is the cheap pre-filter mirroring parseCodexSessionFile:
// only session_meta, turn_context, and token_count lines carry anything we read.
func bytesHasCodexKeyword(line []byte) bool {
	return bytes.Contains(line, []byte("token_count")) ||
		bytes.Contains(line, []byte("turn_context")) ||
		bytes.Contains(line, []byte("session_meta"))
}
