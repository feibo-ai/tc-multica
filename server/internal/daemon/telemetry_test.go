package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadGateCountsAt(t *testing.T) {
	dir := t.TempDir()

	// missing file → zeros (best-effort)
	if gc := readGateCountsAt(filepath.Join(dir, "nope.json")); gc.Pass != 0 || gc.Fail != 0 {
		t.Fatalf("missing file should be zeros, got %+v", gc)
	}

	// valid counts
	p := filepath.Join(dir, "gate-counts.json")
	if err := os.WriteFile(p, []byte(`{"pass":3,"fail":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if gc := readGateCountsAt(p); gc.Pass != 3 || gc.Fail != 1 {
		t.Fatalf("got %+v, want pass=3 fail=1", gc)
	}

	// corrupt → zeros, never panics
	if err := os.WriteFile(p, []byte(`{ not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if gc := readGateCountsAt(p); gc.Pass != 0 || gc.Fail != 0 {
		t.Fatalf("corrupt file should be zeros, got %+v", gc)
	}
}

func writeSkill(t *testing.T, root, name, frontmatter string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\n" + frontmatter + "---\n\n# " + name + "\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillHealthForRoot(t *testing.T) {
	root := t.TempDir()
	today := time.Now().Format("2006-01-02")

	writeSkill(t, root, "healthy", "name: healthy\nowner: 曾振华\nlast_reviewed_at: "+today+"\n")
	writeSkill(t, root, "noowner", "name: noowner\nlast_reviewed_at: "+today+"\n")          // missing owner
	writeSkill(t, root, "stale", "name: stale\nowner: 曾振华\nlast_reviewed_at: 2020-01-01\n") // stale review
	// no-frontmatter skill: counted, but missing owner + stale (no review date)
	bare := filepath.Join(root, "bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bare, "SKILL.md"), []byte("# bare\nno frontmatter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a dir without SKILL.md → not counted
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	h := skillHealthForRoot(root)
	if h.Total != 4 {
		t.Errorf("Total = %d, want 4 (empty dir without SKILL.md excluded)", h.Total)
	}
	if h.MissingOwner != 2 {
		t.Errorf("MissingOwner = %d, want 2 (noowner + bare)", h.MissingOwner)
	}
	if h.Stale != 2 {
		t.Errorf("Stale = %d, want 2 (stale + bare)", h.Stale)
	}
}

func TestParseSkillHygiene(t *testing.T) {
	owner, reviewed := parseSkillHygiene("---\nname: x\nowner: \"曾振华\"\nlast_reviewed_at: 2026-06-09\n---\nbody")
	if owner != "曾振华" {
		t.Errorf("owner = %q, want 曾振华", owner)
	}
	if reviewed != "2026-06-09" {
		t.Errorf("lastReviewed = %q, want 2026-06-09", reviewed)
	}
	// no frontmatter → empty
	if o, r := parseSkillHygiene("# plain\nbody"); o != "" || r != "" {
		t.Errorf("no-frontmatter = (%q,%q), want empty", o, r)
	}
}

func TestIsStaleReview(t *testing.T) {
	today := time.Now().Format("2006-01-02")
	cases := map[string]bool{
		"":           true,  // missing → stale
		"not-a-date": true,  // unparseable → stale
		"2020-01-01": true,  // >90d → stale
		today:        false, // today → fresh
	}
	for in, want := range cases {
		if got := isStaleReview(in); got != want {
			t.Errorf("isStaleReview(%q) = %v, want %v", in, got, want)
		}
	}
}
