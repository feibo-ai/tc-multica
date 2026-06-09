package main

import (
	"strings"
	"testing"
)

func TestBuildSkillMd(t_ *testing.T) {
	skill := map[string]any{
		"name":             "tc-render",
		"description":      `He said "hi" and used a \ backslash`,
		"last_reviewed_at": "2026-06-09",
		"content":          "\n# tc-render\n\nbody line\n",
	}
	md := buildSkillMd(skill)

	// frontmatter present + description YAML-escaped (\" and \\)
	for _, want := range []string{
		"---\n",
		"name: tc-render\n",
		`description: "He said \"hi\" and used a \\ backslash"`,
		"last_reviewed_at: 2026-06-09\n",
	} {
		if !strings.Contains(md, want) {
			t_.Errorf("buildSkillMd missing %q\n---\n%s", want, md)
		}
	}
	// body preserved exactly, after the closing frontmatter fence + blank line
	if !strings.HasSuffix(md, "\n# tc-render\n\nbody line\n") {
		t_.Errorf("body not preserved at tail:\n%s", md)
	}
	// exactly two frontmatter fences
	if n := strings.Count(md, "---\n"); n != 2 {
		t_.Errorf("expected 2 frontmatter fences, got %d", n)
	}
}

func TestBuildSkillMd_NoOptionalFields(t_ *testing.T) {
	skill := map[string]any{
		"name":        "x",
		"description": "d",
		"content":     "no leading newline body",
	}
	md := buildSkillMd(skill)
	// last_reviewed_at omitted when absent
	if strings.Contains(md, "last_reviewed_at") {
		t_.Errorf("should omit last_reviewed_at when absent:\n%s", md)
	}
	// a blank line is inserted between frontmatter and a body lacking a leading newline
	if !strings.Contains(md, "---\n\nno leading newline body\n") {
		t_.Errorf("expected blank line before body:\n%s", md)
	}
}
