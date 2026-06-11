package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fixtureMemberID returns the member id of the test fixture user in the test
// workspace.
func fixtureMemberID(t *testing.T) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(),
		`SELECT id FROM member WHERE workspace_id = $1 AND user_id = $2`,
		testWorkspaceID, testUserID).Scan(&id); err != nil {
		t.Fatalf("fixtureMemberID: %v", err)
	}
	return id
}

// TestTeamOverviewSmoke runs the whole team-overview aggregation pipeline against
// the test DB and asserts it returns the fixture member with no SQL/scan error —
// this is the cheap regression guard for all seven aggregate queries at once.
func TestTeamOverviewSmoke(t *testing.T) {
	resp, err := testHandler.buildTeamOverview(context.Background(), parseUUID(testWorkspaceID), "")
	if err != nil {
		t.Fatalf("buildTeamOverview: %v", err)
	}
	if len(resp.Members) == 0 {
		t.Fatal("expected at least the fixture member, got 0")
	}
}

// TestTeamOverviewIssueCountsAndSort asserts a blocked issue assigned to a member
// shows up in that member's status distribution + blocked count, that the viewer
// flag is set, and that the viewer's own card sorts first (boss-attention sort).
func TestTeamOverviewIssueCountsAndSort(t *testing.T) {
	memberID := fixtureMemberID(t)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "team-overview blocked fixture", "status": "todo",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issue.ID)
	})

	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET assignee_type='member', assignee_id=$1, status='blocked' WHERE id=$2`,
		memberID, issue.ID); err != nil {
		t.Fatalf("assign issue: %v", err)
	}

	resp, err := testHandler.buildTeamOverview(context.Background(), parseUUID(testWorkspaceID), memberID)
	if err != nil {
		t.Fatalf("buildTeamOverview: %v", err)
	}

	var self *TeamOverviewMember
	for i := range resp.Members {
		if resp.Members[i].MemberID == memberID {
			self = &resp.Members[i]
		}
	}
	if self == nil {
		t.Fatal("viewer member not present in overview")
	}
	if self.IssuesBlocked < 1 {
		t.Fatalf("expected IssuesBlocked >= 1, got %d", self.IssuesBlocked)
	}
	if self.IssuesByStatus["blocked"] < 1 {
		t.Fatalf("expected blocked status count >= 1, got %d", self.IssuesByStatus["blocked"])
	}
	if !self.IsSelf {
		t.Fatal("expected IsSelf=true for the viewer member")
	}
	if resp.ViewerMemberID != memberID {
		t.Fatalf("expected ViewerMemberID=%s, got %s", memberID, resp.ViewerMemberID)
	}
	// Self + most-blocked sorts to the front (criterion N-D5/N-D6).
	if resp.Members[0].MemberID != memberID {
		t.Fatalf("expected viewer card first, got %s", resp.Members[0].MemberID)
	}
}
