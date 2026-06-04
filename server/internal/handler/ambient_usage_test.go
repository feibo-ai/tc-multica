package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// TestAmbientUsagePayloadHasNoContentField is the type-level enforcement of the
// privacy doctrine (decisions/2026-06-03-local-log-privacy.md): the wire struct
// the daemon uploads carries numbers and ids ONLY. If a future change adds a
// field that could smuggle message content / prompt text, this fails closed.
func TestAmbientUsagePayloadHasNoContentField(t *testing.T) {
	// Content-bearing words that are safe to SUBSTRING-match: no numbers/ids
	// field on this struct contains them, so a future OutputText / MessageBody /
	// PromptDraft is caught even though its name only *contains* the word.
	substringBanned := []string{"content", "prompt", "text", "body"}
	// Words that appear as a benign prefix in legit fields (MessageID,
	// OutputTokens, InputTokens) — match these EXACTLY so the test doesn't
	// false-positive on an id/count column.
	exactBanned := []string{"message", "summary", "output", "input"}
	ty := reflect.TypeOf(AmbientUsagePayload{})
	for i := 0; i < ty.NumField(); i++ {
		name := strings.ToLower(ty.Field(i).Name)
		jsonTag := strings.ToLower(strings.SplitN(ty.Field(i).Tag.Get("json"), ",", 2)[0])
		flag := func(matched string) {
			t.Errorf("AmbientUsagePayload.%s looks like a content field (matched %q); "+
				"the collector must never carry message content — see decisions/2026-06-03-local-log-privacy.md",
				ty.Field(i).Name, matched)
		}
		for _, b := range substringBanned {
			if strings.Contains(name, b) || strings.Contains(jsonTag, b) {
				flag(b)
			}
		}
		for _, b := range exactBanned {
			if name == b || jsonTag == b {
				flag(b)
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

	// agent_task_queue.session_id is NOT unique — a retry/resume clones it onto
	// a sibling task. The exclusion anti-join must stay correct when the same
	// session matches N>1 atq rows (no fan-out double-exclude / negative sum).
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, session_id)
		VALUES ($1, $2, 'completed', 0, 'S-task')`, agentID, runtimeID); err != nil {
		t.Fatalf("create second task with same session: %v", err)
	}
	if _, err := testPool.Exec(ctx, `SELECT rollup_ambient_usage_hourly_window($1, now() + interval '1 hour')`, tMid); err != nil {
		t.Fatalf("third rollup: %v", err)
	}
	if got := hourlyInput(t, ctx, runtimeID); got != 100 {
		t.Fatalf("anti-join with 2 atq rows for one session miscounted: expected input=100, got %d", got)
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

// TestDashboardUsageByPerson_CombinesTaskAndAmbient is the C1 read-out: one
// person's number must fold their agents' mounted-task usage together with
// their own ad-hoc local CLI usage, and an owner-less runtime must surface as
// the "unattributed" bucket rather than vanishing or attaching to someone.
func TestDashboardUsageByPerson_CombinesTaskAndAmbient(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// Isolated workspace so the per-person aggregate sees only our rows.
	var wsID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('ByPerson Test', 'byperson-test-ws', '', 'BPT') RETURNING id`).Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM task_usage_hourly WHERE workspace_id = $1`, wsID)
		testPool.Exec(ctx, `DELETE FROM ambient_usage_hourly WHERE workspace_id = $1`, wsID)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1`, wsID)
		testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, wsID)
	})

	mkRuntime := func(name string, owned bool) string {
		var id string
		var err error
		if owned {
			err = testPool.QueryRow(ctx, `
				INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
				VALUES ($1, $2, 'local', 'claude', 'online', '', '{}'::jsonb, $3, now()) RETURNING id`, wsID, name, testUserID).Scan(&id)
		} else {
			err = testPool.QueryRow(ctx, `
				INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at)
				VALUES ($1, $2, 'local', 'claude', 'online', '', '{}'::jsonb, now()) RETURNING id`, wsID, name).Scan(&id)
		}
		if err != nil {
			t.Fatalf("create runtime %s: %v", name, err)
		}
		return id
	}
	owned := mkRuntime("owned runtime", true)
	orphan := mkRuntime("orphan runtime", false)

	bucket := `date_trunc('hour', now() - interval '1 day')`
	// Mounted-task usage for the owned runtime.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_usage_hourly (bucket_hour, workspace_id, runtime_id, agent_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, task_count, event_count)
		VALUES (`+bucket+`, $1, $2, gen_random_uuid(), 'claude', 'opus', 1000, 100, 0, 0, 1, 1)`, wsID, owned); err != nil {
		t.Fatalf("insert task_usage_hourly: %v", err)
	}
	// Ad-hoc local CLI usage for the owned runtime.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO ambient_usage_hourly (bucket_hour, workspace_id, runtime_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, event_count)
		VALUES (`+bucket+`, $1, $2, 'claude', 'opus', 200, 20, 0, 0, 1)`, wsID, owned); err != nil {
		t.Fatalf("insert ambient_usage_hourly (owned): %v", err)
	}
	// Ambient usage on the owner-less runtime → unattributed bucket.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO ambient_usage_hourly (bucket_hour, workspace_id, runtime_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, event_count)
		VALUES (`+bucket+`, $1, $2, 'claude', 'opus', 50, 5, 0, 0, 1)`, wsID, orphan); err != nil {
		t.Fatalf("insert ambient_usage_hourly (orphan): %v", err)
	}

	since := pgtype.Timestamptz{Time: time.Now().Add(-7 * 24 * time.Hour), Valid: true}
	rows, err := testHandler.listDashboardUsageByPerson(ctx, parseUUID(wsID), since)
	if err != nil {
		t.Fatalf("listDashboardUsageByPerson: %v", err)
	}
	byOwner := map[string]DashboardUsageByPersonResponse{}
	for _, r := range rows {
		byOwner[r.OwnerID] = r
	}

	person, ok := byOwner[testUserID]
	if !ok {
		t.Fatalf("no row for owner %s; got %+v", testUserID, rows)
	}
	if person.InputTokens != 1200 || person.OutputTokens != 120 {
		t.Errorf("combined totals wrong: in=%d out=%d (want 1200/120 = task 1000/100 + ambient 200/20)", person.InputTokens, person.OutputTokens)
	}
	if person.AmbientTokens != 220 {
		t.Errorf("ambient portion wrong: %d (want 220 = 200+20, the local-CLI part of the total)", person.AmbientTokens)
	}

	un, ok := byOwner[""]
	if !ok {
		t.Fatalf("no unattributed bucket for the owner-less runtime; got %+v", rows)
	}
	if un.InputTokens != 50 || un.AmbientTokens != 55 {
		t.Errorf("unattributed bucket wrong: in=%d ambient=%d (want 50/55)", un.InputTokens, un.AmbientTokens)
	}
}

// TestDashboardUsageByPerson_DeletedRuntimeUnattributed pins the LEFT JOIN: a
// runtime hard-deleted by the offline-runtime GC leaves its ambient_usage_hourly
// rows behind with no agent_runtime to resolve an owner. They must surface in
// the "unattributed" bucket, NOT vanish (an INNER JOIN would silently drop
// exactly the local-CLI usage this feature exists to surface).
func TestDashboardUsageByPerson_DeletedRuntimeUnattributed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var wsID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Deleted RT Test', 'deleted-rt-test-ws', '', 'DRT') RETURNING id`).Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM ambient_usage_hourly WHERE workspace_id = $1`, wsID)
		testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, wsID)
	})

	// Ambient rollup rows whose runtime_id has NO matching agent_runtime row
	// (the runtime was GC'd). gen_random_uuid() guarantees the absence.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO ambient_usage_hourly (bucket_hour, workspace_id, runtime_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, event_count)
		VALUES (date_trunc('hour', now() - interval '1 day'), $1, gen_random_uuid(), 'claude', 'opus', 999, 9, 0, 0, 1)`, wsID); err != nil {
		t.Fatalf("insert ambient_usage_hourly: %v", err)
	}

	since := pgtype.Timestamptz{Time: time.Now().Add(-7 * 24 * time.Hour), Valid: true}
	rows, err := testHandler.listDashboardUsageByPerson(ctx, parseUUID(wsID), since)
	if err != nil {
		t.Fatalf("listDashboardUsageByPerson: %v", err)
	}
	if len(rows) != 1 || rows[0].OwnerID != "" || rows[0].InputTokens != 999 {
		t.Fatalf("deleted runtime's ambient usage must fall to the unattributed bucket; got %+v", rows)
	}
}

// TestReportRuntimeUsage_RejectsCrossWorkspace verifies a daemon authenticated
// for one workspace cannot push usage onto a runtime in another (the workspace
// is server-resolved from the runtime, never the client).
func TestReportRuntimeUsage_RejectsCrossWorkspace(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at)
		VALUES ($1, NULL, 'xws runtime', 'local', 'claude', 'online', '', '{}'::jsonb, now()) RETURNING id`, testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	// Daemon authenticated for a DIFFERENT workspace than the runtime's.
	otherWS := "99999999-9999-4999-8999-999999999999"
	body := map[string]any{"usage": []AmbientUsagePayload{{
		SessionID: "s", MessageID: "m", RequestID: "r",
		Provider: "claude", Model: "opus", EventAt: "2026-04-01T08:30:00Z", InputTokens: 1, Source: "claude",
	}}}
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/usage", body, otherWS, "xws-daemon")
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ReportRuntimeUsage(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace push should be 404, got %d: %s", w.Code, w.Body.String())
	}
	var n int
	testPool.QueryRow(ctx, `SELECT count(*) FROM ambient_usage WHERE runtime_id = $1`, runtimeID).Scan(&n)
	if n != 0 {
		t.Fatalf("cross-workspace push leaked %d rows", n)
	}
}
