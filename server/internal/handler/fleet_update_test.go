package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fleetTestSeq guarantees per-row uniqueness for seeded workspaces / runtimes /
// audit rows regardless of clock resolution — UnixNano alone can collide when
// several rows are seeded inside one test on a fast machine.
var fleetTestSeq atomic.Int64

func fleetUniqueSuffix() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), fleetTestSeq.Add(1))
}

// ---------------------------------------------------------------------------
// TEA-113 fleet self-check handler tests (REV-5 mini-ADR INV-1/6/12).
//
// These exercise FleetSelfCheck through httptest with:
//   - a fake FleetLatestReleaseResolver (server-filled target; no GitHub)
//   - a fake WebhookRateLimiter (deterministic allow/deny, no Redis)
//   - a COUNTABLE fake UpdateStore so we can assert the exact number of
//     UpdateStore.Create calls (zero-Create-on-429, etc.) — the assertions are
//     real, not a mock rigged to pass.
//
// The audit (A) row write goes through the real *db.Queries against the test
// DB, so a dedicated per-test workspace is created and torn down to keep the
// Create-count and audit-row assertions exact and hermetic.
// ---------------------------------------------------------------------------

// fakeLatestResolver returns a fixed tag/url with no network access. It records
// whether LatestTag was called so INV-12 (zero work on 429) can assert it.
type fakeLatestResolver struct {
	tag string
	url string
	err error

	mu     sync.Mutex
	called int
}

func (f *fakeLatestResolver) LatestTag(context.Context) (string, string, error) {
	f.mu.Lock()
	f.called++
	f.mu.Unlock()
	return f.tag, f.url, f.err
}

func (f *fakeLatestResolver) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.called
}

// fixedRateLimiter is a deterministic limiter: it admits the first `allowN`
// calls for any key, then denies. This models INV-12 (limit=1/window) without a
// real clock or Redis.
type fixedRateLimiter struct {
	mu     sync.Mutex
	allowN int
	seen   map[string]int
}

func newFixedRateLimiter(allowN int) *fixedRateLimiter {
	return &fixedRateLimiter{allowN: allowN, seen: map[string]int{}}
}

func (l *fixedRateLimiter) Allow(_ context.Context, key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[key]++
	return l.seen[key] <= l.allowN
}

// countingUpdateStore wraps InMemoryUpdateStore and counts Create calls. The
// real lifecycle semantics (errUpdateInProgress, terminal transitions) are
// inherited from the embedded store; only the call count is added so tests can
// assert the EXACT number of Creates the handler issued.
type countingUpdateStore struct {
	*InMemoryUpdateStore

	mu          sync.Mutex
	createCalls int
	// createErr, when non-nil, is returned from Create for any runtime whose id
	// is in failFor. Used to simulate an infrastructure (non-in-progress) fault.
	createErr error
	failFor   map[string]bool
}

func newCountingUpdateStore() *countingUpdateStore {
	return &countingUpdateStore{
		InMemoryUpdateStore: NewInMemoryUpdateStore(),
		failFor:             map[string]bool{},
	}
}

func (s *countingUpdateStore) Create(ctx context.Context, runtimeID, targetVersion string, force bool) (*UpdateRequest, error) {
	s.mu.Lock()
	s.createCalls++
	failErr := s.createErr
	shouldFail := s.failFor[runtimeID]
	s.mu.Unlock()

	if shouldFail && failErr != nil {
		return nil, failErr
	}
	return s.InMemoryUpdateStore.Create(ctx, runtimeID, targetVersion, force)
}

func (s *countingUpdateStore) creates() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createCalls
}

// fleetTestWorkspace creates an isolated workspace + owner member and returns
// the workspace id, the owner db.Member (for SetMemberContext), and registers
// cleanup. Using a dedicated workspace keeps the enumerated runtime set under
// the test's exclusive control so Create-count assertions are exact.
func fleetTestWorkspace(t *testing.T) (string, db.Member) {
	t.Helper()
	ctx := context.Background()
	suffix := fleetUniqueSuffix()

	var wsID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', 'FLT')
		RETURNING id
	`, "Fleet Test "+suffix, "fleet-test-"+suffix).Scan(&wsID); err != nil {
		t.Fatalf("create fleet test workspace: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM fleet_update_audit WHERE workspace_id = $1`, wsID)
		testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
	})

	var memberID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
		RETURNING id
	`, wsID, testUserID).Scan(&memberID); err != nil {
		t.Fatalf("create fleet test member: %v", err)
	}

	member := db.Member{
		ID:          util.MustParseUUID(memberID),
		WorkspaceID: util.MustParseUUID(wsID),
		UserID:      util.MustParseUUID(testUserID),
		Role:        "owner",
	}
	return wsID, member
}

// seedRuntime inserts an agent_runtime in the given workspace with the supplied
// runtime_mode and metadata (cli_version / launched_by), returning its id. The
// daemon_id/provider are made unique per call so the
// (workspace_id, daemon_id, provider) unique constraint never collides.
func seedRuntime(t *testing.T, wsID, runtimeMode, cliVersion, launchedBy string) string {
	t.Helper()
	ctx := context.Background()

	meta := map[string]any{}
	if cliVersion != "" {
		meta["cli_version"] = cliVersion
	}
	if launchedBy != "" {
		meta["launched_by"] = launchedBy
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal runtime metadata: %v", err)
	}

	uniq := fleetUniqueSuffix()
	var rtID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status, metadata, last_seen_at
		)
		VALUES ($1, $2, $3, $4, $5, 'online', $6::jsonb, now())
		RETURNING id
	`, wsID, "daemon-"+uniq, "rt-"+uniq, runtimeMode, "prov-"+uniq, metaJSON).Scan(&rtID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	return rtID
}

// newFleetHandler builds a Handler that shares the real test DB Queries/pool
// but swaps in the supplied fake resolver, limiter, and update store so the
// fleet-specific collaborators are fully controlled.
func newFleetHandler(t *testing.T, resolver FleetLatestReleaseResolver, limiter WebhookRateLimiter, store UpdateStore) *Handler {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	h := *testHandler // shallow copy: reuse real Queries/DB/Bus, override fleet deps
	h.FleetLatestRelease = resolver
	h.FleetRateLimiter = limiter
	h.UpdateStore = store
	return &h
}

func postFleetSelfCheck(h *Handler, wsID string, member db.Member, body any) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/workspaces/"+wsID+"/runtimes/fleet/self-check", body)
	req = withURLParam(req, "id", wsID)
	req = req.WithContext(middleware.SetMemberContext(req.Context(), wsID, member))
	h.FleetSelfCheck(w, req)
	return w
}

func decodeFleetResult(t *testing.T, w *httptest.ResponseRecorder) FleetSelfCheckResult {
	t.Helper()
	var res FleetSelfCheckResult
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("decode fleet result: %v (body=%s)", err, w.Body.String())
	}
	return res
}

// TestFleetSelfCheck_RateLimitZeroCreate covers INV-12: the second click inside
// the per-workspace window returns 429 and the backend performs ZERO work — no
// UpdateStore.Create, no audit row, no nudge (all-or-nothing). The first click
// must Create exactly once per lagging runtime.
func TestFleetSelfCheck_RateLimitZeroCreate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	wsID, member := fleetTestWorkspace(t)

	// Two lagging local runtimes (cli_version < latest), no desktop.
	seedRuntime(t, wsID, "local", "v0.4.10", "")
	seedRuntime(t, wsID, "local", "v0.4.11", "")

	resolver := &fakeLatestResolver{tag: "v0.4.15", url: "https://example/v0.4.15"}
	limiter := newFixedRateLimiter(1) // admit the first call, deny the rest
	store := newCountingUpdateStore()
	h := newFleetHandler(t, resolver, limiter, store)

	// First click: admitted, 2 lagging runtimes → 2 Creates, 2 audit rows.
	w1 := postFleetSelfCheck(h, wsID, member, map[string]any{})
	if w1.Code != http.StatusOK {
		t.Fatalf("first click: expected 200, got %d: %s", w1.Code, w1.Body.String())
	}
	res1 := decodeFleetResult(t, w1)
	if len(res1.Triggered) != 2 {
		t.Fatalf("first click: expected 2 triggered, got %d (%+v)", len(res1.Triggered), res1)
	}
	if got := store.creates(); got != 2 {
		t.Fatalf("first click: expected 2 Create calls, got %d", got)
	}
	if n := countFleetAuditRows(t, wsID); n != 2 {
		t.Fatalf("first click: expected 2 audit rows, got %d", n)
	}

	// Second click inside the window: 429, and the backend did ZERO additional
	// work — Create count unchanged, no extra audit rows (all-or-nothing).
	w2 := postFleetSelfCheck(h, wsID, member, map[string]any{})
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second click: expected 429, got %d: %s", w2.Code, w2.Body.String())
	}
	if got := store.creates(); got != 2 {
		t.Fatalf("second click: Create count must stay 2 (zero new Creates on 429), got %d", got)
	}
	if n := countFleetAuditRows(t, wsID); n != 2 {
		t.Fatalf("second click: audit rows must stay 2 (zero new audit on 429), got %d", n)
	}
	// The 429 short-circuits before the latest-release resolve as well — the
	// rate limit is checked first, so resolver was only consulted on the first
	// admitted click.
	if resolver.calls() != 1 {
		t.Fatalf("expected exactly 1 latest-release resolve (only the admitted click), got %d", resolver.calls())
	}
}

// TestFleetSelfCheck_Buckets covers INV-1 (target server-filled), the desktop
// exclusion, the non-lagging skip, errUpdateInProgress → skipped, and the new
// (A) infrastructure-error → failed bucket (NOT skipped).
func TestFleetSelfCheck_Buckets(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	wsID, member := fleetTestWorkspace(t)

	rtTrigger := seedRuntime(t, wsID, "local", "v0.4.10", "")  // lagging → triggered
	rtInProgress := seedRuntime(t, wsID, "local", "v0.4.10", "") // lagging → in-progress → skipped
	rtFail := seedRuntime(t, wsID, "local", "v0.4.10", "")       // lagging → infra error → failed
	rtDesktop := seedRuntime(t, wsID, "local", "v0.4.10", "desktop")
	_ = seedRuntime(t, wsID, "local", "v0.4.15", "")  // not lagging (== latest) → skipped silently
	_ = seedRuntime(t, wsID, "local", "", "")         // no reported version → skipped silently
	_ = seedRuntime(t, wsID, "cloud", "v0.4.10", "")  // cloud mode → out of scope, skipped silently

	resolver := &fakeLatestResolver{tag: "v0.4.15", url: "https://example/v0.4.15"}
	limiter := newFixedRateLimiter(1)
	store := newCountingUpdateStore()

	// Pre-seed an in-progress update for rtInProgress so its Create returns
	// errUpdateInProgress; and mark rtFail to fail with an infra error.
	if _, err := store.InMemoryUpdateStore.Create(context.Background(), rtInProgress, "v0.4.15", false); err != nil {
		t.Fatalf("pre-seed in-progress: %v", err)
	}
	store.createErr = &updateError{msg: "redis unavailable"}
	store.failFor[rtFail] = true

	h := newFleetHandler(t, resolver, limiter, store)

	w := postFleetSelfCheck(h, wsID, member, map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	res := decodeFleetResult(t, w)

	// INV-1: target version is server-filled from the fake latest, not the body.
	if res.TargetVersion != "v0.4.15" {
		t.Fatalf("target_version = %q, want server-filled v0.4.15", res.TargetVersion)
	}

	// Triggered: exactly rtTrigger.
	if len(res.Triggered) != 1 || res.Triggered[0].RuntimeID != rtTrigger {
		t.Fatalf("triggered = %+v, want exactly [%s]", res.Triggered, rtTrigger)
	}
	// Skipped: exactly rtInProgress with reason update_in_progress.
	if len(res.Skipped) != 1 || res.Skipped[0].RuntimeID != rtInProgress {
		t.Fatalf("skipped = %+v, want exactly [%s]", res.Skipped, rtInProgress)
	}
	if res.Skipped[0].Reason != "update_in_progress" {
		t.Fatalf("skipped reason = %q, want update_in_progress", res.Skipped[0].Reason)
	}
	// Failed (Part A): exactly rtFail; the infra error must land in failed, NOT
	// skipped — i.e. it must not be disguised as "已在更新中".
	if len(res.Failed) != 1 || res.Failed[0].RuntimeID != rtFail {
		t.Fatalf("failed = %+v, want exactly [%s]", res.Failed, rtFail)
	}
	if res.Failed[0].Reason == "update_in_progress" {
		t.Fatalf("failed reason must NOT be the in-progress reason (must not masquerade as skipped)")
	}
	for _, s := range res.Skipped {
		if s.RuntimeID == rtFail {
			t.Fatalf("rtFail (infra error) leaked into skipped bucket — must be in failed (INV-6)")
		}
	}
	// Desktop excluded into unreachable, NOT triggered/skipped/failed.
	if len(res.Unreachable) != 1 || res.Unreachable[0].RuntimeID != rtDesktop {
		t.Fatalf("unreachable = %+v, want exactly [%s]", res.Unreachable, rtDesktop)
	}
	if res.Unreachable[0].Reason != "desktop" {
		t.Fatalf("unreachable reason = %q, want desktop", res.Unreachable[0].Reason)
	}

	// Create was attempted for the 3 lagging non-desktop local runtimes only
	// (rtTrigger + rtInProgress + rtFail). The non-lagging / no-version / cloud
	// / desktop runtimes never reach Create.
	if got := store.creates(); got != 3 {
		t.Fatalf("expected 3 Create calls (lagging non-desktop local only), got %d", got)
	}

	// Only the truly-triggered runtime got an (A) audit row.
	if n := countFleetAuditRows(t, wsID); n != 1 {
		t.Fatalf("expected exactly 1 audit row (only the triggered runtime), got %d", n)
	}
}

// TestFleetSelfCheck_IgnoresBodyTargetVersion covers INV-1: a body that carries
// a target_version is ignored entirely; TargetVersion is server-filled from the
// authoritative latest. The request body decode only knows about `force`.
func TestFleetSelfCheck_IgnoresBodyTargetVersion(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	wsID, member := fleetTestWorkspace(t)
	rt := seedRuntime(t, wsID, "local", "v0.4.10", "")

	resolver := &fakeLatestResolver{tag: "v0.4.15", url: "https://example/v0.4.15"}
	limiter := newFixedRateLimiter(1)
	store := newCountingUpdateStore()
	h := newFleetHandler(t, resolver, limiter, store)

	// Body tries to pin an attacker-chosen version + force. target_version must
	// be ignored; force is accepted as audit-only.
	w := postFleetSelfCheck(h, wsID, member, map[string]any{
		"target_version": "v9.9.9",
		"force":          true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	res := decodeFleetResult(t, w)
	if res.TargetVersion != "v0.4.15" {
		t.Fatalf("target_version = %q — body target_version must be ignored, server-filled v0.4.15 expected", res.TargetVersion)
	}
	if !res.Force {
		t.Fatalf("force should be echoed true (audit-only) when body sets it")
	}
	if len(res.Triggered) != 1 || res.Triggered[0].RuntimeID != rt {
		t.Fatalf("triggered = %+v, want exactly [%s]", res.Triggered, rt)
	}

	// The persisted (A) audit row's target_version is the server-filled value,
	// never the body's — the audit of record must not carry the client version.
	var auditTarget string
	var auditForce bool
	if err := testPool.QueryRow(context.Background(),
		`SELECT target_version, force FROM fleet_update_audit WHERE workspace_id = $1 AND runtime_id = $2`,
		wsID, rt,
	).Scan(&auditTarget, &auditForce); err != nil {
		t.Fatalf("load audit row: %v", err)
	}
	if auditTarget != "v0.4.15" {
		t.Fatalf("audit target_version = %q, want server-filled v0.4.15 (body version must never be persisted)", auditTarget)
	}
	if !auditForce {
		t.Fatalf("audit force = false, want true (force is recorded as audit data)")
	}
}

// countFleetAuditRows returns the number of fleet_update_audit rows for a
// workspace. Used to assert zero-write on 429 and the exact trigger-row count.
func countFleetAuditRows(t *testing.T, wsID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM fleet_update_audit WHERE workspace_id = $1`, wsID,
	).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}
