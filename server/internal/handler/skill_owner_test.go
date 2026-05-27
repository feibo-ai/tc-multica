package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestCreateSkill_WithOwner(t *testing.T) {
	ownerID := insertHandlerTestUser(t, "owner@test.local")

	body := strings.NewReader(`{
		"name": "owner-test",
		"description": "d",
		"content": "c",
		"config": {},
		"owner_user_id": "` + ownerID + `"
	}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/skills?workspace_id="+testWorkspaceID, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	testHandler.CreateSkill(w, req)
	if w.Code != 201 {
		t.Fatalf("CreateSkill: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := resp["owner_user_id"]; got != ownerID {
		t.Fatalf("owner_user_id: got %v, want %s", got, ownerID)
	}
}

func TestTouchSkillReviewed(t *testing.T) {
	skillID := insertHandlerTestSkill(t, "touch-test", "")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/skills/"+skillID+"/touch-reviewed?workspace_id="+testWorkspaceID, nil)
	req = withURLParam(req, "id", skillID)
	testHandler.TouchSkillReviewed(w, req)
	if w.Code != 200 {
		t.Fatalf("TouchSkillReviewed: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ts, ok := resp["last_reviewed_at"].(string)
	if !ok || ts == "" {
		t.Fatalf("last_reviewed_at should be set after touch, got %v", resp["last_reviewed_at"])
	}
	parsed, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatalf("last_reviewed_at not parseable: %v", err)
	}
	if time.Since(parsed) > time.Minute {
		t.Fatalf("last_reviewed_at should be ~now, got %v", parsed)
	}
}

func TestListSkills_StaleFilter(t *testing.T) {
	freshID := insertHandlerTestSkill(t, "fresh-skill", "")
	staleID := insertHandlerTestSkill(t, "stale-skill", "")
	backdateSkillLastReviewed(t, staleID, time.Now().Add(-100*24*time.Hour))
	touchSkillNow(t, freshID)

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/skills?stale=true&workspace_id="+testWorkspaceID, nil)
	testHandler.ListSkills(w, req)
	if w.Code != 200 {
		t.Fatalf("ListSkills stale: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var foundStale, foundFresh bool
	for _, r := range rows {
		switch r["id"] {
		case staleID:
			foundStale = true
		case freshID:
			foundFresh = true
		}
		if _, hasContent := r["content"]; hasContent {
			t.Fatalf("stale list response must not include `content` field, got: %v", r)
		}
	}
	if !foundStale {
		t.Fatalf("expected stale skill %s in stale list", staleID)
	}
	if foundFresh {
		t.Fatalf("did not expect fresh skill %s in stale list", freshID)
	}
}

func backdateSkillLastReviewed(t *testing.T, skillID string, when time.Time) {
	t.Helper()
	_, err := testPool.Exec(context.Background(),
		`UPDATE skill SET last_reviewed_at = $2 WHERE id = $1`,
		skillID, when)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
}

func touchSkillNow(t *testing.T, skillID string) {
	t.Helper()
	_, err := testPool.Exec(context.Background(),
		`UPDATE skill SET last_reviewed_at = now() WHERE id = $1`, skillID)
	if err != nil {
		t.Fatalf("touch: %v", err)
	}
}

func TestUpdateSkill_SetAndClearOwner(t *testing.T) {
	skillID := insertHandlerTestSkill(t, "update-owner", "")
	ownerA := insertHandlerTestUser(t, "owner-a@test.local")

	// Set owner via UpdateSkill.
	set := strings.NewReader(`{"owner_user_id":"` + ownerA + `"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/skills/"+skillID+"?workspace_id="+testWorkspaceID, set)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	// chi.URLParam reads from RouteContext; populate it so the handler
	// can extract {id} without going through the router.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", skillID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.UpdateSkill(w, req)
	if w.Code != 200 {
		t.Fatalf("UpdateSkill set: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if got := resp["owner_user_id"]; got != ownerA {
		t.Fatalf("owner_user_id after set: got %v, want %s", got, ownerA)
	}

	// Clear owner via UpdateSkill with empty string.
	clr := strings.NewReader(`{"owner_user_id":""}`)
	w = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/api/skills/"+skillID+"?workspace_id="+testWorkspaceID, clr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", skillID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.UpdateSkill(w, req)
	if w.Code != 200 {
		t.Fatalf("UpdateSkill clear: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp = nil
	json.Unmarshal(w.Body.Bytes(), &resp)
	if got := resp["owner_user_id"]; got != nil {
		t.Fatalf("owner_user_id after clear: got %v, want nil", got)
	}
}

// insertHandlerTestUser writes a user row directly via SQL and registers a
// cleanup hook. Mirrors insertHandlerTestSkill / newWaitlistTestUser — used
// for owner-assignment tests that need a real foreign-key target.
func insertHandlerTestUser(t *testing.T, email string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(),
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"Owner Test "+t.Name(), email+"."+t.Name(),
	).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, id)
	})
	return id
}
