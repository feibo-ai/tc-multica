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
	banned := []string{"content", "prompt", "text", "body", "message", "cwd", "gitbranch"}
	ty := reflect.TypeOf(AmbientUsageEntry{})
	for i := 0; i < ty.NumField(); i++ {
		name := strings.ToLower(ty.Field(i).Name)
		for _, b := range banned {
			if name == b {
				t.Errorf("AmbientUsageEntry.%s looks like a content field; the collector must never carry message content (decisions/2026-06-03-local-log-privacy.md)", ty.Field(i).Name)
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
