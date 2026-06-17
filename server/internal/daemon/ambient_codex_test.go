package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// fixedNow returns a now() func pinned to ts for deterministic cutoff math.
func fixedNow(ts string) func() time.Time {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return t }
}

// backfillCodex builds a codex collector with the one-time backfill enabled and
// a fixed clock, so cutoff = nowTS - days*24h is deterministic.
func backfillCodex(root string, days int, nowTS string) *codexCollector {
	return &codexCollector{logger: discardLogger(), root: root, backfillDays: days, now: fixedNow(nowTS)}
}

// TestCodexCollector_PerSnapshotMultiEmit proves the per-snapshot model: a single
// scan that observes multiple distinct cumulative snapshots emits ONE delta per
// snapshot (each on its own EventAt), and Σ(deltas) == the session's final
// cumulative when baselined at zero. This is the steady-state regression for the
// W4 "种子后新建会话全量压单一时间戳" fix.
func TestCodexCollector_PerSnapshotMultiEmit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// Forward-only (no backfill): a brand-new session created AFTER the seed.
	c := &codexCollector{logger: discardLogger(), root: root}

	sid := "per-snapshot-uuid"
	ts := "2026-06-15T01:30:00.000Z"
	// Seed scan over an empty tree → nothing, Seeded=true.
	_, state, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A new session file appears with THREE distinct cumulative snapshots at
	// three timestamps, all in one tick.
	writeCodexRollout(t, root, "sessions", "rollout-multi-"+sid+".jsonl",
		codexSessionMeta(sid, ts),
		codexTurnContext("gpt-5.5", ts, ""),
		codexTokenCount("2026-06-15T01:30:05.000Z", 100, 10, 80, 2),
		codexTokenCount("2026-06-15T01:30:06.000Z", 300, 25, 240, 5),
		codexTokenCount("2026-06-15T01:30:07.000Z", 700, 60, 560, 11),
	)
	entries, _, err := c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// A new session baselines at zero in steady state → one delta per snapshot.
	if len(entries) != 3 {
		t.Fatalf("expected 3 per-snapshot deltas, got %d: %+v", len(entries), entries)
	}
	// Each delta carries its OWN snapshot timestamp, not a single collapsed one.
	wantTS := []string{"2026-06-15T01:30:05.000Z", "2026-06-15T01:30:06.000Z", "2026-06-15T01:30:07.000Z"}
	for i, e := range entries {
		if e.EventAt != wantTS[i] {
			t.Errorf("delta %d EventAt = %q, want %q", i, e.EventAt, wantTS[i])
		}
	}
	// Σ deltas == final cumulative (input=700, output=60+11=71, cache=560).
	gotIn, gotOut, gotCr := sumDeltas(entries)
	if gotIn != 700 || gotOut != 71 || gotCr != 560 {
		t.Fatalf("Σ deltas = (in=%d out=%d cr=%d), want final (700/71/560)", gotIn, gotOut, gotCr)
	}
}

// TestCodexCollector_BackfillWindowStraddle is the cross-window-boundary
// invariant: a session with snapshots both before and after the cutoff baselines
// at the last pre-window cumulative, so the first emitted delta bridges the
// boundary and Σ(deltas) == final − pre-window baseline (NOT final from zero).
func TestCodexCollector_BackfillWindowStraddle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// now = day 20; cutoff = day 13 (7-day window). Snapshots on day 10/11 are
	// pre-window; day 14/15 are in-window.
	c := backfillCodex(root, 7, "2026-06-20T00:00:00Z")

	sid := "straddle-uuid"
	meta := "2026-06-10T00:00:00Z"
	writeCodexRollout(t, root, "sessions", "rollout-straddle-"+sid+".jsonl",
		codexSessionMeta(sid, meta),
		codexTurnContext("gpt-5.5", meta, ""),
		// pre-window
		codexTokenCount("2026-06-10T00:00:00Z", 1000, 100, 800, 0),
		codexTokenCount("2026-06-11T00:00:00Z", 2000, 200, 1600, 0),
		// in-window
		codexTokenCount("2026-06-14T00:00:00Z", 3000, 300, 2400, 0),
		codexTokenCount("2026-06-15T00:00:00Z", 5000, 500, 4000, 0),
	)

	entries, state, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("backfill scan: %v", err)
	}
	// Two in-window snapshots → two deltas. The first bridges the boundary:
	// (3000−2000, 300−200, 2400−1600) = (1000,100,800). The second:
	// (5000−3000, 500−300, 4000−2400) = (2000,200,1600).
	if len(entries) != 2 {
		t.Fatalf("expected 2 in-window deltas, got %d: %+v", len(entries), entries)
	}
	if entries[0].EventAt != "2026-06-14T00:00:00Z" || entries[1].EventAt != "2026-06-15T00:00:00Z" {
		t.Errorf("delta timestamps wrong: %q, %q", entries[0].EventAt, entries[1].EventAt)
	}
	if entries[0].InputTokens != 1000 || entries[0].OutputTokens != 100 || entries[0].CacheReadTokens != 800 {
		t.Errorf("boundary delta wrong: %+v, want 1000/100/800", entries[0])
	}
	// Σ == final − pre-window baseline = (5000−2000, 500−200, 4000−1600) = (3000,300,2400).
	gotIn, gotOut, gotCr := sumDeltas(entries)
	if gotIn != 3000 || gotOut != 300 || gotCr != 2400 {
		t.Fatalf("Σ deltas = (in=%d out=%d cr=%d), want (3000/300/2400) = final − pre-window baseline", gotIn, gotOut, gotCr)
	}

	// Re-scan with no change → idempotent (watermark advanced to final).
	entries, _, err = c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("idempotent re-scan after backfill must emit nothing, got %d", len(entries))
	}
}

// TestCodexCollector_BackfillSessionEntirelyInWindow proves a session whose first
// snapshot is already in-window baselines at zero → the whole session is emitted.
func TestCodexCollector_BackfillSessionEntirelyInWindow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := backfillCodex(root, 7, "2026-06-20T00:00:00Z") // cutoff = day 13

	sid := "in-window-uuid"
	meta := "2026-06-16T00:00:00Z"
	writeCodexRollout(t, root, "sessions", "rollout-inwin-"+sid+".jsonl",
		codexSessionMeta(sid, meta),
		codexTurnContext("gpt-5.5", meta, ""),
		codexTokenCount("2026-06-16T00:00:00Z", 500, 50, 400, 0),
		codexTokenCount("2026-06-17T00:00:00Z", 900, 90, 720, 0),
	)
	entries, _, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("backfill scan: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 deltas (whole session in-window), got %d", len(entries))
	}
	// Σ == final from zero baseline = (900,90,720).
	gotIn, gotOut, gotCr := sumDeltas(entries)
	if gotIn != 900 || gotOut != 90 || gotCr != 720 {
		t.Fatalf("Σ deltas = (in=%d out=%d cr=%d), want final-from-zero (900/90/720)", gotIn, gotOut, gotCr)
	}
}

// TestCodexCollector_BackfillSessionEntirelyOutOfWindow proves a session whose
// snapshots are all before the cutoff baselines at its final cumulative → emits
// nothing, but the watermark is still set to final so later growth is correct.
func TestCodexCollector_BackfillSessionEntirelyOutOfWindow(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := backfillCodex(root, 7, "2026-06-20T00:00:00Z") // cutoff = day 13

	sid := "out-window-uuid"
	meta := "2026-06-01T00:00:00Z"
	path := writeCodexRollout(t, root, "sessions", "rollout-outwin-"+sid+".jsonl",
		codexSessionMeta(sid, meta),
		codexTurnContext("gpt-5.5", meta, ""),
		codexTokenCount("2026-06-05T00:00:00Z", 1000, 100, 800, 0),
		codexTokenCount("2026-06-06T00:00:00Z", 2000, 200, 1600, 0),
	)
	entries, state, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("backfill scan: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("session entirely before cutoff must emit nothing, got %d: %+v", len(entries), entries)
	}

	// New growth AFTER the backfill scan: only the +delta beyond the seeded final
	// (2000/200/1600) is reported, never the pre-window 2000.
	appendToFile(t, path, codexTokenCount("2026-06-21T00:00:00Z", 2100, 210, 1680, 0)+"\n")
	entries, _, err = c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("post-backfill growth scan: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 growth delta, got %d", len(entries))
	}
	e := entries[0]
	if e.InputTokens != 100 || e.OutputTokens != 10 || e.CacheReadTokens != 80 {
		t.Fatalf("growth delta wrong: in=%d out=%d cr=%d, want 100/10/80", e.InputTokens, e.OutputTokens, e.CacheReadTokens)
	}
}

// TestCodexCollector_AdjacentEqualCumulativeDedup proves identical adjacent
// cumulative snapshots (the real duplicate-emission pattern) collapse to one
// snapshot, so a single growth step yields exactly one delta even when emitted
// many times across the file — in a backfill pass.
func TestCodexCollector_AdjacentEqualCumulativeDedup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := backfillCodex(root, 30, "2026-06-20T00:00:00Z") // wide window: everything in

	sid := "dedup-window-uuid"
	meta := "2026-06-18T00:00:00Z"
	// Same cumulative 500/50/400 emitted 4×, then a single growth to 800/80/640.
	writeCodexRollout(t, root, "sessions", "rollout-dedupwin-"+sid+".jsonl",
		codexSessionMeta(sid, meta),
		codexTurnContext("gpt-5.5", meta, ""),
		codexTokenCount("2026-06-18T00:00:01Z", 500, 50, 400, 0),
		codexTokenCount("2026-06-18T00:00:02Z", 500, 50, 400, 0),
		codexTokenCount("2026-06-18T00:00:03Z", 500, 50, 400, 0),
		codexTokenCount("2026-06-18T00:00:04Z", 500, 50, 400, 0),
		codexTokenCount("2026-06-18T00:00:05Z", 800, 80, 640, 0),
	)
	entries, _, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// Two distinct cumulative levels (500 then 800) from zero baseline → 2 deltas.
	if len(entries) != 2 {
		t.Fatalf("adjacent-equal cumulative must collapse to distinct levels (2 deltas), got %d: %+v", len(entries), entries)
	}
	gotIn, gotOut, gotCr := sumDeltas(entries)
	if gotIn != 800 || gotOut != 80 || gotCr != 640 {
		t.Fatalf("Σ deltas = (in=%d out=%d cr=%d), want final (800/80/640)", gotIn, gotOut, gotCr)
	}
}

// TestCodexCollector_BackfillVersionTriggerOnce locks the one-time trigger: a
// state whose BackfillVersion is behind runs the windowed backfill once, stamps
// the version, and a subsequent unchanged scan does NOT re-backfill.
func TestCodexCollector_BackfillVersionTriggerOnce(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	c := backfillCodex(root, 7, "2026-06-20T00:00:00Z")

	sid := "trigger-uuid"
	meta := "2026-06-15T00:00:00Z"
	writeCodexRollout(t, root, "sessions", "rollout-trig-"+sid+".jsonl",
		codexSessionMeta(sid, meta),
		codexTurnContext("gpt-5.5", meta, ""),
		codexTokenCount("2026-06-15T00:00:01Z", 400, 40, 320, 0),
	)

	// First scan: backfill runs (session in-window) → emits, stamps version.
	entries, state, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("backfill-behind state must emit on first scan")
	}
	var st codexState
	if err := json.Unmarshal(state, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if st.BackfillVersion != ambientBackfillVersion {
		t.Fatalf("BackfillVersion = %d, want %d after backfill pass", st.BackfillVersion, ambientBackfillVersion)
	}

	// Second scan, no change: must NOT re-backfill (idempotent).
	entries, _, err = c.Scan(ctx, state)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("backfill must run once; second unchanged scan emitted %d", len(entries))
	}
}

// TestCodexCollector_BackfillUpgradeFromSeededState proves the upgrade path: an
// already-Seeded state with BackfillVersion 0 (a daemon that previously ran the
// legacy forward-only seed) triggers the windowed backfill on the next scan
// WITHOUT anyone deleting the state file.
func TestCodexCollector_BackfillUpgradeFromSeededState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	sid := "upgrade-uuid"
	meta := "2026-06-15T00:00:00Z"
	writeCodexRollout(t, root, "sessions", "rollout-upg-"+sid+".jsonl",
		codexSessionMeta(sid, meta),
		codexTurnContext("gpt-5.5", meta, ""),
		codexTokenCount("2026-06-16T00:00:00Z", 600, 60, 480, 0),
		codexTokenCount("2026-06-17T00:00:00Z", 900, 90, 720, 0),
	)

	// Simulate a pre-upgrade daemon: backfillDays<=0 → legacy forward-only seed.
	legacy := &codexCollector{logger: discardLogger(), root: root}
	entries, seededState, err := legacy.Scan(ctx, nil)
	if err != nil {
		t.Fatalf("legacy seed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("legacy forward-only seed must emit nothing, got %d", len(entries))
	}
	var st codexState
	if err := json.Unmarshal(seededState, &st); err != nil {
		t.Fatalf("unmarshal seeded: %v", err)
	}
	if !st.Seeded || st.BackfillVersion != 0 {
		t.Fatalf("pre-upgrade state should be Seeded with BackfillVersion 0, got seeded=%v ver=%d", st.Seeded, st.BackfillVersion)
	}

	// Upgrade: same on-disk state, now fed to a backfill-enabled collector.
	upgraded := backfillCodex(root, 7, "2026-06-20T00:00:00Z") // cutoff day 13; both snapshots in-window
	entries, _, err = upgraded.Scan(ctx, seededState)
	if err != nil {
		t.Fatalf("upgrade scan: %v", err)
	}
	// The whole in-window session is emitted even though the state was already
	// Seeded forward-only (uq key dedups any server-side overlap).
	if len(entries) != 2 {
		t.Fatalf("upgrade backfill must emit the in-window history, got %d: %+v", len(entries), entries)
	}
	gotIn, gotOut, gotCr := sumDeltas(entries)
	if gotIn != 900 || gotOut != 90 || gotCr != 720 {
		t.Fatalf("Σ deltas = (in=%d out=%d cr=%d), want final (900/90/720)", gotIn, gotOut, gotCr)
	}
}

func pad(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
