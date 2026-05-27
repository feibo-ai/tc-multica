package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestListIssues_LabelsAnyByName(t *testing.T) {
	labelIDs := seedIssueLabelSet(t)
	defer cleanupIssueLabelSet(t, labelIDs)

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues?labels=plan-approved&workspace_id="+testWorkspaceID, nil)
	testHandler.ListIssues(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	ids := extractIssueIDs(w.Body.Bytes())
	if len(ids) != 2 {
		t.Fatalf("?labels=plan-approved: expected 2 issues, got %d (%v)", len(ids), ids)
	}
}

func TestListIssues_LabelsAllByName(t *testing.T) {
	labelIDs := seedIssueLabelSet(t)
	defer cleanupIssueLabelSet(t, labelIDs)

	w := httptest.NewRecorder()
	req := newRequest("GET",
		"/api/issues?labels=plan-approved,urgent&labels_mode=all&workspace_id="+testWorkspaceID, nil)
	testHandler.ListIssues(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	ids := extractIssueIDs(w.Body.Bytes())
	if len(ids) != 1 {
		t.Fatalf("?labels=plan-approved,urgent&labels_mode=all: expected 1 issue, got %d (%v)", len(ids), ids)
	}
}

func TestListIssues_LabelsAny_DefaultMode(t *testing.T) {
	labelIDs := seedIssueLabelSet(t)
	defer cleanupIssueLabelSet(t, labelIDs)

	w := httptest.NewRecorder()
	req := newRequest("GET",
		"/api/issues?labels=plan-approved,debrief&workspace_id="+testWorkspaceID, nil)
	testHandler.ListIssues(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	ids := extractIssueIDs(w.Body.Bytes())
	if len(ids) != 3 {
		t.Fatalf("default mode (any) with 2 labels covering 3 issues: expected 3, got %d (%v)", len(ids), ids)
	}
}

func TestListIssues_LabelsMode_Invalid(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("GET",
		"/api/issues?labels=x&labels_mode=junk&workspace_id="+testWorkspaceID, nil)
	testHandler.ListIssues(w, req)
	if w.Code != 400 {
		t.Fatalf("invalid labels_mode: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// Verifies the label-name filter composes with the status filter — i.e. the
// label dispatch path no longer silently drops other filter params. Before
// this fix, `?labels=plan-approved&status=in_progress` returned every issue
// with the label regardless of status.
func TestListIssues_LabelsComposesWithStatusFilter(t *testing.T) {
	labelIDs := seedIssueLabelSet(t)
	defer cleanupIssueLabelSet(t, labelIDs)

	// Move i2 to status=in_progress so we can verify the status filter narrows
	// the label-matched set.
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET status = 'in_progress' WHERE workspace_id = $1 AND title = 'i2-plan-approved-and-urgent'`,
		testWorkspaceID); err != nil {
		t.Fatalf("update issue status: %v", err)
	}

	// ?labels=plan-approved alone: 2 issues (i1 + i2).
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues?labels=plan-approved&workspace_id="+testWorkspaceID, nil)
	testHandler.ListIssues(w, req)
	if w.Code != 200 {
		t.Fatalf("labels alone: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := len(extractIssueIDs(w.Body.Bytes())); got != 2 {
		t.Fatalf("labels alone: expected 2 issues, got %d", got)
	}

	// ?labels=plan-approved&status=in_progress: only i2.
	w = httptest.NewRecorder()
	req = newRequest("GET",
		"/api/issues?labels=plan-approved&status=in_progress&workspace_id="+testWorkspaceID, nil)
	testHandler.ListIssues(w, req)
	if w.Code != 200 {
		t.Fatalf("labels+status: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	ids := extractIssueIDs(w.Body.Bytes())
	if len(ids) != 1 {
		t.Fatalf("labels+status: expected 1 issue (only i2 has both label and status), got %d (%v)", len(ids), ids)
	}

	// ?labels=plan-approved&status=backlog: only i1 (i2 was moved to in_progress).
	w = httptest.NewRecorder()
	req = newRequest("GET",
		"/api/issues?labels=plan-approved&status=backlog&workspace_id="+testWorkspaceID, nil)
	testHandler.ListIssues(w, req)
	if w.Code != 200 {
		t.Fatalf("labels+status=backlog: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	ids = extractIssueIDs(w.Body.Bytes())
	if len(ids) != 1 {
		t.Fatalf("labels+status=backlog: expected 1 issue (only i1), got %d (%v)", len(ids), ids)
	}

	// labels_mode=all also composes — ?labels=plan-approved,urgent&labels_mode=all
	// alone matches i2; adding &status=backlog should match nothing (i2 is now
	// in_progress), proving the AND-mode query also honors the status filter.
	w = httptest.NewRecorder()
	req = newRequest("GET",
		"/api/issues?labels=plan-approved,urgent&labels_mode=all&status=backlog&workspace_id="+testWorkspaceID, nil)
	testHandler.ListIssues(w, req)
	if w.Code != 200 {
		t.Fatalf("labels_mode=all+status: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	ids = extractIssueIDs(w.Body.Bytes())
	if len(ids) != 0 {
		t.Fatalf("labels_mode=all+status=backlog: expected 0 issues, got %d (%v)", len(ids), ids)
	}
}

// extractIssueIDs returns the "id" string for every row in a JSON array OR
// a {"issues":[...]} envelope, whichever ListIssues returns.
func extractIssueIDs(body []byte) []string {
	// Try envelope shape first
	var env struct {
		Issues []map[string]any `json:"issues"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Issues != nil {
		return collectIDs(env.Issues)
	}
	// Fall back to bare array
	var arr []map[string]any
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil
	}
	return collectIDs(arr)
}

func collectIDs(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if id, ok := r["id"].(string); ok {
			out = append(out, id)
		}
	}
	return out
}

// seedIssueLabelSet creates three labels (plan-approved, urgent, debrief)
// and three issues with the label mix:
//
//	i1: ["plan-approved"]
//	i2: ["plan-approved", "urgent"]
//	i3: ["debrief"]
//
// Returns the label UUIDs for cleanup. Issues are cleaned via t.Cleanup.
func seedIssueLabelSet(t *testing.T) map[string]string {
	t.Helper()
	ctx := context.Background()
	wsID := testWorkspaceID
	creatorID := testUserID

	labels := map[string]string{}
	for _, name := range []string{"plan-approved", "urgent", "debrief"} {
		var id string
		if err := testPool.QueryRow(ctx,
			`INSERT INTO issue_label (workspace_id, name, color) VALUES ($1, $2, $3) RETURNING id`,
			wsID, name, "#888888").Scan(&id); err != nil {
			t.Fatalf("insert label %s: %v", name, err)
		}
		labels[name] = id
	}

	insertIssue := func(title string, labelNames ...string) string {
		var issueID string
		// number column has NOT NULL constraint and a workspace-unique index;
		// grab the next available number per workspace.
		var nextNum int32
		_ = testPool.QueryRow(ctx,
			`SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1`, wsID).Scan(&nextNum)
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, number)
			VALUES ($1, $2, 'backlog', 'none', 'member', $3, $4)
			RETURNING id`,
			wsID, title, creatorID, nextNum).Scan(&issueID); err != nil {
			t.Fatalf("insert issue %s: %v", title, err)
		}
		for _, ln := range labelNames {
			if _, err := testPool.Exec(ctx,
				`INSERT INTO issue_to_label (issue_id, label_id) VALUES ($1, $2)`,
				issueID, labels[ln]); err != nil {
				t.Fatalf("attach label %s to %s: %v", ln, title, err)
			}
		}
		t.Cleanup(func() {
			testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
		})
		return issueID
	}

	insertIssue("i1-plan-approved-only", "plan-approved")
	insertIssue("i2-plan-approved-and-urgent", "plan-approved", "urgent")
	insertIssue("i3-debrief-only", "debrief")

	return labels
}

func cleanupIssueLabelSet(t *testing.T, labelIDs map[string]string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range labelIDs {
		testPool.Exec(ctx, `DELETE FROM issue_label WHERE id = $1`, id)
	}
}
