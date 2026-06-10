package cli

// skillsync_select_test.go —— FetchLatestSkillBundle 的离线滤选/版本比较单测。
// 不联网:直接喂假 []GitHubRelease,断言 selectLatestSkillRelease 选出版本最高的
// skills-v* release,且非 skills-v* / 不可解析的 tag 被排除。

import (
	"encoding/json"
	"testing"
)

// fakeReleasesJSON 模拟 GitHub /releases 列表响应:乱序、含噪声 tag。
// 期望选出 skills-v0.3.10(0.3.10 > 0.3.9 > 0.2.5;非 skills-v* 与不可解析的全排除)。
const fakeReleasesJSON = `[
  {"tag_name": "v0.1.42",            "html_url": "x", "assets": [{"name": "skill-bundle.tar.gz", "browser_download_url": "u-cli"}]},
  {"tag_name": "skills-v0.2.5",      "html_url": "x", "assets": [{"name": "skill-bundle.tar.gz", "browser_download_url": "u-025"}]},
  {"tag_name": "skills-v0.3.10",     "html_url": "x", "assets": [{"name": "skill-bundle.tar.gz", "browser_download_url": "u-0310"}]},
  {"tag_name": "skills-v0.3.9",      "html_url": "x", "assets": [{"name": "skill-bundle.tar.gz", "browser_download_url": "u-039"}]},
  {"tag_name": "skills-vNIGHTLY",    "html_url": "x", "assets": [{"name": "skill-bundle.tar.gz", "browser_download_url": "u-bad"}]},
  {"tag_name": "skills-v0.3",        "html_url": "x", "assets": [{"name": "skill-bundle.tar.gz", "browser_download_url": "u-short"}]}
]`

func decodeFakeReleases(t *testing.T, raw string) []GitHubRelease {
	t.Helper()
	var releases []GitHubRelease
	if err := json.Unmarshal([]byte(raw), &releases); err != nil {
		t.Fatalf("unmarshal fake releases: %v", err)
	}
	return releases
}

func TestSelectLatestSkillRelease_PicksHighestSemver(t *testing.T) {
	releases := decodeFakeReleases(t, fakeReleasesJSON)

	got := selectLatestSkillRelease(releases)
	if got == nil {
		t.Fatal("expected a skills-v* release, got nil")
	}
	if got.TagName != "skills-v0.3.10" {
		t.Fatalf("selected wrong release: got %q, want %q", got.TagName, "skills-v0.3.10")
	}
	// Sanity: the chosen release carries the expected asset URL so a downstream
	// download would hit the right artifact.
	var url string
	for _, a := range got.Assets {
		if a.Name == skillBundleAssetName {
			url = a.BrowserDownloadURL
		}
	}
	if url != "u-0310" {
		t.Fatalf("selected release asset url: got %q, want %q", url, "u-0310")
	}
}

func TestSelectLatestSkillRelease_NumericOrderingNotLexical(t *testing.T) {
	// 0.3.10 must beat 0.3.9 — a string compare would wrongly pick "0.3.9".
	releases := decodeFakeReleases(t, `[
      {"tag_name": "skills-v0.3.9",  "html_url": "x", "assets": []},
      {"tag_name": "skills-v0.3.10", "html_url": "x", "assets": []}
    ]`)
	got := selectLatestSkillRelease(releases)
	if got == nil || got.TagName != "skills-v0.3.10" {
		t.Fatalf("numeric ordering failed: got %+v, want skills-v0.3.10", got)
	}
}

func TestSelectLatestSkillRelease_NoSkillReleases(t *testing.T) {
	releases := decodeFakeReleases(t, `[
      {"tag_name": "v0.1.42",    "html_url": "x", "assets": []},
      {"tag_name": "nightly",    "html_url": "x", "assets": []},
      {"tag_name": "skills-vXY", "html_url": "x", "assets": []}
    ]`)
	if got := selectLatestSkillRelease(releases); got != nil {
		t.Fatalf("expected nil when no parseable skills-v* release exists, got %q", got.TagName)
	}
	if got := selectLatestSkillRelease(nil); got != nil {
		t.Fatalf("expected nil for empty input, got %q", got.TagName)
	}
}

func TestSkillBundleVersion_PrefixAndParse(t *testing.T) {
	cases := []struct {
		tag    string
		want   string
		wantOK bool
	}{
		{"skills-v0.3.10", "0.3.10", true},
		{"skills-v1.0.0", "1.0.0", true},
		{"v0.1.42", "", false},              // wrong prefix (binary release)
		{"skills-v0.3", "", false},          // not x.y.z
		{"skills-vfoo", "", false},          // non-numeric
		{"skills-v", "", false},             // empty version
		{"  skills-v2.4.6 ", "2.4.6", true}, // trimmed
	}
	for _, c := range cases {
		got, ok := skillBundleVersion(c.tag)
		if got != c.want || ok != c.wantOK {
			t.Errorf("skillBundleVersion(%q) = (%q, %v), want (%q, %v)", c.tag, got, ok, c.want, c.wantOK)
		}
	}
}
