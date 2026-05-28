package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestHasCapability_RoleMatrix(t *testing.T) {
	cases := []struct {
		role, cap string
		want      bool
	}{
		// Owner: full access.
		{"owner", middleware.CapIntegrationsRead, true},
		{"owner", middleware.CapIntegrationsWrite, true},
		{"owner", middleware.CapSecretsRead, true},
		{"owner", middleware.CapSecretsWrite, true},

		// Admin: same as owner for control plane.
		{"admin", middleware.CapIntegrationsRead, true},
		{"admin", middleware.CapIntegrationsWrite, true},
		{"admin", middleware.CapSecretsRead, true},
		{"admin", middleware.CapSecretsWrite, true},

		// Member: read integrations only.
		{"member", middleware.CapIntegrationsRead, true},
		{"member", middleware.CapIntegrationsWrite, false},
		{"member", middleware.CapSecretsRead, false},
		{"member", middleware.CapSecretsWrite, false},

		// Unknown role: nothing.
		{"guest", middleware.CapIntegrationsRead, false},
		{"", middleware.CapIntegrationsRead, false},
	}
	for _, c := range cases {
		if got := middleware.HasCapability(c.role, c.cap); got != c.want {
			t.Errorf("HasCapability(%q,%q) = %v, want %v", c.role, c.cap, got, c.want)
		}
	}
}

func TestRequireCapability_AllowsCapableMember(t *testing.T) {
	h := middleware.RequireCapability(middleware.CapIntegrationsWrite)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		}),
	)

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	ctx := middleware.SetMemberContext(req.Context(), "ws-1", db.Member{Role: "admin"})
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", w.Code, w.Body.String())
	}
}

func TestRequireCapability_RejectsMissingCapability(t *testing.T) {
	h := middleware.RequireCapability(middleware.CapSecretsRead)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("handler must not be called when capability is missing")
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	ctx := middleware.SetMemberContext(req.Context(), "ws-1", db.Member{Role: "member"})
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%q", w.Code, w.Body.String())
	}
}

func TestRequireCapability_RejectsMissingMemberInContext(t *testing.T) {
	h := middleware.RequireCapability(middleware.CapIntegrationsRead)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("handler must not be called without member in context")
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(context.Background())

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body=%q", w.Code, w.Body.String())
	}
}
