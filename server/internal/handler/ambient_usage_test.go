package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestAmbientUsagePayloadHasNoContentField is the type-level enforcement of the
// privacy doctrine (decisions/2026-06-03-local-log-privacy.md): the wire struct
// the daemon uploads carries numbers and ids ONLY. If a future change adds a
// field that could smuggle message content / prompt text, this fails closed.
func TestAmbientUsagePayloadHasNoContentField(t *testing.T) {
	banned := []string{"content", "prompt", "text", "body", "message", "summary", "output", "input_text"}
	ty := reflect.TypeOf(AmbientUsagePayload{})
	for i := 0; i < ty.NumField(); i++ {
		name := strings.ToLower(ty.Field(i).Name)
		jsonTag := strings.ToLower(ty.Field(i).Tag.Get("json"))
		for _, b := range banned {
			// input_tokens / output_tokens are counts, not text — only flag a
			// field whose name IS a banned word, not one that contains "input".
			if name == b || strings.HasPrefix(jsonTag, b+",") || jsonTag == b {
				t.Errorf("AmbientUsagePayload.%s looks like a content field (matched %q); "+
					"the collector must never carry message content — see decisions/2026-06-03-local-log-privacy.md",
					ty.Field(i).Name, b)
			}
		}
	}
}

func TestNormalizeAmbientUsage(t *testing.T) {
	valid := AmbientUsagePayload{
		SessionID: "sess", MessageID: "msg", RequestID: "req",
		Provider: "claude", Model: "claude-sonnet-4-6",
		EventAt:     "2026-06-03T10:00:00Z",
		InputTokens: 100, OutputTokens: 10, CacheReadTokens: 5, CacheWriteTokens: 2,
		Source: "claude",
	}

	t.Run("valid passes and normalizes to UTC", func(t *testing.T) {
		p, ok := normalizeAmbientUsage(valid)
		if !ok {
			t.Fatal("expected valid event to pass")
		}
		if p.SessionID != "sess" || p.MessageID != "msg" || p.RequestID != "req" {
			t.Errorf("ids not preserved: %+v", p)
		}
		if !p.EventAt.Valid || p.EventAt.Time.UTC().Hour() != 10 {
			t.Errorf("event_at not parsed to UTC: %+v", p.EventAt)
		}
		if p.InputTokens != 100 || p.CacheWriteTokens != 2 {
			t.Errorf("token counts not preserved: %+v", p)
		}
		// Handler fills these from the resolved runtime — never the client.
		if p.WorkspaceID.Valid || p.RuntimeID.Valid {
			t.Errorf("normalize must not set workspace/runtime id, got %+v", p)
		}
	})

	// Fail-soft: a malformed event is skipped, not fatal (坑#3).
	skipCases := map[string]func(AmbientUsagePayload) AmbientUsagePayload{
		"missing session id": func(u AmbientUsagePayload) AmbientUsagePayload { u.SessionID = ""; return u },
		"missing message id": func(u AmbientUsagePayload) AmbientUsagePayload { u.MessageID = " "; return u },
		"missing request id": func(u AmbientUsagePayload) AmbientUsagePayload { u.RequestID = ""; return u },
		"empty model":        func(u AmbientUsagePayload) AmbientUsagePayload { u.Model = ""; return u },
		"synthetic model":    func(u AmbientUsagePayload) AmbientUsagePayload { u.Model = "<synthetic>"; return u },
		"unparseable time":   func(u AmbientUsagePayload) AmbientUsagePayload { u.EventAt = "not-a-time"; return u },
		"empty time":         func(u AmbientUsagePayload) AmbientUsagePayload { u.EventAt = ""; return u },
	}
	for name, mutate := range skipCases {
		t.Run("skips "+name, func(t *testing.T) {
			if _, ok := normalizeAmbientUsage(mutate(valid)); ok {
				t.Errorf("expected %q to be skipped", name)
			}
		})
	}

	t.Run("clamps negative counts and defaults source", func(t *testing.T) {
		u := valid
		u.InputTokens = -5
		u.Source = ""
		p, ok := normalizeAmbientUsage(u)
		if !ok {
			t.Fatal("expected pass")
		}
		if p.InputTokens != 0 {
			t.Errorf("negative input not clamped: %d", p.InputTokens)
		}
		if p.Source != "claude" {
			t.Errorf("empty source should default to provider, got %q", p.Source)
		}
	})
}

// TestReportRuntimeUsage_DedupExcludeSelfHeal drives the Phase A spine end to
// end through the protected daemon endpoint: ingest → dedup → rollup →
// task-session exclusion → in-flight self-heal. The exclusion is the load-
// bearing "don't double-count mounted runs" guarantee (坑#2).
func TestReportRuntimeUsage_DedupExcludeSelfHeal(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// A runtime owned by the test user — person attribution = runtime.owner_id.
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
		VALUES ($1, NULL, 'ambient test runtime', 'local', 'claude', 'online', '', '{}'::jsonb, $2, now())
		RETURNING id`, testWorkspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM ambient_usage_hourly_dirty WHERE runtime_id = $1`, runtimeID)
		testPool.Exec(ctx, `DELETE FROM ambient_usage_hourly WHERE runtime_id = $1`, runtimeID)
		testPool.Exec(ctx, `DELETE FROM ambient_usage WHERE runtime_id = $1`, runtimeID)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	// Attribution anchor present (criterion 9 happy path; the NULL-guard /
	// "unattributed" bucket is the read layer's job in Phase C).
	var ownerID string
	if err := testPool.QueryRow(ctx, `SELECT owner_id::text FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&ownerID); err != nil || ownerID != testUserID {
		t.Fatalf("runtime owner_id should be the daemon owner %s, got %q (err %v)", testUserID, ownerID, err)
	}

	const eventAt = "2026-04-01T08:30:00Z"
	dup := AmbientUsagePayload{
		SessionID: "S-local", MessageID: "m1", RequestID: "r1",
		Provider: "claude", Model: "claude-sonnet-4-6", EventAt: eventAt,
		InputTokens: 100, OutputTokens: 10, Source: "claude",
	}
	body := map[string]any{"usage": []AmbientUsagePayload{
		dup, dup, dup, // same (message.id, requestId) x3 → dedup to one row (坑#1)
		{SessionID: "S-task", MessageID: "m2", RequestID: "r2",
			Provider: "claude", Model: "claude-sonnet-4-6", EventAt: eventAt,
			InputTokens: 300, OutputTokens: 30, Source: "claude"},
	}}

	// DB clock before ingest — lower bound for the first rollup window.
	var tBefore time.Time
	testPool.QueryRow(ctx, `SELECT now()`).Scan(&tBefore)

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/usage", body, testWorkspaceID, "ambient-test-daemon")
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ReportRuntimeUsage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ReportRuntimeUsage: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Dedup: 3 identical tuples collapsed → 2 rows total (S-local + S-task).
	var rawRows int
	testPool.QueryRow(ctx, `SELECT count(*) FROM ambient_usage WHERE runtime_id = $1`, runtimeID).Scan(&rawRows)
	if rawRows != 2 {
		t.Fatalf("dedup failed: expected 2 raw rows, got %d", rawRows)
	}

	// First rollup (no task yet): both sessions count → input = 100 + 300.
	if _, err := testPool.Exec(ctx, `SELECT rollup_ambient_usage_hourly_window($1, now() + interval '1 hour')`, tBefore); err != nil {
		t.Fatalf("first rollup: %v", err)
	}
	if got := hourlyInput(t, ctx, runtimeID); got != 400 {
		t.Fatalf("after first rollup expected input=400, got %d", got)
	}

	// In-flight → completed: a dispatched task writes session_id = "S-task".
	// The atq trigger must re-enqueue that bucket so the next tick excludes it.
	var agentID string
	testPool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id)
		VALUES ($1, 'ambient test agent', '', 'local', '{}'::jsonb, $2, 'private', 1, $3) RETURNING id`,
		testWorkspaceID, runtimeID, testUserID).Scan(&agentID)
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID) })

	var tMid time.Time // DB clock after ingest, before the task — lower bound proving the dirty queue (not the watermark) drives the re-roll.
	testPool.QueryRow(ctx, `SELECT now()`).Scan(&tMid)

	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, session_id)
		VALUES ($1, $2, 'completed', 0, 'S-task')`, agentID, runtimeID); err != nil {
		t.Fatalf("create task with session: %v", err)
	}

	// Trigger fired: the bucket is dirty.
	var dirtyRows int
	testPool.QueryRow(ctx, `SELECT count(*) FROM ambient_usage_hourly_dirty WHERE runtime_id = $1`, runtimeID).Scan(&dirtyRows)
	if dirtyRows < 1 {
		t.Fatalf("atq session trigger did not enqueue a dirty bucket (self-heal would never fire)")
	}

	// Second rollup with p_from = tMid: the ambient rows (created before tMid)
	// are OUTSIDE the watermark window, so the ONLY way this bucket recomputes
	// is the dirty queue the trigger filled. It must drop S-task → input = 100.
	if _, err := testPool.Exec(ctx, `SELECT rollup_ambient_usage_hourly_window($1, now() + interval '1 hour')`, tMid); err != nil {
		t.Fatalf("second rollup: %v", err)
	}
	if got := hourlyInput(t, ctx, runtimeID); got != 100 {
		t.Fatalf("self-heal failed: after task session set, expected S-task excluded (input=100), got %d", got)
	}
}

func hourlyInput(t *testing.T, ctx context.Context, runtimeID string) int64 {
	t.Helper()
	var total int64
	if err := testPool.QueryRow(ctx,
		`SELECT COALESCE(SUM(input_tokens), 0)::bigint FROM ambient_usage_hourly WHERE runtime_id = $1`,
		runtimeID).Scan(&total); err != nil {
		t.Fatalf("read hourly input: %v", err)
	}
	return total
}
