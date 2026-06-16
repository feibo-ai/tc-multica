package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ---------------------------------------------------------------------------
// TEA-113 fleet one-click update (nudge + force-override)
//
// This file owns the server side of the DRI "update every lagging CLI runtime"
// button: the authoritative latest-release endpoint (INV-11), the per-workspace
// rate limiter (INV-12), and the fleet self-check endpoint (INV-1/3/4/6). The
// terminal-write path (INV-13) lives in runtime_update.go ReportUpdateResult;
// the timeout sweep (INV-14) lives in cmd/server.
// ---------------------------------------------------------------------------

// fleetSelfCheckWindow is the per-workspace fixed window for the fleet
// self-check endpoint (INV-12): one call per workspace per 30s. Limit=1 so the
// second call inside the window is rejected with 429 before any UpdateStore
// .Create runs.
const fleetSelfCheckWindow = 30 * time.Second

// DefaultFleetSelfCheckRateLimit returns the INV-12 per-workspace budget:
// 1 request / 30s window. The key is the workspace ID (scope=workspaceID), NOT
// per-IP or per-user, so a DRI cannot widen the fleet blast radius by rotating
// source IPs.
func DefaultFleetSelfCheckRateLimit() WebhookRateLimit {
	return WebhookRateLimit{Limit: 1, Window: fleetSelfCheckWindow}
}

// fleetLatestReleaseTTL is the short cache window for the authoritative latest
// release lookup. Keeps an internal-network full Runtimes page open by many
// people from hammering the GitHub API while still reflecting a new release
// within a minute.
const fleetLatestReleaseTTL = 60 * time.Second

// FleetLatestReleaseResolver resolves the authoritative latest CLI release tag
// (feibo-ai/tc-multica, INV-11). Behind an interface so tests can inject a fake
// instead of hitting GitHub.
type FleetLatestReleaseResolver interface {
	// LatestTag returns the latest release tag (e.g. "v0.4.15") and its
	// release HTML URL. It must resolve feibo-ai/tc-multica and never the
	// upstream multica-ai/multica repo.
	LatestTag(ctx context.Context) (tag string, htmlURL string, err error)
}

// defaultFleetLatestReleaseResolver wraps cli.FetchLatestRelease with a short
// TTL cache. The cache is intentionally global-per-resolver (not per-workspace)
// because the latest release tag is a deployment-wide fact.
type defaultFleetLatestReleaseResolver struct {
	ttl   time.Duration
	fetch func() (*cli.GitHubRelease, error)

	mu        sync.Mutex
	cachedTag string
	cachedURL string
	cachedAt  time.Time
}

// NewDefaultFleetLatestReleaseResolver builds the production resolver: it calls
// cli.FetchLatestRelease (which targets feibo-ai/tc-multica) and caches the
// result for fleetLatestReleaseTTL.
func NewDefaultFleetLatestReleaseResolver() *defaultFleetLatestReleaseResolver {
	return &defaultFleetLatestReleaseResolver{
		ttl:   fleetLatestReleaseTTL,
		fetch: cli.FetchLatestRelease,
	}
}

func (r *defaultFleetLatestReleaseResolver) LatestTag(_ context.Context) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cachedTag != "" && time.Since(r.cachedAt) < r.ttl {
		return r.cachedTag, r.cachedURL, nil
	}

	rel, err := r.fetch()
	if err != nil {
		// Serve a slightly-stale cached value rather than failing the whole
		// fleet view if GitHub hiccups and we still have a prior good tag.
		if r.cachedTag != "" {
			return r.cachedTag, r.cachedURL, nil
		}
		return "", "", err
	}
	r.cachedTag = rel.TagName
	r.cachedURL = rel.HTMLURL
	r.cachedAt = time.Now()
	return r.cachedTag, r.cachedURL, nil
}

// GetFleetLatestRelease returns the authoritative latest CLI release tag for
// the frontend's "is this runtime lagging?" comparison (INV-11). It resolves
// feibo-ai/tc-multica server-side with a short TTL cache; the frontend must use
// this instead of the historical hard-coded upstream multica-ai/multica URL,
// which both pointed at the wrong repo and was unreachable from self-hosted
// internal networks.
func (h *Handler) GetFleetLatestRelease(w http.ResponseWriter, r *http.Request) {
	tag, htmlURL, err := h.FleetLatestRelease.LatestTag(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to resolve latest release")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"tag_name": tag,
		"html_url": htmlURL,
	})
}

// fleetSelfCheckRequest is the fleet self-check request body. It deliberately
// has NO target_version field (INV-1): the server fills the target from the
// authoritative latest release; a client cannot ask the fleet to install a
// chosen version. force is DRI-override intent and is recorded for audit only
// (INV-2) — it does not change which version is sent or which runtimes are
// nudged.
type fleetSelfCheckRequest struct {
	Force bool `json:"force"`
}

// FleetSelfCheckResult is the per-fleet-call response. It separates the
// outcomes so the UI can render an honest x/N (INV-6) and never silently drops
// a machine:
//   - Triggered: an UpdateStore.Create succeeded; carries the update id.
//   - Skipped:   Create returned errUpdateInProgress (still updating to a prior
//     trigger) — surfaced explicitly, never silently swallowed.
//   - Failed:    Create returned an infrastructure error (Redis / store fault),
//     NOT "already updating". A separate honest bucket carrying the reason so
//     the operator can see the failure and re-trigger; it must NOT masquerade
//     as Skipped ("已在更新中"), which would falsely imply the machine is
//     making progress (zero silent drops, INV-6).
//   - Unreachable: lagging runtimes excluded from the fleet (desktop-launched)
//     — listed separately and NOT counted in the x/N denominator.
type FleetSelfCheckResult struct {
	TargetVersion string                    `json:"target_version"`
	Force         bool                      `json:"force"`
	Triggered     []FleetTriggeredRuntime   `json:"triggered"`
	Skipped       []FleetSkippedRuntime     `json:"skipped"`
	Failed        []FleetFailedRuntime      `json:"failed"`
	Unreachable   []FleetUnreachableRuntime `json:"unreachable"`
}

type FleetTriggeredRuntime struct {
	RuntimeID string `json:"runtime_id"`
	UpdateID  string `json:"update_id"`
}

type FleetSkippedRuntime struct {
	RuntimeID string `json:"runtime_id"`
	Reason    string `json:"reason"`
}

// FleetFailedRuntime is a lagging runtime whose UpdateStore.Create failed with
// an infrastructure error (anything other than errUpdateInProgress). It is
// reported in its own bucket — never folded into Skipped — so a transient
// store/Redis fault surfaces honestly as an error the operator can re-trigger,
// rather than being disguised as "已在更新中" (INV-6: zero silent drops).
type FleetFailedRuntime struct {
	RuntimeID string `json:"runtime_id"`
	Reason    string `json:"reason"`
}

type FleetUnreachableRuntime struct {
	RuntimeID string `json:"runtime_id"`
	Reason    string `json:"reason"`
}

// FleetSelfCheck handles POST /api/workspaces/{id}/runtimes/fleet/self-check.
//
// Authorization is enforced upstream by RequireWorkspaceRoleFromURL(owner,admin)
// in the router (INV-3); this handler trusts that the workspace + member are
// already validated and injected into the context.
//
// Flow (INV-12 → INV-11 → INV-1 → INV-4):
//  1. Rate limit per workspace; on hit return 429 with ZERO Create / ZERO audit
//     write / ZERO nudge (all-or-nothing).
//  2. Resolve the authoritative latest tag (server-filled target; INV-1).
//  3. Enumerate the workspace's lagging LOCAL runtimes (cli_version < latest),
//     excluding desktop-launched ones (those become "unreachable", not in x/N).
//  4. For each: UpdateStore.Create(runtimeID, latestTag, force). On success
//     write the (A) audit trigger row (INV-4) best-effort (a missing audit row
//     degrades追责, not safety, so a failed audit write logs and continues —
//     the machine stays triggered, the batch is not aborted). errUpdateInProgress
//     → skipped; any other Create error → failed (infrastructure fault, its own
//     bucket, never disguised as skipped).
//
// force is recorded into the (A) audit row and echoed back, but never decides
// the target version, which runtimes are nudged, or any terminal-write branch
// (INV-2).
func (h *Handler) FleetSelfCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	wsID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace_id")
	if !ok {
		return
	}

	// INV-12: per-workspace fixed-window rate limit, checked BEFORE any
	// Create / audit write / nudge. On hit nothing is created — all-or-nothing.
	if !h.fleetRateLimitAllow(ctx, wsID) {
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, "fleet self-check rate limited; try again shortly")
		return
	}

	// Member is injected by RequireWorkspaceRoleFromURL — used for the
	// non-repudiable (A) audit user_id (the DRI who pressed the button).
	member, hasMember := middleware.MemberFromContext(ctx)
	if !hasMember {
		writeError(w, http.StatusForbidden, "workspace membership required")
		return
	}

	var body fleetSelfCheckRequest
	// An empty body is valid (force defaults false). Only reject a malformed
	// non-empty body. Crucially, the body has no target_version field at all
	// (INV-1), so even a client that sends one cannot influence the target.
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	// INV-1 / INV-11: target version is server-filled from the authoritative
	// latest release. The request body NEVER supplies it.
	latestTag, _, err := h.FleetLatestRelease.LatestTag(ctx)
	if err != nil || latestTag == "" {
		writeError(w, http.StatusBadGateway, "failed to resolve latest release")
		return
	}

	runtimes, err := h.Queries.ListAgentRuntimes(ctx, wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes")
		return
	}

	result := FleetSelfCheckResult{
		TargetVersion: latestTag,
		Force:         body.Force,
		Triggered:     []FleetTriggeredRuntime{},
		Skipped:       []FleetSkippedRuntime{},
		Failed:        []FleetFailedRuntime{},
		Unreachable:   []FleetUnreachableRuntime{},
	}

	for _, rt := range runtimes {
		// Only local CLI runtimes are in scope; cloud/remote runtimes self-
		// manage their binary and are not fleet-nudged.
		if rt.RuntimeMode != "local" {
			continue
		}
		cliVersion, launchedBy := fleetRuntimeMeta(rt.Metadata)

		// Not lagging → skip silently (no row in any bucket). A runtime with no
		// reported version is treated as not-lagging to avoid nudging machines
		// we cannot reason about.
		if cliVersion == "" || !cli.IsNewerVersion(latestTag, cliVersion) {
			continue
		}

		runtimeID := util.UUIDToString(rt.ID)

		// Desktop-launched runtimes are excluded from the fleet (INV-5 echoes
		// this on the daemon side). They are reported as "unreachable" and are
		// NOT counted in the x/N denominator.
		if launchedBy == "desktop" {
			result.Unreachable = append(result.Unreachable, FleetUnreachableRuntime{
				RuntimeID: runtimeID,
				Reason:    "desktop",
			})
			continue
		}

		// force is passed to Create purely so PopPending can echo it into the
		// heartbeat ack for daemon-side logging; the store never branches on it.
		update, createErr := h.UpdateStore.Create(ctx, runtimeID, latestTag, body.Force)
		if createErr != nil {
			if createErr == errUpdateInProgress {
				// errUpdateInProgress is the ONLY error that means "already
				// updating to a prior trigger" — surface it as skipped (INV-6:
				// explicit, never silently swallowed).
				result.Skipped = append(result.Skipped, FleetSkippedRuntime{
					RuntimeID: runtimeID,
					Reason:    "update_in_progress",
				})
				continue
			}
			// Any OTHER Create error is an infrastructure fault (Redis / store
			// failure), NOT "already updating". It must NOT be folded into
			// skipped — that would falsely tell the operator the machine is
			// making progress. Report it in its own failed bucket with the raw
			// reason so the operator can see it and re-trigger, and do not abort
			// the rest of the batch (INV-6: zero silent drops).
			slog.Error("fleet self-check: UpdateStore.Create failed",
				"runtime_id", runtimeID, "error", createErr)
			result.Failed = append(result.Failed, FleetFailedRuntime{
				RuntimeID: runtimeID,
				Reason:    createErr.Error(),
			})
			continue
		}

		// INV-4 (A): record the non-repudiable trigger fact immediately on
		// Create success. force lives in this row as audit data only (INV-2).
		if auditErr := h.Queries.InsertFleetUpdateTrigger(ctx, db.InsertFleetUpdateTriggerParams{
			UpdateID:      update.ID,
			WorkspaceID:   wsUUID,
			RuntimeID:     rt.ID,
			UserID:        member.UserID,
			TargetVersion: latestTag,
			Force:         body.Force,
		}); auditErr != nil {
			// Best-effort (ADR: "missing audit row degrades追责, not safety").
			// The nudge is already created (the daemon will act on it); we only
			// failed to persist the (A) audit row. Log and CONTINUE — keep the
			// runtime in the triggered bucket so the UI is honest about what was
			// sent, and do NOT abort the remaining runtimes in the batch. The
			// timeout sweep (INV-14) keys off the audit row, so a missing row
			// degrades that machine's追责/timeout-detection only, never the
			// nudge that was already issued.
			slog.Error("fleet self-check: failed to persist (A) audit trigger row",
				"runtime_id", runtimeID, "update_id", update.ID, "error", auditErr)
		}

		result.Triggered = append(result.Triggered, FleetTriggeredRuntime{
			RuntimeID: runtimeID,
			UpdateID:  update.ID,
		})
	}

	writeJSON(w, http.StatusOK, result)
}

// fleetRateLimitAllow applies the INV-12 per-workspace fixed-window limit using
// the shared-storage WebhookRateLimiter (Redis in multi-node). The key is the
// workspace ID only (scope=workspaceID). A nil limiter fails open (consistent
// with the rest of the rate-limit stack: the limit is defense-in-depth, not a
// correctness gate — the role gate + Create idempotency + updating CAS remain).
func (h *Handler) fleetRateLimitAllow(ctx context.Context, workspaceID string) bool {
	if h.FleetRateLimiter == nil {
		return true
	}
	return h.FleetRateLimiter.Allow(ctx, "fleet-self-check:"+workspaceID)
}

// ---------------------------------------------------------------------------
// Fleet update progress回显 (INV-6): read-only audit endpoint
//
// The persistent fleet_update_audit table is the AUTHORITATIVE source for the
// frontend's per-runtime progress view — never the ephemeral UpdateStore, whose
// rows evaporate on a 5min TTL. This endpoint exposes ListFleetUpdateAuditByWorkspace
// shaped into frontend-friendly per-runtime rows. It is read-only and shares the
// same owner/admin role gate as the self-check trigger (router.go): the progress
// view is DRI-only, so the gate stays consistent with who could press the button.
// ---------------------------------------------------------------------------

// fleetAuditDefaultWindow is the default lookback for the progress回显 when the
// caller passes no `since`. The audit table is bounded by triggered_at, so this
// keeps the panel aggregating only the latest fleet run(s) rather than the full
// history.
const fleetAuditDefaultWindow = 6 * time.Hour

// fleetAuditMaxWindow caps the caller-supplied `since` lookback so a client
// cannot turn the read into an unbounded full-table scan.
const fleetAuditMaxWindow = 7 * 24 * time.Hour

// fleetAuditMaxLimit caps the number of rows returned in one call. The audit
// query yields newest-first, so the cap keeps the most recent triggers.
const fleetAuditMaxLimit = 500

// FleetAuditRow is one per-runtime progress row for the frontend (INV-6). The
// (A) trigger-fact columns (update_id/runtime_id/target_version/force/triggered_at,
// plus user_id) are the server's non-repudiable record; the (B) result columns
// (report_status/report_source/reported_at) are nullable and stay null until a
// terminal result lands — either a daemon report (INV-13) or the server-timeout
// sweep (INV-14). report_source distinguishes 'daemon-reported' (NOT a "safely
// updated" assertion, INV-4) from 'server-timeout'.
type FleetAuditRow struct {
	UpdateID      string  `json:"update_id"`
	RuntimeID     string  `json:"runtime_id"`
	UserID        string  `json:"user_id"`
	TargetVersion string  `json:"target_version"`
	Force         bool    `json:"force"`
	TriggeredAt   string  `json:"triggered_at"`
	ReportStatus  *string `json:"report_status"`
	ReportSource  *string `json:"report_source"`
	ReportedAt    *string `json:"reported_at"`
}

// FleetAuditResult is the audit-endpoint response. Rows are ordered newest
// trigger first (the query's ORDER BY triggered_at DESC); the frontend groups by
// update_id to render each fleet run's per-runtime progress.
type FleetAuditResult struct {
	WindowSeconds int             `json:"window_seconds"`
	Rows          []FleetAuditRow `json:"rows"`
}

// GetFleetUpdateAudit handles GET /api/workspaces/{id}/runtimes/fleet/audit.
//
// Authorization is enforced upstream by RequireWorkspaceRoleFromURL(owner,admin)
// in the router (INV-3) — the same gate as the self-check trigger, since the
// progress view is DRI-only. This is a read-only endpoint: it never writes the
// audit table.
//
// Optional query params guard against a full-table read:
//   - since: lookback window in seconds (default 6h, capped at 7d). Maps to the
//     query's WindowSecs.
//   - limit: max rows returned (capped at 500). Applied in-handler after the
//     newest-first query, so it keeps the most recent triggers.
func (h *Handler) GetFleetUpdateAudit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	wsID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace_id")
	if !ok {
		return
	}

	q := r.URL.Query()

	// `since` is a lookback window in seconds. Default to fleetAuditDefaultWindow;
	// clamp to (0, fleetAuditMaxWindow] so a client cannot widen the scan.
	windowSecs := fleetAuditDefaultWindow.Seconds()
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid since — expected a positive number of seconds")
			return
		}
		windowSecs = float64(n)
		if max := fleetAuditMaxWindow.Seconds(); windowSecs > max {
			windowSecs = max
		}
	}

	// `limit` caps the returned rows. Default to the max; clamp to (0, max].
	limit := fleetAuditMaxLimit
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit — expected a positive integer")
			return
		}
		if n < limit {
			limit = n
		}
	}

	audits, err := h.Queries.ListFleetUpdateAuditByWorkspace(ctx, db.ListFleetUpdateAuditByWorkspaceParams{
		WorkspaceID: wsUUID,
		WindowSecs:  windowSecs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list fleet update audit")
		return
	}

	// The query returns newest-first; cap to `limit` to keep the most recent
	// triggers. A non-nil empty slice keeps the JSON `rows` array, never null.
	if len(audits) > limit {
		audits = audits[:limit]
	}

	rows := make([]FleetAuditRow, 0, len(audits))
	for _, a := range audits {
		rows = append(rows, FleetAuditRow{
			UpdateID:      a.UpdateID,
			RuntimeID:     uuidToString(a.RuntimeID),
			UserID:        uuidToString(a.UserID),
			TargetVersion: a.TargetVersion,
			Force:         a.Force,
			TriggeredAt:   timestampToString(a.TriggeredAt),
			ReportStatus:  textToPtr(a.ReportStatus),
			ReportSource:  textToPtr(a.ReportSource),
			ReportedAt:    timestampToPtr(a.ReportedAt),
		})
	}

	writeJSON(w, http.StatusOK, FleetAuditResult{
		WindowSeconds: int(windowSecs),
		Rows:          rows,
	})
}

// fleetRuntimeMeta extracts cli_version and launched_by from a runtime row's
// metadata JSON (the daemon writes both at registration; see DaemonRegister).
// Returns ("","") when metadata is absent or malformed.
func fleetRuntimeMeta(metadata []byte) (cliVersion, launchedBy string) {
	if len(metadata) == 0 {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal(metadata, &m); err != nil {
		return "", ""
	}
	if v, ok := m["cli_version"].(string); ok {
		cliVersion = v
	}
	if v, ok := m["launched_by"].(string); ok {
		launchedBy = v
	}
	return cliVersion, launchedBy
}
