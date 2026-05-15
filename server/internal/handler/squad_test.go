package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// withSquadURLParams sets both the squad "id" and the "workspaceId" chi URL
// params on the request in a single route context — withURLParam allocates a
// fresh chi.RouteContext per call, so chaining two calls would drop the first.
func withSquadURLParams(req *http.Request, squadID string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", squadID)
	rctx.URLParams.Add("workspaceId", testWorkspaceID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// ── Test helpers ─────────────────────────────────────────────────────────────

// createSquadForTest creates a squad owned by the workspace's leader agent.
// Returns (squadID, leaderAgentID).
func createSquadForTest(t *testing.T, name string) (string, string) {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}

	leaderID := createHandlerTestAgent(t, name+"-leader", []byte(`{}`))

	w := httptest.NewRecorder()
	body := map[string]any{
		"name":      name,
		"leader_id": leaderID,
	}
	req := newRequest(http.MethodPost, "/api/squads", body)
	req = withURLParam(req, "workspaceId", testWorkspaceID)
	testHandler.CreateSquad(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateSquad: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp SquadResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode squad: %v", err)
	}

	t.Cleanup(func() {
		// Force-clean any lingering issue assignees + the squad row, regardless
		// of whether the test archived the squad.
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE assignee_id = $1 OR creator_id = $2`, resp.ID, testUserID)
		testPool.Exec(context.Background(), `DELETE FROM squad_member WHERE squad_id = $1`, resp.ID)
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, resp.ID)
	})

	return resp.ID, leaderID
}

// createIssueAssignedToSquad seeds an issue assigned to the given squad with
// the requested status. Returns the issue UUID string.
func createIssueAssignedToSquad(t *testing.T, squadID, status string) string {
	t.Helper()
	ctx := context.Background()

	// Find the next number for the workspace to avoid colliding with existing
	// test fixtures from other tests in the package.
	var number int64
	if err := testPool.QueryRow(ctx, `
		SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1
	`, testWorkspaceID).Scan(&number); err != nil {
		t.Fatalf("compute next issue number: %v", err)
	}

	var id string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, description, status, priority,
			assignee_type, assignee_id, creator_type, creator_id,
			number, position
		) VALUES ($1, $2, '', $3, 'medium', 'squad', $4, 'member', $5, $6, 0)
		RETURNING id
	`, testWorkspaceID, "squad-test-"+status, status, squadID, testUserID, number).Scan(&id); err != nil {
		t.Fatalf("insert squad-assigned issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id)
	})
	return id
}

func getSquadInTest(t *testing.T, squadID string) SquadResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodGet, "/api/squads/"+squadID, nil)
	req = withSquadURLParams(req, squadID)
	testHandler.GetSquad(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetSquad: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp SquadResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode squad: %v", err)
	}
	return resp
}

func deleteSquadInTest(t *testing.T, squadID string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodDelete, "/api/squads/"+squadID, nil)
	req = withSquadURLParams(req, squadID)
	testHandler.DeleteSquad(w, req)
	return w.Code, w.Body.String()
}

// readIssueAssignee reads the (assignee_type, assignee_id) of an issue
// directly from the DB. Used by the post-archive invariant assertions.
func readIssueAssignee(t *testing.T, issueID string) (string, string) {
	t.Helper()
	var assigneeType, assigneeID *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT assignee_type::text, assignee_id::text FROM issue WHERE id = $1`,
		issueID,
	).Scan(&assigneeType, &assigneeID); err != nil {
		t.Fatalf("read issue assignee: %v", err)
	}
	if assigneeType == nil || assigneeID == nil {
		return "", ""
	}
	return *assigneeType, *assigneeID
}

// readSquadArchivedAtValid reports whether the squad row's archived_at is set.
func readSquadArchivedAtValid(t *testing.T, squadID string) bool {
	t.Helper()
	var archivedAt *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT archived_at::text FROM squad WHERE id = $1`,
		squadID,
	).Scan(&archivedAt); err != nil {
		t.Fatalf("read squad archived_at: %v", err)
	}
	return archivedAt != nil
}

// ── Tests ────────────────────────────────────────────────────────────────────

// TestGetSquadIncludesIssueCount locks the v3 invariant that GetSquad reports
// the count of ALL assigned issues (every status), so the archive dialog
// shows a number that matches the eventual transfer.
func TestGetSquadIncludesIssueCount(t *testing.T) {
	squadID, _ := createSquadForTest(t, "issuecount")

	// Mix of statuses — count is ALL issues, regardless of status.
	createIssueAssignedToSquad(t, squadID, "todo")
	createIssueAssignedToSquad(t, squadID, "in_progress")
	createIssueAssignedToSquad(t, squadID, "done")
	createIssueAssignedToSquad(t, squadID, "cancelled")

	resp := getSquadInTest(t, squadID)
	if resp.IssueCount == nil {
		t.Fatalf("GetSquad must populate issue_count, got nil")
	}
	if *resp.IssueCount != 4 {
		t.Fatalf("issue_count: expected 4, got %d", *resp.IssueCount)
	}
}

// TestDeleteSquadTransfersAllIssuesIncludingTerminalToLeader locks the v3
// invariant that on archive every assigned issue (active OR terminal) is
// reassigned to the squad leader. Future "optimization" that reintroduces an
// active-only filter would resurrect the v2-era Unknown Squad / reopen-into-
// archived-squad bugs that the count-all + transfer-all design eliminated.
func TestDeleteSquadTransfersAllIssuesIncludingTerminalToLeader(t *testing.T) {
	squadID, leaderID := createSquadForTest(t, "transferall")

	active := createIssueAssignedToSquad(t, squadID, "in_progress")
	done := createIssueAssignedToSquad(t, squadID, "done")
	cancelled := createIssueAssignedToSquad(t, squadID, "cancelled")

	if code, body := deleteSquadInTest(t, squadID); code != http.StatusNoContent {
		t.Fatalf("DeleteSquad: expected 204, got %d: %s", code, body)
	}

	for _, c := range []struct {
		label string
		id    string
	}{
		{"active", active},
		{"done (terminal)", done},
		{"cancelled (terminal)", cancelled},
	} {
		t.Run(c.label, func(t *testing.T) {
			at, ai := readIssueAssignee(t, c.id)
			if at != "agent" {
				t.Fatalf("v3 invariant: %s issue must be reassigned away from squad, got assignee_type=%q", c.label, at)
			}
			if ai != leaderID {
				t.Fatalf("%s issue must be reassigned to squad leader (%s), got %s", c.label, leaderID, ai)
			}
		})
	}
}

// ── Transactional rollback tests ─────────────────────────────────────────────
//
// These two tests force one of the two writes (transfer or archive) to fail
// after the transaction is opened, then assert that nothing about either
// the squad or its issues has changed. They lock down the v3 invariant under
// failure: DeleteSquad must be all-or-nothing, never half-archived or
// silently-emptied.

// faultTxStarter wraps a real txStarter and replaces the returned tx with a
// fault-injecting wrapper. The wrapper fails any Exec/QueryRow whose SQL
// contains a configured substring, surfacing as a real error to the caller.
type faultTxStarter struct {
	inner   txStarter
	failSQL string // first call whose SQL contains this substring fails
}

func (f *faultTxStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := f.inner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &faultTx{Tx: tx, failSQL: f.failSQL}, nil
}

type faultTx struct {
	pgx.Tx
	failSQL string
	tripped bool
}

var errFaultInjected = errors.New("fault-injected SQL failure")

func (f *faultTx) shouldFail(sql string) bool {
	if f.tripped || f.failSQL == "" {
		return false
	}
	if strings.Contains(sql, f.failSQL) {
		f.tripped = true
		return true
	}
	return false
}

func (f *faultTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if f.shouldFail(sql) {
		return pgconn.CommandTag{}, errFaultInjected
	}
	return f.Tx.Exec(ctx, sql, args...)
}

func (f *faultTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if f.shouldFail(sql) {
		return nil, errFaultInjected
	}
	return f.Tx.Query(ctx, sql, args...)
}

func (f *faultTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if f.shouldFail(sql) {
		return faultRow{}
	}
	return f.Tx.QueryRow(ctx, sql, args...)
}

type faultRow struct{}

func (faultRow) Scan(dest ...any) error { return errFaultInjected }

// withFaultTx temporarily swaps testHandler.TxStarter so DeleteSquad's
// transaction sees forced failures on queries matching failSQL.
func withFaultTx(t *testing.T, failSQL string) {
	t.Helper()
	original := testHandler.TxStarter
	testHandler.TxStarter = &faultTxStarter{inner: original, failSQL: failSQL}
	t.Cleanup(func() { testHandler.TxStarter = original })
}

// TestDeleteSquadFailedTransferLeavesSquadActive: when TransferSquadAssignees
// errors, the squad must NOT be archived and the issue assignee must NOT have
// changed. This guards against the legacy slog.Warn-and-continue behavior.
func TestDeleteSquadFailedTransferLeavesSquadActive(t *testing.T) {
	squadID, _ := createSquadForTest(t, "failtransfer")
	issueID := createIssueAssignedToSquad(t, squadID, "in_progress")

	// Match the TransferSquadAssignees SQL by a stable substring of its body.
	withFaultTx(t, "UPDATE issue SET assignee_type = 'agent'")

	code, body := deleteSquadInTest(t, squadID)
	if code < 500 {
		t.Fatalf("transfer failure must surface as 5xx, got %d: %s", code, body)
	}

	if readSquadArchivedAtValid(t, squadID) {
		t.Fatalf("squad must remain active when transfer fails (archived_at was set)")
	}
	at, ai := readIssueAssignee(t, issueID)
	if at != "squad" || ai != squadID {
		t.Fatalf("transfer rollback: expected (squad, %s), got (%s, %s)", squadID, at, ai)
	}
}

// TestDeleteSquadFailedArchiveRollsBackTransfer: when ArchiveSquad errors
// after a successful transfer, the transfer must also roll back. Locks the
// reverse partial-write scenario.
func TestDeleteSquadFailedArchiveRollsBackTransfer(t *testing.T) {
	squadID, _ := createSquadForTest(t, "failarchive")
	issueID := createIssueAssignedToSquad(t, squadID, "in_progress")

	// Match the ArchiveSquad SQL by a stable substring of its body.
	withFaultTx(t, "UPDATE squad SET archived_at = now()")

	code, body := deleteSquadInTest(t, squadID)
	if code < 500 {
		t.Fatalf("archive failure must surface as 5xx, got %d: %s", code, body)
	}

	if readSquadArchivedAtValid(t, squadID) {
		t.Fatalf("squad must remain active when archive fails (archived_at was set)")
	}
	at, ai := readIssueAssignee(t, issueID)
	if at != "squad" || ai != squadID {
		t.Fatalf("transfer rollback: expected (squad, %s), got (%s, %s)", squadID, at, ai)
	}
}
