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

	// Member-type assignee_id holds the USER id, not the member id (polymorphic
	// actor convention — see issue.sql). The card buckets by user.id, so the
	// viewer member (whose user_id is testUserID) must see this issue.
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET assignee_type='member', assignee_id=$1, status='blocked' WHERE id=$2`,
		testUserID, issue.ID); err != nil {
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

// TestTeamOverviewAgentDelegatedIssues asserts an issue assigned to a member's
// AGENT lands in that member's agent-task counts (delegated work) — keyed by the
// agent owner's user.id — which is what keeps the card non-empty for an AI-native
// team that assigns issues to agents rather than to members.
func TestTeamOverviewAgentDelegatedIssues(t *testing.T) {
	memberID := fixtureMemberID(t)
	ctx := context.Background()

	// Minimal runtime + agent owned by the test user.
	var runtimeID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider)
		 VALUES ($1, 'team-overview test runtime', 'local', 'claude') RETURNING id`,
		testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	var agentID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO agent (workspace_id, name, runtime_mode, runtime_id, owner_id)
		 VALUES ($1, 'team-overview test agent', 'local', $2, $3) RETURNING id`,
		testWorkspaceID, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID) })

	// An issue assigned to that agent (delegated work).
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "team-overview agent-delegated fixture", "status": "todo",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issue.ID) })

	if _, err := testPool.Exec(ctx,
		`UPDATE issue SET assignee_type='agent', assignee_id=$1, status='in_progress' WHERE id=$2`,
		agentID, issue.ID); err != nil {
		t.Fatalf("assign issue to agent: %v", err)
	}

	resp, err := testHandler.buildTeamOverview(ctx, parseUUID(testWorkspaceID), memberID)
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
	if self.AgentIssuesByStatus["in_progress"] < 1 {
		t.Fatalf("expected agent in_progress >= 1, got %d (map=%v)",
			self.AgentIssuesByStatus["in_progress"], self.AgentIssuesByStatus)
	}
	if self.AgentIssuesTotal < 1 {
		t.Fatalf("expected AgentIssuesTotal >= 1, got %d", self.AgentIssuesTotal)
	}

	// Archiving the agent excludes its delegated issues — consistent with the
	// agents-total tile, which also filters archived_at IS NULL.
	before := self.AgentIssuesByStatus["in_progress"]
	if _, err := testPool.Exec(ctx,
		`UPDATE agent SET archived_at = now() WHERE id = $1`, agentID); err != nil {
		t.Fatalf("archive agent: %v", err)
	}
	resp2, err := testHandler.buildTeamOverview(ctx, parseUUID(testWorkspaceID), memberID)
	if err != nil {
		t.Fatalf("buildTeamOverview after archive: %v", err)
	}
	for i := range resp2.Members {
		if resp2.Members[i].MemberID == memberID {
			if got := resp2.Members[i].AgentIssuesByStatus["in_progress"]; got >= before {
				t.Fatalf("archived agent's issue should be excluded: before=%d after=%d", before, got)
			}
		}
	}
}
