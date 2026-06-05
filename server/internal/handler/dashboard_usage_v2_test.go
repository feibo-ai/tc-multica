package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Usage v2 (Phase 1) read endpoints:
//
//   GET /api/dashboard/usage/ambient/by-person   per-(owner, model) ambient rows
//   GET /api/dashboard/usage/ambient/daily       per-(date, model) for one owner
//   GET /api/dashboard/usage/by-agent/daily       per-(date, model) for one agent
//
// All three are pure reads over the existing hourly rollups. These tests seed
// the hourly tables directly (no rollup pass needed) and tag rows with unique
// model strings so they assert precisely against the shared fixture workspace.

type ambientByPersonRow struct {
	OwnerID          string `json:"owner_id"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

type usageDailyByModelRow struct {
	Date             string `json:"date"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

// seedAmbientHourly inserts one ambient_usage_hourly row in `bucketExpr`'s hour.
func seedAmbientHourly(t *testing.T, ctx context.Context, bucketExpr, ws, rt, model string, in, out, cr, cw int64) {
	t.Helper()
	if _, err := testPool.Exec(ctx, `
		INSERT INTO ambient_usage_hourly (
			bucket_hour, workspace_id, runtime_id, provider, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, event_count
		)
		VALUES (`+bucketExpr+`, $1, $2, 'claude', $3, $4, $5, $6, $7, 1)`,
		ws, rt, model, in, out, cr, cw); err != nil {
		t.Fatalf("seed ambient_usage_hourly (%s): %v", model, err)
	}
}

// mkAmbientRuntime creates an agent_runtime in `ws`, owned by `ownerID` when
// non-empty (else owner-less → "unattributed"), and registers cleanup.
func mkAmbientRuntime(t *testing.T, ctx context.Context, ws, name, ownerID string) string {
	t.Helper()
	var id string
	var err error
	if ownerID != "" {
		err = testPool.QueryRow(ctx, `
			INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
			VALUES ($1, $2, 'local', 'claude', 'online', '', '{}'::jsonb, $3, now()) RETURNING id`,
			ws, name, ownerID).Scan(&id)
	} else {
		err = testPool.QueryRow(ctx, `
			INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at)
			VALUES ($1, $2, 'local', 'claude', 'online', '', '{}'::jsonb, now()) RETURNING id`,
			ws, name).Scan(&id)
	}
	if err != nil {
		t.Fatalf("create runtime %s: %v", name, err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM ambient_usage_hourly WHERE runtime_id = $1`, id)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, id)
	})
	return id
}

// TestDashboardAmbientUsageByPerson covers the user-tab leaderboard feed:
// per-(owner, model) ambient totals with the model dimension PRESERVED (so the
// client can price per model), the unattributed "" bucket for an owner-less
// runtime, and workspace isolation (a row in another workspace must not leak).
func TestDashboardAmbientUsageByPerson(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	const personModelA = "v2-byperson-A"
	const personModelB = "v2-byperson-B"
	const orphanModel = "v2-byperson-orphan"
	const isoModel = "v2-byperson-iso-canary"

	ownedRT := mkAmbientRuntime(t, ctx, testWorkspaceID, "v2 byperson owned", testUserID)
	orphanRT := mkAmbientRuntime(t, ctx, testWorkspaceID, "v2 byperson orphan", "")

	// Isolation workspace B: a row here owned by the same user must NOT appear
	// in the primary workspace's response (proves WHERE workspace_id = $1).
	var wsB string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('V2 Iso ByPerson', 'v2-iso-byperson-' || gen_random_uuid()::text, '', 'V2I') RETURNING id`).Scan(&wsB); err != nil {
		t.Fatalf("create iso workspace: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, wsB) })
	isoRT := mkAmbientRuntime(t, ctx, wsB, "v2 iso owned", testUserID)

	bucket := `date_trunc('hour', now() - interval '1 day')`
	// Owned person, two models → must stay TWO rows (model dimension preserved).
	seedAmbientHourly(t, ctx, bucket, testWorkspaceID, ownedRT, personModelA, 1000, 100, 10, 1)
	seedAmbientHourly(t, ctx, bucket, testWorkspaceID, ownedRT, personModelB, 500, 50, 5, 0)
	// Owner-less runtime → unattributed bucket.
	seedAmbientHourly(t, ctx, bucket, testWorkspaceID, orphanRT, orphanModel, 70, 7, 0, 0)
	// Canary in workspace B — leaks only if the workspace filter is broken.
	seedAmbientHourly(t, ctx, bucket, wsB, isoRT, isoModel, 9999, 9999, 9999, 9999)

	w := httptest.NewRecorder()
	testHandler.GetDashboardAmbientUsageByPerson(w, newRequest("GET", "/api/dashboard/usage/ambient/by-person?days=7", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var rows []ambientByPersonRow
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var a, b, orphan *ambientByPersonRow
	for i := range rows {
		switch {
		case rows[i].OwnerID == testUserID && rows[i].Model == personModelA:
			a = &rows[i]
		case rows[i].OwnerID == testUserID && rows[i].Model == personModelB:
			b = &rows[i]
		case rows[i].OwnerID == "" && rows[i].Model == orphanModel:
			orphan = &rows[i]
		}
		if rows[i].Model == isoModel {
			t.Errorf("workspace isolation leaked: workspace-B canary appeared in primary ws response: %+v", rows[i])
		}
	}

	if a == nil || a.InputTokens != 1000 || a.OutputTokens != 100 || a.CacheReadTokens != 10 || a.CacheWriteTokens != 1 {
		t.Errorf("owned person model-A row wrong/missing: %+v", a)
	}
	// The model dimension MUST survive — a collapsed-model query would have
	// folded both models into one row and lost per-model cost.
	if b == nil || b.InputTokens != 500 || b.OutputTokens != 50 {
		t.Errorf("owned person model-B row wrong/missing (model dimension must be preserved): %+v", b)
	}
	if orphan == nil || orphan.InputTokens != 70 || orphan.OutputTokens != 7 {
		t.Errorf("unattributed (owner-less) row wrong/missing: %+v", orphan)
	}
}

// TestDashboardAmbientUsageDailyBucketsByViewerTimezone proves the user-tab
// heatmap feed cuts its calendar day under the caller's ?tz=, exactly like the
// existing daily token chart: a 04:00 UTC row lands on a different `date` for a
// UTC viewer vs an America/Los_Angeles viewer.
func TestDashboardAmbientUsageDailyBucketsByViewerTimezone(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	const model = "v2-ambient-daily-tz"
	rt := mkAmbientRuntime(t, ctx, testWorkspaceID, "v2 ambient daily tz", testUserID)

	// 04:00 UTC two days ago — still the prior evening in LA (UTC-7/-8).
	var bucketHour time.Time
	if err := testPool.QueryRow(ctx, `
		INSERT INTO ambient_usage_hourly (
			bucket_hour, workspace_id, runtime_id, provider, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, event_count
		)
		VALUES (
			((CURRENT_DATE - 2)::timestamp + interval '4 hours') AT TIME ZONE 'UTC',
			$1, $2, 'claude', $3, 999, 0, 0, 0, 1
		)
		RETURNING bucket_hour`, testWorkspaceID, rt, model).Scan(&bucketHour); err != nil {
		t.Fatalf("seed hourly row: %v", err)
	}

	utcDate := bucketHour.UTC().Format("2006-01-02")
	laLoc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load LA location: %v", err)
	}
	laDate := bucketHour.In(laLoc).Format("2006-01-02")
	if utcDate == laDate {
		t.Fatalf("test setup: UTC and LA dates must differ, both %s", utcDate)
	}

	readDate := func(tz string) string {
		w := httptest.NewRecorder()
		testHandler.GetDashboardAmbientUsageDaily(w, newRequest("GET",
			"/api/dashboard/usage/ambient/daily?owner_id="+testUserID+"&days=10&tz="+tz, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("tz=%s: expected 200, got %d: %s", tz, w.Code, w.Body.String())
		}
		var rows []usageDailyByModelRow
		_ = json.NewDecoder(w.Body).Decode(&rows)
		for _, r := range rows {
			if r.Model == model {
				return r.Date
			}
		}
		t.Fatalf("tz=%s: model %s row not found in %v", tz, model, rows)
		return ""
	}

	if got := readDate("UTC"); got != utcDate {
		t.Errorf("UTC viewer: expected date %s, got %s", utcDate, got)
	}
	if got := readDate("America/Los_Angeles"); got != laDate {
		t.Errorf("LA viewer: expected date %s, got %s", laDate, got)
	}
}

// TestDashboardAmbientUsageDailyUnattributed pins the owner_id="" / absent-key
// contract: BOTH route to the unattributed bucket (owner-less runtime), NOT a
// 400, and "" is NOT "all owners" — an owned runtime's usage must NOT appear
// under the unattributed request.
func TestDashboardAmbientUsageDailyUnattributed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	const orphanModel = "v2-ambient-daily-unattr-orphan"
	const ownedModel = "v2-ambient-daily-unattr-owned"

	orphanRT := mkAmbientRuntime(t, ctx, testWorkspaceID, "v2 unattr orphan", "")
	ownedRT := mkAmbientRuntime(t, ctx, testWorkspaceID, "v2 unattr owned", testUserID)

	bucket := `date_trunc('hour', now() - interval '1 day')`
	seedAmbientHourly(t, ctx, bucket, testWorkspaceID, orphanRT, orphanModel, 42, 4, 0, 0)
	seedAmbientHourly(t, ctx, bucket, testWorkspaceID, ownedRT, ownedModel, 808, 80, 0, 0)

	// hasModels returns which of (orphan, owned) appear in the response body.
	hasModels := func(path string) (orphan bool, owned bool, code int) {
		w := httptest.NewRecorder()
		testHandler.GetDashboardAmbientUsageDaily(w, newRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			return false, false, w.Code
		}
		var rows []usageDailyByModelRow
		_ = json.NewDecoder(w.Body).Decode(&rows)
		for _, r := range rows {
			switch r.Model {
			case orphanModel:
				orphan = true
			case ownedModel:
				owned = true
			}
		}
		return orphan, owned, w.Code
	}

	// owner_id="" (empty value) → unattributed bucket, not a 400.
	orphan, owned, code := hasModels("/api/dashboard/usage/ambient/daily?owner_id=&days=7&tz=UTC")
	if code != http.StatusOK {
		t.Fatalf("owner_id=\"\": expected 200, got %d", code)
	}
	if !orphan {
		t.Errorf("owner_id=\"\": unattributed (orphan) row should be present")
	}
	if owned {
		t.Errorf("owner_id=\"\" must NOT behave as \"all owners\": owned runtime's row leaked in")
	}

	// owner_id key entirely absent → same unattributed bucket.
	orphan, owned, code = hasModels("/api/dashboard/usage/ambient/daily?days=7&tz=UTC")
	if code != http.StatusOK {
		t.Fatalf("owner_id absent: expected 200, got %d", code)
	}
	if !orphan {
		t.Errorf("owner_id absent: unattributed (orphan) row should be present")
	}
	if owned {
		t.Errorf("owner_id absent must NOT behave as \"all owners\": owned runtime's row leaked in")
	}

	// A non-empty, malformed owner_id is still a 400 (the only error branch).
	w := httptest.NewRecorder()
	testHandler.GetDashboardAmbientUsageDaily(w, newRequest("GET",
		"/api/dashboard/usage/ambient/daily?owner_id=not-a-uuid&days=7&tz=UTC", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("malformed owner_id: expected 400, got %d", w.Code)
	}
}

// TestDashboardAgentUsageDaily covers the agent-tab heatmap feed: per-(date,
// model) task tokens for a single agent, tz day-slicing, and the required
// agent_id UUID boundary (malformed / missing → 400, #1661 convention).
func TestDashboardAgentUsageDaily(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	const model = "v2-agent-daily-tz"
	runtimeID := handlerTestRuntimeID(t)
	agentID := createHandlerTestAgent(t, "v2 agent daily", []byte("[]"))

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM task_usage_hourly WHERE model = $1`, model)
	})

	// 04:00 UTC two days ago — different calendar day in LA vs UTC.
	var bucketHour time.Time
	if err := testPool.QueryRow(ctx, `
		INSERT INTO task_usage_hourly (
			bucket_hour, workspace_id, runtime_id, agent_id, project_id,
			provider, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, task_count, event_count
		)
		VALUES (
			((CURRENT_DATE - 2)::timestamp + interval '4 hours') AT TIME ZONE 'UTC',
			$1, $2, $3, NULL, 'claude', $4,
			1234, 12, 0, 0, 1, 1
		)
		RETURNING bucket_hour`, testWorkspaceID, runtimeID, agentID, model).Scan(&bucketHour); err != nil {
		t.Fatalf("seed task_usage_hourly: %v", err)
	}

	utcDate := bucketHour.UTC().Format("2006-01-02")
	laLoc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load LA location: %v", err)
	}
	laDate := bucketHour.In(laLoc).Format("2006-01-02")
	if utcDate == laDate {
		t.Fatalf("test setup: UTC and LA dates must differ, both %s", utcDate)
	}

	readRow := func(tz string) (string, int64) {
		w := httptest.NewRecorder()
		testHandler.GetDashboardAgentUsageDaily(w, newRequest("GET",
			"/api/dashboard/usage/by-agent/daily?agent_id="+agentID+"&days=10&tz="+tz, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("tz=%s: expected 200, got %d: %s", tz, w.Code, w.Body.String())
		}
		var rows []usageDailyByModelRow
		_ = json.NewDecoder(w.Body).Decode(&rows)
		for _, r := range rows {
			if r.Model == model {
				return r.Date, r.InputTokens
			}
		}
		t.Fatalf("tz=%s: model %s row not found in %v", tz, model, rows)
		return "", 0
	}

	if date, in := readRow("UTC"); date != utcDate || in != 1234 {
		t.Errorf("UTC viewer: got date=%s in=%d, want date=%s in=1234", date, in, utcDate)
	}
	if date, _ := readRow("America/Los_Angeles"); date != laDate {
		t.Errorf("LA viewer: got date=%s, want %s", date, laDate)
	}

	// agent_id is a required UUID boundary: malformed → 400.
	{
		w := httptest.NewRecorder()
		testHandler.GetDashboardAgentUsageDaily(w, newRequest("GET",
			"/api/dashboard/usage/by-agent/daily?agent_id=not-a-uuid&days=10", nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("malformed agent_id: expected 400, got %d", w.Code)
		}
	}
	// Missing agent_id (empty/absent) → 400 (no "all agents" mode here).
	{
		w := httptest.NewRecorder()
		testHandler.GetDashboardAgentUsageDaily(w, newRequest("GET",
			"/api/dashboard/usage/by-agent/daily?days=10", nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("missing agent_id: expected 400, got %d", w.Code)
		}
	}
}
