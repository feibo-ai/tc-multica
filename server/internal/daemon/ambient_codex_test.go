package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// codexSessionMeta builds a session_meta line. `cwd` is a privacy probe — it
// must NEVER surface in an emitted entry.
func codexSessionMeta(sessionID, ts string) string {
	m := map[string]any{
		"timestamp": ts,
		"type":      "session_meta",
		"payload": map[string]any{
			"id":             sessionID,
			"timestamp":      ts,
			"cwd":            "/Users/secret/private-repo", // must be ignored
			"originator":     "multica-agent-sdk",
			"cli_version":    "0.129.0",
			"model_provider": "openai",
		},
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// codexTurnContext builds a turn_context line carrying the model.
func codexTurnContext(model, ts, instructions string) string {
	m := map[string]any{
		"timestamp": ts,
		"type":      "turn_context",
		"payload": map[string]any{
			"turn_id":           "turn-1",
			"cwd":               "/Users/secret/private-repo",
			"model":             model,
			"effort":            "high",
			"user_instructions": instructions, // privacy probe
		},
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// codexTokenCount builds an event_msg/token_count line with a CUMULATIVE
// total_token_usage. Mirrors the real shape: last_token_usage is also present
// (and must be ignored — we never sum it).
func codexTokenCount(ts string, cumIn, cumOut, cumCached, cumReasoning int64) string {
	m := map[string]any{
		"timestamp": ts,
		"type":      "event_msg",
		"payload": map[string]any{
			"type": "token_count",
			"info": map[string]any{
				"total_token_usage": map[string]any{
					"input_tokens":            cumIn,
					"output_tokens":           cumOut,
					"cached_input_tokens":     cumCached,
					"reasoning_output_tokens": cumReasoning,
					"total_tokens":            cumIn + cumOut,
				},
				"last_token_usage": map[string]any{
					"input_tokens":            int64(999), // bait: summing this 2x-inflates
					"output_tokens":           int64(7),
					"cached_input_tokens":     int64(3),
					"reasoning_output_tokens": int64(1),
				},
			},
		},
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// codexEmptyTokenCount is the real warm-up event: type token_count, info null.
const codexEmptyTokenCount = `{"timestamp":"2026-06-15T01:30:39.523Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{}}}`

func writeCodexRollout(t *testing.T, root, sub, fname string, lines ...string) string {
	t.Helper()
	// Real layout: sessions/YYYY/MM/DD/rollout-*.jsonl (nested);
	// archived_sessions/rollout-*.jsonl (flat).
	var dir string
	if sub == "sessions" {
		dir = filepath.Join(root, "sessions", "2026", "06", "15")
	} else {
		dir = filepath.Join(root, sub)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fname)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func sumDeltas(entries []AmbientUsageEntry) (in, out, cr int64) {
	for _, e := range entries {
		in += e.InputTokens
		out += e.OutputTokens
		cr += e.CacheReadTokens
	}
	return
}

// TestCodexCollector_CumulativeDeltaExactSum is the key correctness assertion:
// across multiple scans of a growing file, the SUM of emitted deltas equals the
// session's FINAL cumulative total_token_usage exactly — never the 2x a naive
// sum-of-events would produce.
func TestCodexCollector_CumulativeDeltaExactSum(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := &codexCollector{logger: discardLogger(), root: root}

	sid := "019ec8e6-bf12-7543-a4b9-4172b53417b1"
	ts := "2026-06-15T01:30:00.000Z"

	// Seed file: meta + turn_context + the EMPTY warm-up token_count only — NO
	// usage yet. Forward-only: scan 1 emits nothing and seeds the session at
	// zero, so every token_count appended after is pure growth and the Σ of
	// emitted deltas equals the session's FINAL cumulative EXACTLY.
	path := writeCodexRollout(t, root, "sessions", "rollout-seed-"+sid+".jsonl",
		codexSessionMeta(sid, ts),
		codexTurnContext("gpt-5.5", ts, "SECRET_INSTRUCTIONS"),
		codexEmptyTokenCount,
	)

	entries, state, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("forward-only seed must emit nothing, got %d", len(entries))
	}

	var allDeltas []AmbientUsageEntry

	// Each subsequent append grows the cumulative; each new cumulative is emitted
	// twice (the real duplication). Final cumulative after all appends is the
	// target — the Σ of deltas must equal it exactly, not 2x.
	type step struct{ in, out, cached, reasoning int64 }
	steps := []step{
		{250, 30, 200, 12},
		{600, 55, 480, 20},
		{1500, 120, 1200, 60},
	}
	tsN := 11
	for _, s := range steps {
		line1 := codexTokenCount("2026-06-15T01:30:"+pad(tsN)+".000Z", s.in, s.out, s.cached, s.reasoning)
		line2 := codexTokenCount("2026-06-15T01:30:"+pad(tsN)+".001Z", s.in, s.out, s.cached, s.reasoning) // dup
		appendToFile(t, path, line1+"\n"+line2+"\n")
		tsN++

		var got []AmbientUsageEntry
		got, state, err = c.Scan(ctx, state)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		allDeltas = append(allDeltas, got...)
	}

	// Final cumulative = last step (seed was zero, so Σ deltas == final exactly).
	last := steps[len(steps)-1]
	wantIn := last.in
	wantOut := last.out + last.reasoning // OutputTokens = output + reasoning
	wantCr := last.cached

	gotIn, gotOut, gotCr := sumDeltas(allDeltas)
	if gotIn != wantIn || gotOut != wantOut || gotCr != wantCr {
		t.Fatalf("sum of deltas = (in=%d out=%d cr=%d), want (in=%d out=%d cr=%d) — the session's final cumulative",
			gotIn, gotOut, gotCr, wantIn, wantOut, wantCr)
	}

	// Privacy: nothing from the transcript content surfaces.
	blob, _ := json.Marshal(allDeltas)
	for _, secret := range []string{"SECRET_INSTRUCTIONS", "private-repo"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("content %q leaked into upload: %s", secret, blob)
		}
	}
}

// TestCodexCollector_DuplicateCumulativeNoInflate proves duplicate-cumulative
// emissions in a single file do not inflate: one growth step emitted N times
// yields exactly one delta of the growth, not N×.
func TestCodexCollector_DuplicateCumulativeNoInflate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := &codexCollector{logger: discardLogger(), root: root}

	sid := "dup-session-uuid"
	ts := "2026-06-15T01:30:00.000Z"

	// Seed at cumulative 100/10/80.
	path := writeCodexRollout(t, root, "sessions", "rollout-dup-"+sid+".jsonl",
		codexSessionMeta(sid, ts),
		codexTurnContext("gpt-5.5", ts, ""),
		codexTokenCount("2026-06-15T01:30:05.000Z", 100, 10, 80, 4),
	)
	_, state, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Append the SAME new cumulative (500/40/400) five times.
	for i := 0; i < 5; i++ {
		appendToFile(t, path, codexTokenCount("2026-06-15T01:30:0"+strconv.Itoa(i+6)+".000Z", 500, 40, 400, 16)+"\n")
	}
	entries, _, err := c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("duplicate cumulative must yield exactly ONE delta, got %d (naive sum would be ~5x)", len(entries))
	}
	e := entries[0]
	// delta = (500-100, (40+16)-(10+4), 400-80) = (400, 42, 320).
	if e.InputTokens != 400 || e.OutputTokens != 42 || e.CacheReadTokens != 320 {
		t.Fatalf("delta wrong: in=%d out=%d cr=%d, want 400/42/320", e.InputTokens, e.OutputTokens, e.CacheReadTokens)
	}
}

// TestCodexCollector_IdempotentRescan proves an unchanged file emits nothing on
// re-scan.
func TestCodexCollector_IdempotentRescan(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := &codexCollector{logger: discardLogger(), root: root}

	sid := "idem-session-uuid"
	ts := "2026-06-15T01:30:00.000Z"
	path := writeCodexRollout(t, root, "sessions", "rollout-idem-"+sid+".jsonl",
		codexSessionMeta(sid, ts),
		codexTurnContext("gpt-5.5", ts, ""),
		codexTokenCount("2026-06-15T01:30:05.000Z", 100, 10, 80, 4),
	)
	_, state, err := c.Scan(ctx, nil) // seed
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Grow once and scan → one delta.
	appendToFile(t, path, codexTokenCount("2026-06-15T01:30:06.000Z", 300, 30, 240, 12)+"\n")
	entries, state, err := c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 delta after growth, got %d", len(entries))
	}
	// Re-scan with NO change → nothing.
	entries, _, err = c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("unchanged re-scan must emit nothing, got %d", len(entries))
	}
}

// TestCodexCollector_ForwardOnlySeed proves a pre-existing file's history is
// never backfilled: scan 1 emits nothing, and only content appended AFTER the
// seed is reported.
func TestCodexCollector_ForwardOnlySeed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := &codexCollector{logger: discardLogger(), root: root}

	sid := "fwd-session-uuid"
	ts := "2026-06-15T01:30:00.000Z"
	// A pre-existing session with substantial history (5000 cumulative input).
	path := writeCodexRollout(t, root, "sessions", "rollout-fwd-"+sid+".jsonl",
		codexSessionMeta(sid, ts),
		codexTurnContext("gpt-5.5", ts, ""),
		codexTokenCount("2026-06-15T01:30:05.000Z", 5000, 400, 4000, 200),
	)
	entries, state, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("forward-only seed must NOT backfill history, got %d entries", len(entries))
	}

	// Append a small growth (5000 → 5100 input). Only the +100 delta is reported,
	// never the 5000 of pre-seed history.
	appendToFile(t, path, codexTokenCount("2026-06-15T01:30:06.000Z", 5100, 405, 4080, 202)+"\n")
	entries, _, err = c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 post-seed delta, got %d", len(entries))
	}
	e := entries[0]
	// delta = (5100-5000, (405+202)-(400+200), 4080-4000) = (100, 7, 80).
	if e.InputTokens != 100 || e.OutputTokens != 7 || e.CacheReadTokens != 80 {
		t.Fatalf("post-seed delta wrong: in=%d out=%d cr=%d, want 100/7/80 (NOT the pre-seed 5000)",
			e.InputTokens, e.OutputTokens, e.CacheReadTokens)
	}
}

// TestCodexCollector_ModelResolutionAndRequestID covers: model resolves from
// turn_context (non-empty); the RequestID is "cum:"+total and differs per delta;
// a session with no model is skipped.
func TestCodexCollector_ModelResolutionAndRequestID(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := &codexCollector{logger: discardLogger(), root: root}

	sid := "model-session-uuid"
	ts := "2026-06-15T01:30:00.000Z"
	path := writeCodexRollout(t, root, "sessions", "rollout-model-"+sid+".jsonl",
		codexSessionMeta(sid, ts),
		codexTurnContext("gpt-5.5-codex", ts, ""),
		codexTokenCount("2026-06-15T01:30:05.000Z", 100, 10, 80, 4),
	)
	_, state, err := c.Scan(ctx, nil) // seed
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Two distinct growth steps in separate scans → two entries with different
	// "cum:" request ids.
	appendToFile(t, path, codexTokenCount("2026-06-15T01:30:06.000Z", 300, 30, 240, 12)+"\n")
	e1, state, err := c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("scan1: %v", err)
	}
	appendToFile(t, path, codexTokenCount("2026-06-15T01:30:07.000Z", 700, 60, 560, 24)+"\n")
	e2, _, err := c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("scan2: %v", err)
	}
	if len(e1) != 1 || len(e2) != 1 {
		t.Fatalf("expected 1 entry per growth scan, got %d and %d", len(e1), len(e2))
	}
	if e1[0].Model != "gpt-5.5-codex" {
		t.Errorf("model from turn_context wrong: %q", e1[0].Model)
	}
	if e1[0].Provider != "codex" || e1[0].Source != "codex" {
		t.Errorf("provider/source wrong: %+v", e1[0])
	}
	// RequestID == "cum:" + the session's final cumulative total of that scan,
	// where total = input + (output+reasoning) + cacheRead (the three mapped
	// fields). For scan 1 the cumulative is 300/30+12/240 → 300+42+240 = 582.
	want1 := "cum:" + strconv.FormatInt(300+(30+12)+240, 10)
	want2 := "cum:" + strconv.FormatInt(700+(60+24)+560, 10)
	if e1[0].RequestID != want1 {
		t.Errorf("RequestID1 = %q, want %q", e1[0].RequestID, want1)
	}
	if e2[0].RequestID != want2 {
		t.Errorf("RequestID2 = %q, want %q", e2[0].RequestID, want2)
	}
	if e1[0].RequestID == e2[0].RequestID {
		t.Error("RequestID must differ per delta (monotonic cumulative total)")
	}
	// SessionID == MessageID == session UUID.
	if e1[0].SessionID != sid || e1[0].MessageID != sid {
		t.Errorf("session/message id should both be the session UUID, got %q/%q", e1[0].SessionID, e1[0].MessageID)
	}
	// EventAt is the last token_count timestamp of that scan.
	if e2[0].EventAt != "2026-06-15T01:30:07.000Z" {
		t.Errorf("EventAt = %q, want last token_count ts", e2[0].EventAt)
	}

	// A session with NO model (no turn_context, token_count info.model empty) is
	// skipped — it would be server-rejected.
	noModelSID := "no-model-session"
	writeCodexRollout(t, root, "sessions", "rollout-nomodel-"+noModelSID+".jsonl",
		codexSessionMeta(noModelSID, ts),
		codexTokenCount("2026-06-15T01:30:05.000Z", 100, 10, 80, 4),
	)
	// Fresh collector + state so this session is seen for the first time and we
	// can append growth to it; the no-model session must never emit.
	c2 := &codexCollector{logger: discardLogger(), root: root}
	_, st2, err := c2.Scan(ctx, nil) // seed everything
	if err != nil {
		t.Fatalf("seed2: %v", err)
	}
	noModelPath := filepath.Join(root, "sessions", "2026", "06", "15", "rollout-nomodel-"+noModelSID+".jsonl")
	appendToFile(t, noModelPath, codexTokenCount("2026-06-15T01:30:09.000Z", 500, 40, 400, 16)+"\n")
	entries, _, err := c2.Scan(ctx, st2)
	if err != nil {
		t.Fatalf("scan no-model: %v", err)
	}
	for _, e := range entries {
		if e.SessionID == noModelSID {
			t.Errorf("a session with empty model must be skipped, but it emitted: %+v", e)
		}
	}
	_ = path
}

// TestCodexCollector_ArchivedDirAndDedup proves the collector walks both
// sessions/ and archived_sessions/, and that a session appearing in both is
// counted once (active sessions/ wins).
func TestCodexCollector_ArchivedDirAndDedup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := &codexCollector{logger: discardLogger(), root: root}

	ts := "2026-06-15T01:30:00.000Z"

	// An archived-only session.
	archSID := "archived-only-uuid"
	writeCodexRollout(t, root, "archived_sessions", "rollout-arch-"+archSID+".jsonl",
		codexSessionMeta(archSID, ts),
		codexTurnContext("gpt-5.5", ts, ""),
		codexTokenCount("2026-06-15T01:30:05.000Z", 100, 10, 80, 4),
	)
	// A session present in BOTH dirs (same UUID). The active copy carries a
	// higher cumulative; archived a stale lower one. Dedup → active wins, counted
	// once.
	dupSID := "both-dirs-uuid"
	archPath := writeCodexRollout(t, root, "archived_sessions", "rollout-both-"+dupSID+".jsonl",
		codexSessionMeta(dupSID, ts),
		codexTurnContext("gpt-5.5", ts, ""),
		codexTokenCount("2026-06-15T01:30:05.000Z", 200, 20, 160, 8),
	)
	activePath := writeCodexRollout(t, root, "sessions", "rollout-both-"+dupSID+".jsonl",
		codexSessionMeta(dupSID, ts),
		codexTurnContext("gpt-5.5", ts, ""),
		codexTokenCount("2026-06-15T01:30:05.000Z", 200, 20, 160, 8),
	)

	_, state, err := c.Scan(ctx, nil) // seed
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Grow the archived-only session and the active copy of the dup session.
	appendToFile(t, filepath.Join(root, "archived_sessions", "rollout-arch-"+archSID+".jsonl"),
		codexTokenCount("2026-06-15T01:30:06.000Z", 300, 25, 240, 10)+"\n")
	appendToFile(t, activePath, codexTokenCount("2026-06-15T01:30:06.000Z", 500, 45, 400, 18)+"\n")

	entries, _, err := c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	bySession := map[string]int{}
	for _, e := range entries {
		bySession[e.SessionID]++
	}
	if bySession[archSID] != 1 {
		t.Errorf("archived-only session should emit exactly 1 delta, got %d", bySession[archSID])
	}
	if bySession[dupSID] != 1 {
		t.Errorf("session in both dirs must be deduped to 1 delta, got %d", bySession[dupSID])
	}
	_ = archPath
}

// TestCodexCollector_ArchiveMovePreservesWatermark locks the design's hinge:
// when Codex archives a session (file moves sessions/ → archived_sessions/,
// same UUID, no content change), the UUID-keyed watermark survives the move so
// the already-counted cumulative is NOT re-emitted. Both dirs are walked every
// scan, so the session never leaves liveSessions — this is the normal flow the
// watermark-drop loop relies on.
func TestCodexCollector_ArchiveMovePreservesWatermark(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := &codexCollector{logger: discardLogger(), root: root}

	sid := "archive-move-uuid"
	ts := "2026-06-15T01:30:00.000Z"
	srcPath := writeCodexRollout(t, root, "sessions", "rollout-move-"+sid+".jsonl",
		codexSessionMeta(sid, ts),
		codexTurnContext("gpt-5.5", ts, ""),
		codexTokenCount("2026-06-15T01:30:05.000Z", 1000, 50, 800, 10),
	)
	_, state, err := c.Scan(ctx, nil) // seed at 1000 cumulative, emit nothing
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	appendToFile(t, srcPath, codexTokenCount("2026-06-15T01:30:06.000Z", 1100, 57, 880, 11)+"\n")
	entries, state, err := c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("grow scan: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("growth must emit exactly 1 delta, got %d", len(entries))
	}

	// ARCHIVE: move sessions/ → archived_sessions/ (flat), same UUID.
	dstDir := filepath.Join(root, "archived_sessions")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(srcPath, filepath.Join(dstDir, "rollout-move-"+sid+".jsonl")); err != nil {
		t.Fatalf("archive move: %v", err)
	}

	// Re-scan: UUID now under archived_sessions/ with the SAME final cumulative.
	// Watermark must survive the move → delta 0 → nothing re-emitted.
	entries, _, err = c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("post-archive scan: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("archive move must not re-emit (UUID-keyed watermark survives), got %d: %+v", len(entries), entries)
	}
}

func pad(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
