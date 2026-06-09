package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestUserRows(t *testing.T) {
	members := []map[string]any{
		{"user_id": "u-1", "name": "曾振华", "email": "z@team.local", "role": "owner"},
		{"user_id": "u-2", "name": "feibo", "email": "f@team.local", "role": "member"},
	}
	rows := userRows(members)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0][0] != "u-1" || rows[0][1] != "曾振华" || rows[0][2] != "z@team.local" || rows[0][3] != "owner" {
		t.Errorf("row0 = %v", rows[0])
	}
	// missing fields render empty (no panic) — owner resolution needs robust partials
	got := userRows([]map[string]any{{"user_id": "u-3"}})
	if got[0][0] != "u-3" || got[0][1] != "" || got[0][3] != "" {
		t.Errorf("partial member row = %v, want [u-3,'','','']", got[0])
	}
}

func TestListWorkspaceUsers_TableAndJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspaces/ws-1/members" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"user_id": "u-1", "name": "曾振华", "email": "z@team.local", "role": "owner"},
		})
	}))
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "ws-1", "tok")

	var table bytes.Buffer
	if err := listWorkspaceUsers(context.Background(), client, "ws-1", "table", &table); err != nil {
		t.Fatalf("table: %v", err)
	}
	for _, want := range []string{"USER ID", "u-1", "曾振华", "z@team.local", "owner"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table missing %q:\n%s", want, table.String())
		}
	}

	var jsonOut bytes.Buffer
	if err := listWorkspaceUsers(context.Background(), client, "ws-1", "json", &jsonOut); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !strings.Contains(jsonOut.String(), `"user_id"`) || !strings.Contains(jsonOut.String(), "u-1") {
		t.Errorf("json output = %s", jsonOut.String())
	}
}

func TestListWorkspaceUsers_ErrorWrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "ws-1", "tok")

	var buf bytes.Buffer
	err := listWorkspaceUsers(context.Background(), client, "ws-1", "table", &buf)
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if !strings.Contains(err.Error(), "list users") {
		t.Errorf("error should be wrapped with context, got: %v", err)
	}
}

func TestUserListCommandRegistered(t *testing.T) {
	c, _, err := userCmd.Find([]string{"list"})
	if err != nil || c.Name() != "list" {
		t.Fatalf("user list not registered: %v (got %v)", err, c.Name())
	}
	if c.Flags().Lookup("output") == nil {
		t.Error("user list missing --output flag")
	}
}
