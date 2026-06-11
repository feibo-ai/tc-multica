package handler

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TeamOverviewMember is one member card for the team overview page (TEA-104).
// It folds the per-member aggregates (issues by status, agents, autopilots,
// projects led/DRI, token usage, squad) into a single object the frontend
// renders as a dense member card. Counts come from fixed GROUP BY aggregates
// (never a per-member query loop), so the number of DB round-trips is
// independent of team size (criterion N2).
type TeamOverviewMember struct {
	MemberID  string `json:"member_id"`
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
	Role      string `json:"role"`
	IsSelf    bool   `json:"is_self"`
	SquadName string `json:"squad_name"`

	ProjectsLed int64 `json:"projects_led"`
	ProjectsDri int64 `json:"projects_dri"`

	// IssuesByStatus is keyed by the issue status enum
	// (backlog/todo/in_progress/in_review/done/blocked/cancelled). Absent keys
	// mean zero. The frontend renders the distribution bar from this.
	IssuesByStatus map[string]int64 `json:"issues_by_status"`
	IssuesTotal    int64            `json:"issues_total"`
	IssuesBlocked  int64            `json:"issues_blocked"`

	// AgentIssuesByStatus is the same status breakdown for issues assigned to
	// this person's AGENTS (delegated work). Keyed by the same status enum. The
	// card folds this into the task distribution so an AI-native team — which
	// assigns issues to agents, not members — still sees real activity.
	AgentIssuesByStatus map[string]int64 `json:"agent_issues_by_status"`
	AgentIssuesTotal    int64            `json:"agent_issues_total"`

	AgentsTotal   int64 `json:"agents_total"`
	AgentsRunning int64 `json:"agents_running"`
	Autopilots    int64 `json:"autopilots"`

	// Token usage. Two windows so the card can show 本周/本月 AI 用量 without a
	// second client request. Tokens are summed across the four kinds.
	TokensWeek  int64 `json:"tokens_week"`
	TokensMonth int64 `json:"tokens_month"`
}

// TeamOverviewResponse is the whole-workspace team overview. ViewerMemberID lets
// the frontend float + highlight the current user's own card; Members is already
// sorted blocked → running → total (boss attention, criterion N-D5).
type TeamOverviewResponse struct {
	ViewerMemberID string               `json:"viewer_member_id"`
	Members        []TeamOverviewMember `json:"members"`
}

// GetTeamOverview returns one card per workspace member for the team overview
// page. All members are visible to all members (per the explicit product
// decision; mirrors the existing /api/dashboard/usage/by-person visibility).
func (h *Handler) GetTeamOverview(w http.ResponseWriter, r *http.Request) {
	workspaceIDStr := h.resolveWorkspaceID(r)
	viewer, ok := h.workspaceMember(w, r, workspaceIDStr)
	if !ok {
		return
	}
	workspaceID := parseUUID(workspaceIDStr)

	resp, err := h.buildTeamOverview(r.Context(), workspaceID, uuidToString(viewer.ID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build team overview")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// buildTeamOverview runs the fixed set of workspace aggregates and folds them
// into per-member cards. Extracted from the HTTP handler so it is unit-testable
// against a test database.
func (h *Handler) buildTeamOverview(
	ctx context.Context,
	workspaceID pgtype.UUID,
	viewerMemberID string,
) (TeamOverviewResponse, error) {
	out := TeamOverviewResponse{ViewerMemberID: viewerMemberID, Members: []TeamOverviewMember{}}

	members, err := h.Queries.ListMembersWithUser(ctx, workspaceID)
	if err != nil {
		return out, err
	}

	// Aggregates keyed by member.id.
	issuesByMember := map[string]map[string]int64{}
	if rows, err := h.Queries.CountIssuesByMemberStatus(ctx, workspaceID); err != nil {
		return out, err
	} else {
		for _, row := range rows {
			id := uuidToString(row.AssigneeID)
			if issuesByMember[id] == nil {
				issuesByMember[id] = map[string]int64{}
			}
			issuesByMember[id][row.Status] = row.Count
		}
	}

	// Issues assigned to a member's agents (delegated work), keyed by user.id.
	agentIssuesByUser := map[string]map[string]int64{}
	if rows, err := h.Queries.CountAgentIssuesByOwnerStatus(ctx, workspaceID); err != nil {
		return out, err
	} else {
		for _, row := range rows {
			id := uuidToString(row.OwnerID)
			if agentIssuesByUser[id] == nil {
				agentIssuesByUser[id] = map[string]int64{}
			}
			agentIssuesByUser[id][row.Status] = row.Count
		}
	}

	autopilotsByMember := map[string]int64{}
	if rows, err := h.Queries.CountAutopilotsByCreatorMember(ctx, workspaceID); err != nil {
		return out, err
	} else {
		for _, row := range rows {
			autopilotsByMember[uuidToString(row.CreatedByID)] = row.Count
		}
	}

	projectsLedByMember := map[string]int64{}
	if rows, err := h.Queries.CountProjectsByLeadMember(ctx, workspaceID); err != nil {
		return out, err
	} else {
		for _, row := range rows {
			projectsLedByMember[uuidToString(row.LeadID)] = row.Count
		}
	}

	squadByMember := map[string]string{}
	if rows, err := h.Queries.ListSquadMembershipByMember(ctx, workspaceID); err != nil {
		return out, err
	} else {
		for _, row := range rows {
			id := uuidToString(row.MemberID)
			if squadByMember[id] == "" { // first squad wins for the chip
				squadByMember[id] = row.SquadName
			}
		}
	}

	// Aggregates keyed by user.id (agent owner, project DRI, token usage).
	agentsByUser := map[string]int64{}
	if rows, err := h.Queries.CountAgentsByOwner(ctx, workspaceID); err != nil {
		return out, err
	} else {
		for _, row := range rows {
			agentsByUser[uuidToString(row.OwnerID)] = row.Count
		}
	}

	runningAgentsByUser := map[string]int64{}
	if rows, err := h.Queries.CountRunningAgentsByOwner(ctx, workspaceID); err != nil {
		return out, err
	} else {
		for _, row := range rows {
			runningAgentsByUser[uuidToString(row.OwnerID)] = row.Count
		}
	}

	projectsDriByUser := map[string]int64{}
	if rows, err := h.Queries.CountProjectsByDriUser(ctx, workspaceID); err != nil {
		return out, err
	} else {
		for _, row := range rows {
			projectsDriByUser[uuidToString(row.DriUserID)] = row.Count
		}
	}

	tokensWeekByUser, err := h.sumTokensByUser(ctx, workspaceID, 7)
	if err != nil {
		return out, err
	}
	tokensMonthByUser, err := h.sumTokensByUser(ctx, workspaceID, 30)
	if err != nil {
		return out, err
	}

	for _, m := range members {
		memberID := uuidToString(m.ID)
		userID := uuidToString(m.UserID)

		byStatus := issuesByMember[memberID]
		if byStatus == nil {
			byStatus = map[string]int64{}
		}
		var total int64
		for _, c := range byStatus {
			total += c
		}

		agentByStatus := agentIssuesByUser[userID]
		if agentByStatus == nil {
			agentByStatus = map[string]int64{}
		}
		var agentTotal int64
		for _, c := range agentByStatus {
			agentTotal += c
		}

		out.Members = append(out.Members, TeamOverviewMember{
			MemberID:            memberID,
			UserID:              userID,
			Name:                m.UserName,
			Email:               m.UserEmail,
			AvatarURL:           m.UserAvatarUrl.String,
			Role:                m.Role,
			IsSelf:              memberID == viewerMemberID,
			SquadName:           squadByMember[memberID],
			ProjectsLed:         projectsLedByMember[memberID],
			ProjectsDri:         projectsDriByUser[userID],
			IssuesByStatus:      byStatus,
			IssuesTotal:         total,
			IssuesBlocked:       byStatus["blocked"],
			AgentIssuesByStatus: agentByStatus,
			AgentIssuesTotal:    agentTotal,
			AgentsTotal:         agentsByUser[userID],
			AgentsRunning:       runningAgentsByUser[userID],
			Autopilots:          autopilotsByMember[memberID],
			TokensWeek:          tokensWeekByUser[userID],
			TokensMonth:         tokensMonthByUser[userID],
		})
	}

	sortTeamMembers(out.Members)
	return out, nil
}

// sumTokensByUser reuses the per-person usage aggregate over a days-ago window
// and collapses the four token kinds into a single total keyed by user.id.
func (h *Handler) sumTokensByUser(ctx context.Context, workspaceID pgtype.UUID, days int) (map[string]int64, error) {
	since := pgtype.Timestamptz{Time: time.Now().AddDate(0, 0, -days), Valid: true}
	rows, err := h.Queries.ListDashboardUsageByPerson(ctx, db.ListDashboardUsageByPersonParams{
		WorkspaceID: workspaceID,
		Since:       since,
	})
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, row := range rows {
		if !row.OwnerID.Valid {
			continue // unattributed bucket has no member card
		}
		out[uuidToString(row.OwnerID)] = row.InputTokens + row.OutputTokens + row.CacheReadTokens + row.CacheWriteTokens
	}
	return out, nil
}

// sortTeamMembers orders cards by boss attention: self first, then most blocked,
// then most agents running, then most total issues (criterion N-D5/N-D6).
func sortTeamMembers(members []TeamOverviewMember) {
	sort.SliceStable(members, func(i, j int) bool {
		a, b := members[i], members[j]
		if a.IsSelf != b.IsSelf {
			return a.IsSelf
		}
		if a.IssuesBlocked != b.IssuesBlocked {
			return a.IssuesBlocked > b.IssuesBlocked
		}
		if a.AgentsRunning != b.AgentsRunning {
			return a.AgentsRunning > b.AgentsRunning
		}
		return a.IssuesTotal > b.IssuesTotal
	})
}
