package handler

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ---------------------------------------------------------------------------
// Workspace / Project dashboard
//
// Three read endpoints power the workspace dashboard:
//
//   GET /api/dashboard/usage/daily       per-(date, model) token rows
//   GET /api/dashboard/usage/by-agent    per-(agent, model) token rows
//   GET /api/dashboard/agent-runtime     per-agent run-time + task counts
//   GET /api/dashboard/runtime/daily     per-date run-time + task counts
//
// All three accept ?days=N (defaults to 30, capped at 365) and an optional
// ?project_id=<uuid> to scope the rollup to a single project. With no
// project_id the data spans the whole workspace.
//
// Cost is computed client-side from a per-model pricing table — the model
// dimension is intentionally preserved on the wire (same convention as the
// per-runtime usage endpoints).
//
// Access control: workspace membership only — we don't filter by per-agent
// visibility on the dashboard because token spend / run time are workspace-
// level operational metrics. Agent-detail pages still gate on per-agent
// access (see GetWorkspaceAgentRunCounts).
// ---------------------------------------------------------------------------

// parseProjectIDParam reads ?project_id=<uuid> off the URL. Returns a
// pgtype.UUID with Valid=false when the param is absent so sqlc's nullable
// argument resolves to SQL NULL and the WHERE clause degrades to "no
// project filter". On a malformed UUID it writes a 400 and returns
// ok=false; callers must return immediately.
func parseProjectIDParam(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	raw := r.URL.Query().Get("project_id")
	if raw == "" {
		return pgtype.UUID{}, true
	}
	u, err := util.ParseUUID(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project_id")
		return pgtype.UUID{}, false
	}
	return u, true
}

// DashboardUsageDailyResponse is one (date, model) bucket. Cost-side math
// happens on the client from a per-model pricing table; model stays on the
// wire for that reason.
type DashboardUsageDailyResponse struct {
	Date             string `json:"date"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	TaskCount        int32  `json:"task_count"`
}

// GetDashboardUsageDaily returns per-(date, model) token rows for the
// workspace, optionally scoped to a project. Backed by task_usage_hourly,
// sliced into calendar days under the viewer's tz.
func (h *Handler) GetDashboardUsageDaily(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	projectID, ok := parseProjectIDParam(w, r)
	if !ok {
		return
	}
	tz := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, tz)

	resp, err := h.listDashboardUsageDaily(r.Context(), parseUUID(workspaceID), tz, since, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list usage")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) listDashboardUsageDaily(
	ctx context.Context,
	workspaceID pgtype.UUID,
	tz string,
	since pgtype.Timestamptz,
	projectID pgtype.UUID,
) ([]DashboardUsageDailyResponse, error) {
	rows, err := h.Queries.ListDashboardUsageDaily(ctx, db.ListDashboardUsageDailyParams{
		WorkspaceID: workspaceID,
		Tz:          tz,
		Since:       since,
		ProjectID:   projectID,
	})
	if err != nil {
		return nil, err
	}
	resp := make([]DashboardUsageDailyResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardUsageDailyResponse{
			Date:             row.Date.Time.Format("2006-01-02"),
			Model:            row.Model,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
			TaskCount:        row.TaskCount,
		}
	}
	return resp, nil
}

// DashboardUsageByAgentResponse is one (agent, model) row.
type DashboardUsageByAgentResponse struct {
	AgentID          string `json:"agent_id"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	TaskCount        int32  `json:"task_count"`
}

// GetDashboardUsageByAgent returns per-(agent, model) token aggregates
// for the workspace, optionally scoped to a project. Backed by
// task_usage_hourly with the viewer's tz applied to the `?days=` cutoff.
func (h *Handler) GetDashboardUsageByAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	projectID, ok := parseProjectIDParam(w, r)
	if !ok {
		return
	}
	// "By agent" has no date grouping in the SQL — tz only determines
	// the cutoff boundary, not the bucket axis.
	tz := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, tz)

	resp, err := h.listDashboardUsageByAgent(r.Context(), parseUUID(workspaceID), since, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list usage by agent")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) listDashboardUsageByAgent(
	ctx context.Context,
	workspaceID pgtype.UUID,
	since pgtype.Timestamptz,
	projectID pgtype.UUID,
) ([]DashboardUsageByAgentResponse, error) {
	rows, err := h.Queries.ListDashboardUsageByAgent(ctx, db.ListDashboardUsageByAgentParams{
		WorkspaceID: workspaceID,
		Since:       since,
		ProjectID:   projectID,
	})
	if err != nil {
		return nil, err
	}
	resp := make([]DashboardUsageByAgentResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardUsageByAgentResponse{
			AgentID:          uuidToString(row.AgentID),
			Model:            row.Model,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
			TaskCount:        row.TaskCount,
		}
	}
	return resp, nil
}

// DashboardUsageByPersonResponse is one person's combined token total over the
// window: their agents' mounted-task usage PLUS their own ad-hoc local CLI
// sessions. AmbientTokens is the local-CLI portion of the total, so the UI can
// label a row "includes local CLI" without a second request. OwnerID is "" for
// the "unattributed" bucket (usage on a runtime with no resolved owner).
type DashboardUsageByPersonResponse struct {
	OwnerID          string `json:"owner_id"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	AmbientTokens    int64  `json:"ambient_tokens"`
}

// GetDashboardUsageByPerson returns per-person token totals for the workspace,
// combining mounted-task usage (task_usage_hourly) with ad-hoc local CLI usage
// (ambient_usage_hourly) under the runtime owner. This is the read-out that
// makes a teammate's previously-invisible local Claude Code usage show up.
//
// No project filter (ambient usage has no project). Like "by agent", there is
// no date bucket — tz only sets the `?days=` cutoff boundary.
func (h *Handler) GetDashboardUsageByPerson(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	tz := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, tz)

	resp, err := h.listDashboardUsageByPerson(r.Context(), parseUUID(workspaceID), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list usage by person")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) listDashboardUsageByPerson(
	ctx context.Context,
	workspaceID pgtype.UUID,
	since pgtype.Timestamptz,
) ([]DashboardUsageByPersonResponse, error) {
	rows, err := h.Queries.ListDashboardUsageByPerson(ctx, db.ListDashboardUsageByPersonParams{
		WorkspaceID: workspaceID,
		Since:       since,
	})
	if err != nil {
		return nil, err
	}
	resp := make([]DashboardUsageByPersonResponse, len(rows))
	for i, row := range rows {
		// NULL owner → "" so the client renders the "Unattributed" bucket
		// instead of silently folding it into a person (plan A4b).
		ownerID := ""
		if row.OwnerID.Valid {
			ownerID = uuidToString(row.OwnerID)
		}
		resp[i] = DashboardUsageByPersonResponse{
			OwnerID:          ownerID,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
			AmbientTokens:    row.AmbientTokens,
		}
	}
	return resp, nil
}

// DashboardAmbientUsageByPersonResponse is one (owner, model) ambient row — the
// "user tab" read-out. Unlike DashboardUsageByPersonResponse it covers ONLY
// local-CLI usage (ambient_usage_hourly), never executed-task usage (clean
// ambient / task split, plan D2), and it KEEPS the model dimension so the
// client can fold rows by owner and price each model. OwnerID is "" for the
// "unattributed" bucket (ambient usage on a runtime with no resolved owner).
type DashboardAmbientUsageByPersonResponse struct {
	OwnerID          string `json:"owner_id"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

// GetDashboardAmbientUsageByPerson returns per-(owner, model) ambient token
// aggregates for the workspace — the user-tab leaderboard feed. Model stays on
// the wire so the client folds rows by owner and computes per-model cost.
//
// No project filter (ambient usage has no project). No date bucket — tz only
// sets the ?days= cutoff boundary.
func (h *Handler) GetDashboardAmbientUsageByPerson(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	tz := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, tz)

	rows, err := h.Queries.ListDashboardAmbientUsageByPerson(r.Context(), db.ListDashboardAmbientUsageByPersonParams{
		WorkspaceID: parseUUID(workspaceID),
		Since:       since,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list ambient usage by person")
		return
	}

	resp := make([]DashboardAmbientUsageByPersonResponse, len(rows))
	for i, row := range rows {
		// NULL owner → "" so the client renders the "Unattributed" bucket
		// instead of folding it into a person (plan Q1; same as by-person).
		ownerID := ""
		if row.OwnerID.Valid {
			ownerID = uuidToString(row.OwnerID)
		}
		resp[i] = DashboardAmbientUsageByPersonResponse{
			OwnerID:          ownerID,
			Model:            row.Model,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// DashboardUsageDailyByModelResponse is one (date, model) bucket of token
// counts, shared by BOTH heatmap endpoints (ambient/daily and by-agent/daily)
// since their wire shape is identical. No task_count: the heatmap colours by
// tokens or by client-computed cost, neither of which needs the count.
type DashboardUsageDailyByModelResponse struct {
	Date             string `json:"date"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

// GetDashboardAmbientUsageDaily returns per-(date, model) ambient token rows
// for a SINGLE owner — or the unattributed bucket — sliced into calendar days
// under the viewer's tz. Powers the user-tab 26-week heatmap.
//
// owner_id handling is deliberately NOT the blanket parseUUIDOrBadRequest: this
// endpoint has no "all owners" mode. An empty OR absent owner_id means the
// unattributed bucket (rt.owner_id IS NULL), passed to the query as SQL NULL
// via pgtype.UUID{Valid:false}. We MUST NOT run "" through any UUID parse
// (util.ParseUUID("") errors), and an empty owner_id is NOT a 400. Note
// r.URL.Query().Get("owner_id") returns "" both for `?owner_id=` and for the
// key being absent entirely; both route to the unattributed bucket. Only a
// non-empty, malformed owner_id is a 400.
func (h *Handler) GetDashboardAmbientUsageDaily(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}

	ownerID := pgtype.UUID{} // Valid:false → SQL NULL → unattributed bucket
	if raw := r.URL.Query().Get("owner_id"); raw != "" {
		parsed, ok := parseUUIDOrBadRequest(w, raw, "owner_id")
		if !ok {
			return
		}
		ownerID = parsed
	}

	tz := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, tz)

	rows, err := h.Queries.ListDashboardAmbientUsageDaily(r.Context(), db.ListDashboardAmbientUsageDailyParams{
		WorkspaceID: parseUUID(workspaceID),
		Tz:          tz,
		Since:       since,
		OwnerID:     ownerID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list ambient usage daily")
		return
	}

	resp := make([]DashboardUsageDailyByModelResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardUsageDailyByModelResponse{
			Date:             row.Date.Time.Format("2006-01-02"),
			Model:            row.Model,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetDashboardAgentUsageDaily returns per-(date, model) task token rows for a
// SINGLE agent, sliced into calendar days under the viewer's tz. Powers the
// agent-tab 26-week heatmap. agent_id is a required UUID — malformed input is a
// 400 (#1661 boundary convention), though this is a pure read with no write.
func (h *Handler) GetDashboardAgentUsageDaily(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, r.URL.Query().Get("agent_id"), "agent_id")
	if !ok {
		return
	}
	tz := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, tz)

	rows, err := h.Queries.ListDashboardAgentUsageDaily(r.Context(), db.ListDashboardAgentUsageDailyParams{
		WorkspaceID: parseUUID(workspaceID),
		Tz:          tz,
		AgentID:     agentID,
		Since:       since,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent usage daily")
		return
	}

	resp := make([]DashboardUsageDailyByModelResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardUsageDailyByModelResponse{
			Date:             row.Date.Time.Format("2006-01-02"),
			Model:            row.Model,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// DashboardAgentRunTimeResponse is one agent's total terminal-task run time
// over the window. Includes failed tasks so the dashboard can surface how
// much execution time was spent on runs that didn't succeed.
type DashboardAgentRunTimeResponse struct {
	AgentID      string `json:"agent_id"`
	TotalSeconds int64  `json:"total_seconds"`
	TaskCount    int32  `json:"task_count"`
	FailedCount  int32  `json:"failed_count"`
}

// GetDashboardAgentRunTime returns per-agent total task run time (seconds)
// and task counts for the workspace, optionally scoped to a project. Only
// terminal tasks (completed or failed) with both started_at and
// completed_at populated contribute, since queued/running tasks have no
// finite duration.
func (h *Handler) GetDashboardAgentRunTime(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	projectID, ok := parseProjectIDParam(w, r)
	if !ok {
		return
	}
	// Cutoff in the viewer's tz so the "last N days" window matches the
	// per-agent cost card (GetDashboardUsageByAgent).
	tz := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, tz)

	rows, err := h.Queries.ListDashboardAgentRunTime(r.Context(), db.ListDashboardAgentRunTimeParams{
		WorkspaceID: parseUUID(workspaceID),
		Since:       since,
		ProjectID:   projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent runtime")
		return
	}

	resp := make([]DashboardAgentRunTimeResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardAgentRunTimeResponse{
			AgentID:      uuidToString(row.AgentID),
			TotalSeconds: row.TotalSeconds,
			TaskCount:    row.TaskCount,
			FailedCount:  row.FailedCount,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// DashboardRunTimeDailyResponse is one (date) bucket of terminal-task run
// time and counts. Powers the workspace dashboard's daily Time and Tasks
// charts — same toggle as Tokens / Cost, different metric.
type DashboardRunTimeDailyResponse struct {
	Date         string `json:"date"`
	TotalSeconds int64  `json:"total_seconds"`
	TaskCount    int32  `json:"task_count"`
	FailedCount  int32  `json:"failed_count"`
}

// GetDashboardRunTimeDaily returns per-date total task run time and task
// counts for the workspace, optionally scoped to a project. Only terminal
// tasks (completed or failed) with both started_at and completed_at
// populated contribute. Bucketed by completed_at so the day boundaries
// line up with the per-agent run-time card.
func (h *Handler) GetDashboardRunTimeDaily(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	projectID, ok := parseProjectIDParam(w, r)
	if !ok {
		return
	}
	// Slice day buckets in the viewer's tz so the Time / Tasks charts cut
	// their calendar day identically to the Cost / Tokens charts.
	tz := h.resolveViewingTZ(r)
	since := parseSinceParamInTZ(r, 30, tz)

	rows, err := h.Queries.ListDashboardRunTimeDaily(r.Context(), db.ListDashboardRunTimeDailyParams{
		WorkspaceID: parseUUID(workspaceID),
		Tz:          tz,
		Since:       since,
		ProjectID:   projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list daily runtime")
		return
	}

	resp := make([]DashboardRunTimeDailyResponse, len(rows))
	for i, row := range rows {
		resp[i] = DashboardRunTimeDailyResponse{
			Date:         row.Date.Time.Format("2006-01-02"),
			TotalSeconds: row.TotalSeconds,
			TaskCount:    row.TaskCount,
			FailedCount:  row.FailedCount,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
