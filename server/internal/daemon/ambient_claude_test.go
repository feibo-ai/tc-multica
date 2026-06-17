package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// asstLine builds one assistant transcript line. `content` is the privacy
// probe — it must NEVER surface in an emitted entry.
func asstLine(session, msgID, reqID, model, ts string, in, out, cr, cw int64, content string) string {
	m := map[string]any{
		"type":      "assistant",
		"sessionId": session,
		"requestId": reqID,
		"timestamp": ts,
		"cwd":       "/Users/secret/private-repo", // noise that must be ignored
		"gitBranch": "feature/secret",
		"message": map[string]any{
			"id":      msgID,
			"model":   model,
			"role":    "assistant",
			"content": content,
			"usage": map[string]any{
				"input_tokens":                in,
				"output_tokens":               out,
				"cache_read_input_tokens":     cr,
				"cache_creation_input_tokens": cw,
			},
		},
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// TestAmbientUsageEntryHasNoContentField is the daemon-side twin of the server
// privacy test: the upload struct carries numbers and ids only.
func TestAmbientUsageEntryHasNoContentField(t *testing.T) {
	// Substring-match content words (catches OutputText / MessageBody / …);
	// exact-match words that prefix legit id/count fields (MessageID, …).
	substringBanned := []string{"content", "prompt", "text", "body", "cwd", "gitbranch"}
	exactBanned := []string{"message", "summary", "output", "input"}
	ty := reflect.TypeOf(AmbientUsageEntry{})
	for i := 0; i < ty.NumField(); i++ {
		name := strings.ToLower(ty.Field(i).Name)
		jsonTag := strings.ToLower(strings.SplitN(ty.Field(i).Tag.Get("json"), ",", 2)[0])
		for _, b := range substringBanned {
			if strings.Contains(name, b) || strings.Contains(jsonTag, b) {
				t.Errorf("AmbientUsageEntry.%s looks like a content field (matched %q); the collector must never carry message content (decisions/2026-06-03-local-log-privacy.md)", ty.Field(i).Name, b)
			}
		}
		for _, b := range exactBanned {
			if name == b || jsonTag == b {
				t.Errorf("AmbientUsageEntry.%s looks like a content field (matched %q); the collector must never carry message content (decisions/2026-06-03-local-log-privacy.md)", ty.Field(i).Name, b)
			}
		}
	}
}

func TestParseClaudeUsageLine(t *testing.T) {
	ts := "2026-04-01T08:30:00.123Z"

	t.Run("real assistant line yields numbers, never content", func(t *testing.T) {
		raw := asstLine("S1", "m1", "r1", "claude-opus-4-7", ts, 6, 951, 17997, 19513, "TOP_SECRET_PROMPT_TEXT")
		entry, ok := parseClaudeUsageLine([]byte(raw))
		if !ok {
			t.Fatal("expected a usable entry")
		}
		if entry.InputTokens != 6 || entry.OutputTokens != 951 || entry.CacheReadTokens != 17997 || entry.CacheWriteTokens != 19513 {
			t.Errorf("token mapping wrong: %+v", entry)
		}
		if entry.SessionID != "S1" || entry.MessageID != "m1" || entry.RequestID != "r1" {
			t.Errorf("id mapping wrong: %+v", entry)
		}
		if entry.Provider != "claude" || entry.Source != "claude" {
			t.Errorf("provider/source wrong: %+v", entry)
		}
		// Privacy: the entry's serialized form must not contain the content.
		b, _ := json.Marshal(entry)
		if strings.Contains(string(b), "TOP_SECRET") {
			t.Errorf("content leaked into entry: %s", b)
		}
	})

	skip := map[string]string{
		"synthetic model":    asstLine("S", "m", "r", "<synthetic>", ts, 0, 0, 0, 0, "x"),
		"all-zero usage":     asstLine("S", "m", "r", "claude-opus-4-7", ts, 0, 0, 0, 0, "x"),
		"missing session":    asstLine("", "m", "r", "claude-opus-4-7", ts, 1, 1, 0, 0, "x"),
		"missing request id": asstLine("S", "m", "", "claude-opus-4-7", ts, 1, 1, 0, 0, "x"),
		"missing timestamp":  asstLine("S", "m", "r", "claude-opus-4-7", "", 1, 1, 0, 0, "x"),
		"malformed json":     `{"type":"assistant" BROKEN`,
		"user line":          `{"type":"user","sessionId":"S","message":{"content":"hi"}}`,
	}
	for name, raw := range skip {
		t.Run("skips "+name, func(t *testing.T) {
			if _, ok := parseClaudeUsageLine([]byte(raw)); ok {
				t.Errorf("expected %q to be dropped", name)
			}
		})
	}
}

func TestClaudeCollector_ScanDedupForwardOnlyTailing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ts := "2026-04-01T08:30:00.000Z"

	projA := filepath.Join(root, "-Users-x-projA")
	if err := os.MkdirAll(projA, 0o755); err != nil {
		t.Fatal(err)
	}
	fileA := filepath.Join(projA, "sessA.jsonl")

	// Pre-existing content present at first (seed) scan: a synthetic line and a
	// real line. Forward-only means NEITHER is emitted.
	seedContent := asstLine("SA", "ms", "rs", "<synthetic>", ts, 0, 0, 0, 0, "x") + "\n" +
		asstLine("SA", "m1", "r1", "claude-opus-4-7", ts, 100, 10, 0, 0, "SEED_SECRET") + "\n"
	if err := os.WriteFile(fileA, []byte(seedContent), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &claudeCollector{logger: discardLogger(), root: root}

	// Scan 1 — seed. Emits nothing; records the watermark.
	entries, state1, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("forward-only seed must emit nothing, got %d", len(entries))
	}

	// Append to A: a real line, a DUPLICATE of it, a malformed line, another
	// real line, then a PARTIAL trailing line (no newline).
	appendA := asstLine("SA", "m2", "r2", "claude-opus-4-7", ts, 300, 30, 0, 0, "BETA_SECRET") + "\n" +
		asstLine("SA", "m2", "r2", "claude-opus-4-7", ts, 300, 30, 0, 0, "BETA_SECRET") + "\n" + // dup
		`{"type":"assistant" THIS LINE IS BROKEN` + "\n" +
		asstLine("SA", "m3", "r3", "claude-opus-4-7", ts, 5, 5, 0, 0, "GAMMA") + "\n" +
		asstLine("SA", "m4", "r4", "claude-opus-4-7", ts, 7, 7, 0, 0, "PARTIAL") // NO trailing newline
	appendToFile(t, fileA, appendA)

	// A brand-new file created after the seed: read from offset 0, with a dup.
	projB := filepath.Join(root, "-Users-x-projB")
	os.MkdirAll(projB, 0o755)
	fileB := filepath.Join(projB, "sessB.jsonl")
	bContent := asstLine("SB", "m5", "r5", "claude-opus-4-7", ts, 11, 1, 0, 0, "DELTA") + "\n" +
		asstLine("SB", "m5", "r5", "claude-opus-4-7", ts, 11, 1, 0, 0, "DELTA") + "\n" // dup
	if err := os.WriteFile(fileB, []byte(bContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Scan 2 — incremental. Expect m2 (dedup), m3 (A append), m5 (B, dedup).
	// NOT m1 (seeded), NOT the synthetic, NOT the malformed, NOT m4 (partial).
	entries, state2, err := c.Scan(ctx, state1)
	if err != nil {
		t.Fatalf("incremental scan: %v", err)
	}
	got := idSet(entries)
	want := map[string]bool{"m2": true, "m3": true, "m5": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scan 2 ids = %v, want %v", got, want)
	}
	if countID(entries, "m2") != 1 {
		t.Errorf("m2 not deduped: appears %d times", countID(entries, "m2"))
	}
	// Privacy end-to-end: no content from any line surfaces in the upload.
	blob, _ := json.Marshal(entries)
	for _, secret := range []string{"SEED_SECRET", "BETA_SECRET", "GAMMA", "DELTA", "PARTIAL"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("content %q leaked into upload: %s", secret, blob)
		}
	}

	// Complete the partial line m4 — next scan picks it up.
	appendToFile(t, fileA, "\n")
	entries, state3, err := c.Scan(ctx, state2)
	if err != nil {
		t.Fatalf("scan 3: %v", err)
	}
	if got := idSet(entries); !reflect.DeepEqual(got, map[string]bool{"m4": true}) {
		t.Fatalf("scan 3 (completed partial) ids = %v, want {m4}", got)
	}

	// Scan 4 — nothing changed → nothing emitted.
	entries, _, err = c.Scan(ctx, state3)
	if err != nil {
		t.Fatalf("scan 4: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("unchanged scan must emit nothing, got %d", len(entries))
	}
}

// TestClaudeCollector_BackfillWindow proves the windowed backfill: on the first
// scan of a backfill-enabled collector, every message with EventAt >= cutoff is
// emitted (with its original timestamp), messages older than cutoff are dropped,
// and the watermark still advances to file end. A re-scan is then idempotent.
func TestClaudeCollector_BackfillWindow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	proj := filepath.Join(root, "-Users-x-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(proj, "sess.jsonl")

	// now = 2026-06-20; cutoff = 2026-06-13 (7-day window). Two old messages
	// (day 05, day 10) must be dropped; two recent (day 14, day 16) emitted.
	content := asstLine("S", "old1", "r1", "claude-opus-4-7", "2026-06-05T00:00:00Z", 100, 10, 0, 0, "OLD1") + "\n" +
		asstLine("S", "old2", "r2", "claude-opus-4-7", "2026-06-10T00:00:00Z", 200, 20, 0, 0, "OLD2") + "\n" +
		asstLine("S", "new1", "r3", "claude-opus-4-7", "2026-06-14T00:00:00Z", 300, 30, 0, 0, "NEW1") + "\n" +
		asstLine("S", "new2", "r4", "claude-opus-4-7", "2026-06-16T00:00:00Z", 400, 40, 0, 0, "NEW2") + "\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &claudeCollector{logger: discardLogger(), root: root, backfillDays: 7, now: fixedNow("2026-06-20T00:00:00Z")}

	entries, state, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("backfill scan: %v", err)
	}
	got := idSet(entries)
	want := map[string]bool{"new1": true, "new2": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill emitted %v, want only in-window {new1,new2}", got)
	}
	// Original timestamps preserved on the backfilled entries.
	for _, e := range entries {
		if e.MessageID == "new1" && e.EventAt != "2026-06-14T00:00:00Z" {
			t.Errorf("new1 EventAt = %q, want original 2026-06-14T00:00:00Z", e.EventAt)
		}
	}
	// Privacy: no content leaks even on the backfill path.
	blob, _ := json.Marshal(entries)
	for _, secret := range []string{"OLD1", "OLD2", "NEW1", "NEW2"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("content %q leaked: %s", secret, blob)
		}
	}

	// Watermark advanced to file end → re-scan emits nothing (idempotent), and
	// BackfillVersion is now stamped so no second backfill.
	var st claudeState
	if err := json.Unmarshal(state, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if st.BackfillVersion != ambientBackfillVersion {
		t.Fatalf("BackfillVersion = %d, want %d", st.BackfillVersion, ambientBackfillVersion)
	}
	entries, state2, err := c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("re-scan: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("idempotent re-scan must emit nothing, got %d", len(entries))
	}

	// A NEW message appended after backfill is reported forward-only (steady
	// state), regardless of the window filter.
	appendToFile(t, file, asstLine("S", "post", "r5", "claude-opus-4-7", "2026-06-21T00:00:00Z", 7, 7, 0, 0, "POST")+"\n")
	entries, _, err = c.Scan(ctx, state2)
	if err != nil {
		t.Fatalf("post scan: %v", err)
	}
	if got := idSet(entries); !reflect.DeepEqual(got, map[string]bool{"post": true}) {
		t.Fatalf("post-backfill append ids = %v, want {post}", got)
	}
}

// TestClaudeCollector_BackfillUpgradeFromSeededState proves the upgrade path: a
// state already Seeded forward-only (BackfillVersion 0) re-reads each file from
// offset 0 and backfills the window once when fed to a backfill-enabled
// collector — no state-file deletion required.
func TestClaudeCollector_BackfillUpgradeFromSeededState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	proj := filepath.Join(root, "-Users-x-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(proj, "sess.jsonl")
	content := asstLine("S", "h1", "r1", "claude-opus-4-7", "2026-06-14T00:00:00Z", 100, 10, 0, 0, "H1") + "\n" +
		asstLine("S", "h2", "r2", "claude-opus-4-7", "2026-06-16T00:00:00Z", 200, 20, 0, 0, "H2") + "\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-upgrade: legacy forward-only seed (backfillDays<=0) → emits nothing,
	// Seeded=true, BackfillVersion 0.
	legacy := &claudeCollector{logger: discardLogger(), root: root}
	entries, seeded, err := legacy.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("legacy seed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("legacy seed must emit nothing, got %d", len(entries))
	}
	var st claudeState
	if err := json.Unmarshal(seeded, &st); err != nil {
		t.Fatalf("unmarshal seeded: %v", err)
	}
	if !st.Seeded || st.BackfillVersion != 0 {
		t.Fatalf("pre-upgrade should be Seeded ver=0, got seeded=%v ver=%d", st.Seeded, st.BackfillVersion)
	}

	// Upgrade: feed the same Seeded state to a backfill-enabled collector. Both
	// messages are in-window (cutoff day 13) → backfilled despite prior seed.
	upgraded := &claudeCollector{logger: discardLogger(), root: root, backfillDays: 7, now: fixedNow("2026-06-20T00:00:00Z")}
	entries, _, err = upgraded.Scan(ctx, seeded)
	if err != nil {
		t.Fatalf("upgrade scan: %v", err)
	}
	if got := idSet(entries); !reflect.DeepEqual(got, map[string]bool{"h1": true, "h2": true}) {
		t.Fatalf("upgrade backfill ids = %v, want {h1,h2}", got)
	}
}

func appendToFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func idSet(entries []AmbientUsageEntry) map[string]bool {
	out := map[string]bool{}
	for _, e := range entries {
		out[e.MessageID] = true
	}
	return out
}

func countID(entries []AmbientUsageEntry, id string) int {
	n := 0
	for _, e := range entries {
		if e.MessageID == id {
			n++
		}
	}
	return n
}
