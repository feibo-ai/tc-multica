package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// validateProjectStatus must accept the five DB-backed statuses and reject
// anything else with a message that lists the valid values. `project create`,
// `project update`, and `project status` all share it (#3925: `--status active`
// used to reach the server and 500 on the CHECK constraint).
func TestValidateProjectStatus(t *testing.T) {
	for _, s := range validProjectStatuses {
		if err := validateProjectStatus(s); err != nil {
			t.Errorf("status %q should be valid, got: %v", s, err)
		}
	}
	err := validateProjectStatus("active")
	if err == nil {
		t.Fatal("status \"active\" should be rejected")
	}
	if !strings.Contains(err.Error(), "planned") {
		t.Errorf("error should list valid statuses, got: %v", err)
	}
}

// newProjectResourceUpdateTestCmd mirrors the flag surface of
// projectResourceUpdateCmd so unit tests can exercise the shortcut-flag plumbing
// without spinning up a server.
func newProjectResourceUpdateTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "update"}
	c.Flags().String("url", "", "")
	c.Flags().String("default-branch-hint", "", "")
	c.Flags().String("local-path", "", "")
	c.Flags().String("daemon-id", "", "")
	c.Flags().String("ref-label", "", "")
	c.Flags().String("ref", "", "")
	c.Flags().String("label", "", "")
	c.Flags().Bool("clear-label", false, "")
	c.Flags().Int32("position", 0, "")
	c.Flags().String("output", "json", "")
	return c
}

// TestBuildResourceRefFromFlagsGithubMergesHint pins the nit fix from MUL-2662
// review round 2: `multica project resource update <p> <r> --default-branch-hint x`
// must rebuild the full github_repo payload by merging the existing `url` —
// otherwise the server sees `{default_branch_hint: "x"}` and 400s.
func TestBuildResourceRefFromFlagsGithubMergesHint(t *testing.T) {
	t.Run("hint-only edit preserves existing url", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("default-branch-hint", "main")
		existing := map[string]any{"url": "https://github.com/multica-ai/multica"}

		ref, has, err := buildResourceRefFromFlags(cmd, "github_repo", existing)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Fatalf("expected has=true when default-branch-hint is set")
		}
		if ref["url"] != "https://github.com/multica-ai/multica" {
			t.Errorf("expected merged url, got %v", ref["url"])
		}
		if ref["default_branch_hint"] != "main" {
			t.Errorf("expected merged hint=main, got %v", ref["default_branch_hint"])
		}
	})

	t.Run("hint=empty clears the hint but keeps url", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("default-branch-hint", "")
		existing := map[string]any{
			"url":                 "https://github.com/multica-ai/multica",
			"default_branch_hint": "stale",
		}
		ref, has, err := buildResourceRefFromFlags(cmd, "github_repo", existing)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Fatalf("expected has=true")
		}
		if ref["url"] != "https://github.com/multica-ai/multica" {
			t.Errorf("expected url to survive empty-hint clear, got %v", ref["url"])
		}
		if _, ok := ref["default_branch_hint"]; ok {
			t.Errorf("expected default_branch_hint to be cleared, got %v", ref["default_branch_hint"])
		}
	})

	t.Run("url override survives merge", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("url", "https://github.com/multica-ai/new-repo")
		existing := map[string]any{
			"url":                 "https://github.com/multica-ai/multica",
			"default_branch_hint": "main",
		}
		ref, has, err := buildResourceRefFromFlags(cmd, "github_repo", existing)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Fatalf("expected has=true")
		}
		if ref["url"] != "https://github.com/multica-ai/new-repo" {
			t.Errorf("expected overridden url, got %v", ref["url"])
		}
		if ref["default_branch_hint"] != "main" {
			t.Errorf("expected merged hint to persist, got %v", ref["default_branch_hint"])
		}
	})

	t.Run("hint-only with no existing url fails fast", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("default-branch-hint", "main")
		_, _, err := buildResourceRefFromFlags(cmd, "github_repo", nil)
		if err == nil {
			t.Fatalf("expected error when no existing url is available to merge")
		}
	})

	t.Run("no flags set returns has=false", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		ref, has, err := buildResourceRefFromFlags(cmd, "github_repo", map[string]any{"url": "https://x"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if has {
			t.Errorf("expected has=false when no shortcut flag is set, got ref=%v", ref)
		}
	})
}

// TestBuildResourceRefFromFlagsLocalDirectoryMerges covers the same merge
// behavior for local_directory: partial edits keep unmentioned fields from the
// existing ref.
func TestBuildResourceRefFromFlagsLocalDirectoryMerges(t *testing.T) {
	t.Run("ref-label only edit preserves existing path + daemon", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("ref-label", "renamed")
		existing := map[string]any{
			"local_path": "/Users/foo/work/a",
			"daemon_id":  "d1",
			"label":      "old",
		}
		ref, has, err := buildResourceRefFromFlags(cmd, "local_directory", existing)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Fatalf("expected has=true")
		}
		if ref["local_path"] != "/Users/foo/work/a" {
			t.Errorf("local_path missing after merge: %v", ref["local_path"])
		}
		if ref["daemon_id"] != "d1" {
			t.Errorf("daemon_id missing after merge: %v", ref["daemon_id"])
		}
		if ref["label"] != "renamed" {
			t.Errorf("label not overridden: %v", ref["label"])
		}
	})

	t.Run("local-path only without existing daemon fails", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("local-path", "/Users/foo/work/b")
		_, _, err := buildResourceRefFromFlags(cmd, "local_directory", nil)
		if err == nil {
			t.Fatalf("expected error when daemon_id is missing from both flags and existing ref")
		}
	})

	t.Run("ref-label cleared on empty input", func(t *testing.T) {
		cmd := newProjectResourceUpdateTestCmd()
		_ = cmd.Flags().Set("ref-label", "")
		existing := map[string]any{
			"local_path": "/Users/foo/work/a",
			"daemon_id":  "d1",
			"label":      "to-clear",
		}
		ref, has, err := buildResourceRefFromFlags(cmd, "local_directory", existing)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Fatalf("expected has=true")
		}
		if _, ok := ref["label"]; ok {
			t.Errorf("expected embedded label to be cleared, got %v", ref["label"])
		}
	})
}

func newProjectCreateTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "create"}
	cmd.Flags().String("title", "", "")
	cmd.Flags().String("description", "", "")
	cmd.Flags().String("status", "", "")
	cmd.Flags().String("icon", "", "")
	cmd.Flags().String("lead", "", "")
	cmd.Flags().String("dri", "", "")
	cmd.Flags().String("priority", "", "")
	cmd.Flags().String("start-date", "", "")
	cmd.Flags().String("due-date", "", "")
	cmd.Flags().StringArray("repo", nil, "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

// TestRunProjectCreateSendsDatesDriPriority pins that `project create` forwards
// --start-date/--due-date/--priority/--dri as start_date/due_date/priority/
// dri_user_id in the POST body. These were API-only before; the CLI never
// exposed them, so a CLI/skill-created project could not carry a start,
// deadline, or priority.
func TestRunProjectCreateSendsDatesDriPriority(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "proj-1", "title": "P", "status": "planned"})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")

	cmd := newProjectCreateTestCmd()
	_ = cmd.Flags().Set("title", "P")
	_ = cmd.Flags().Set("dri", "11111111-1111-1111-1111-111111111111")
	_ = cmd.Flags().Set("start-date", "2026-07-01")
	_ = cmd.Flags().Set("due-date", "2026-07-31")
	_ = cmd.Flags().Set("priority", "high")
	if err := runProjectCreate(cmd, nil); err != nil {
		t.Fatalf("runProjectCreate: %v", err)
	}

	if got := body["start_date"]; got != "2026-07-01" {
		t.Errorf("start_date = %#v, want 2026-07-01", got)
	}
	if got := body["due_date"]; got != "2026-07-31" {
		t.Errorf("due_date = %#v, want 2026-07-31", got)
	}
	if got := body["priority"]; got != "high" {
		t.Errorf("priority = %#v, want high", got)
	}
	if got := body["dri_user_id"]; got != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("dri_user_id = %#v, want the passed UUID", got)
	}
}

// TestRunProjectCreateOmitsUnsetFields pins that unset date/priority/dri flags
// stay out of the body, so create never sends empty strings the server would
// reject (bad date) or misread.
func TestRunProjectCreateOmitsUnsetFields(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "proj-1", "title": "P", "status": "planned"})
	}))
	defer srv.Close()

	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")

	cmd := newProjectCreateTestCmd()
	_ = cmd.Flags().Set("title", "P")
	if err := runProjectCreate(cmd, nil); err != nil {
		t.Fatalf("runProjectCreate: %v", err)
	}

	for _, k := range []string{"start_date", "due_date", "priority", "dri_user_id"} {
		if _, present := body[k]; present {
			t.Errorf("body should omit %q when its flag is unset, got %#v", k, body[k])
		}
	}
}
