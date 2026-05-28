package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Admin-only endpoints used by self-host operators to onboard service users
// (e.g. autopilot-bot) and issue PATs without going through the regular
// signup → email-verification flow.
//
// Gated at the route layer by RequireWorkspaceRole("owner","admin"); the
// handlers themselves trust that the caller is already authorized.

// --- create user ---

// AdminCreateUserRequest is the body of POST /api/admin/users.
type AdminCreateUserRequest struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"` // owner | admin | member
}

// AdminCreateUserResponse returns the resolved user + member rows.
type AdminCreateUserResponse struct {
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	MemberID  string `json:"member_id"`
	CreatedAt string `json:"created_at"`
}

// AdminCreateUser: POST /api/admin/users
// Find-or-create the user by email, then add as member in the calling
// workspace at the requested role. Returns the resolved IDs. Idempotent on
// repeated calls (re-adding existing member is a no-op).
func (h *Handler) AdminCreateUser(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req AdminCreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	req.Name = strings.TrimSpace(req.Name)
	if req.Email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	if !strings.Contains(req.Email, "@") {
		writeError(w, http.StatusBadRequest, "email must contain '@'")
		return
	}
	if req.Role == "" {
		req.Role = "member"
	}
	if req.Role != "owner" && req.Role != "admin" && req.Role != "member" {
		writeError(w, http.StatusBadRequest, "role must be owner | admin | member")
		return
	}
	if req.Name == "" {
		req.Name = strings.SplitN(req.Email, "@", 2)[0]
	}

	// Find-or-create user by email.
	user, err := h.Queries.GetUserByEmail(r.Context(), req.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		user, err = h.Queries.CreateUser(r.Context(), db.CreateUserParams{
			Name:      req.Name,
			Email:     req.Email,
			AvatarUrl: pgtype.Text{},
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create user")
			return
		}
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to look up user")
		return
	}

	// Add as member (UNIQUE(workspace_id, user_id) — second call hits the
	// existing row; we surface the membership in either case).
	member, err := h.Queries.CreateMember(r.Context(), db.CreateMemberParams{
		WorkspaceID: wsUUID,
		UserID:      user.ID,
		Role:        req.Role,
	})
	if err != nil {
		if isUniqueViolation(err) {
			// Already a member — fetch and return current row.
			existing, lookupErr := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
				UserID:      user.ID,
				WorkspaceID: wsUUID,
			})
			if lookupErr != nil {
				writeError(w, http.StatusInternalServerError, "user already a member but lookup failed")
				return
			}
			member = existing
		} else {
			writeError(w, http.StatusInternalServerError, "failed to add member: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusCreated, AdminCreateUserResponse{
		UserID:    uuidToString(user.ID),
		Email:     user.Email,
		Name:      user.Name,
		Role:      member.Role,
		MemberID:  uuidToString(member.ID),
		CreatedAt: timestampToString(user.CreatedAt),
	})
}

// --- issue token for another user ---

// AdminIssueTokenRequest is the body of POST /api/admin/tokens.
type AdminIssueTokenRequest struct {
	UserEmail     string `json:"user_email"`
	Name          string `json:"name"`            // PAT label (required)
	ExpiresInDays *int   `json:"expires_in_days"` // optional; default no expiry
}

// AdminIssueToken: POST /api/admin/tokens
// Issues a PAT on behalf of the specified user. Target user must already
// be a member of the calling workspace (creates the binding the regular PAT
// gate expects). Returns the raw token exactly once.
func (h *Handler) AdminIssueToken(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req AdminIssueTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.UserEmail = strings.TrimSpace(req.UserEmail)
	req.Name = strings.TrimSpace(req.Name)
	if req.UserEmail == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "user_email and name are required")
		return
	}

	user, err := h.Queries.GetUserByEmail(r.Context(), req.UserEmail)
	if err != nil {
		writeError(w, http.StatusNotFound, "user not found — create via /api/admin/users first")
		return
	}
	// Confirm target user is a member of current workspace.
	if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      user.ID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusBadRequest, "target user is not a member of this workspace")
		return
	}

	rawToken, err := auth.GeneratePATToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	var expiresAt pgtype.Timestamptz
	if req.ExpiresInDays != nil && *req.ExpiresInDays > 0 {
		expiresAt = pgtype.Timestamptz{
			Time:  time.Now().Add(time.Duration(*req.ExpiresInDays) * 24 * time.Hour),
			Valid: true,
		}
	}
	prefix := rawToken
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}

	pat, err := h.Queries.CreatePersonalAccessToken(r.Context(), db.CreatePersonalAccessTokenParams{
		UserID:      user.ID,
		Name:        req.Name,
		TokenHash:   auth.HashToken(rawToken),
		TokenPrefix: prefix,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create token")
		return
	}

	writeJSON(w, http.StatusCreated, CreatePATResponse{
		PersonalAccessTokenResponse: patToResponse(pat),
		Token:                       rawToken,
	})
}
