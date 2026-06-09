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

func TestEstimateSkillTokens(t_ *testing.T) {
	cases := map[string]int{
		"":                 0,
		"one two three":    3, // 3 words * 1.3 = 3.9 -> 3
		"a b c d e f g h i j": 13, // 10 * 1.3 = 13
	}
	for body, want := range cases {
		if got := estimateSkillTokens(body); got != want {
			t_.Errorf("estimateSkillTokens(%q) = %d, want %d", body, got, want)
		}
	}
}

func TestParseSkillFrontmatter(t_ *testing.T) {
	md := "---\nname: tc-x\ndescription: \"Use when: do a thing\"\nlast_reviewed_at: 2026-06-09\n---\n\n# Heading\nbody\n"
	fm, body := parseSkillFrontmatter(md)
	if fm["name"] != "tc-x" {
		t_.Errorf("name = %q", fm["name"])
	}
	// value keeps text after the first colon (internal colons preserved), quotes trimmed
	if fm["description"] != "Use when: do a thing" {
		t_.Errorf("description = %q", fm["description"])
	}
	if fm["last_reviewed_at"] != "2026-06-09" {
		t_.Errorf("last_reviewed_at = %q", fm["last_reviewed_at"])
	}
	if !strings.HasPrefix(body, "\n# Heading") {
		t_.Errorf("body = %q", body)
	}
	// no frontmatter -> empty map, full text as body
	if fm2, b2 := parseSkillFrontmatter("# plain\nno fm\n"); len(fm2) != 0 || b2 != "# plain\nno fm\n" {
		t_.Errorf("no-frontmatter case: fm=%v body=%q", fm2, b2)
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
