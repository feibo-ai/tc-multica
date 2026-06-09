package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Structured, counts-only telemetry attached to the daemon register payload so
// the server can see "who's on what version, with what skill health, and what
// 命门B(publish) success rate" — without ever collecting document content,
// tokens, or source. Mirrors the ambient_usage discipline: bare counts only,
// best-effort (a read failure reports zeros, never breaks registration).

// gateCounts is the bare pass/fail tally that publish.py (命门B) writes to
// <profile>/gate-counts.json. No error strings, paths, or content — counts only.
type gateCounts struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
}

// skillHealth is a counts-only snapshot of local skill hygiene (SOP ❌5):
// total skills, how many lack an owner, how many are unreviewed/stale (>90d).
type skillHealth struct {
	Total        int `json:"total"`
	MissingOwner int `json:"missing_owner"`
	Stale        int `json:"stale"`
}

// readGateCounts loads the 命门B tally for the daemon's profile. Best-effort:
// a missing or corrupt file reports zeros.
func readGateCounts(profile string) gateCounts {
	dir, err := cli.ProfileDir(profile)
	if err != nil {
		return gateCounts{}
	}
	return readGateCountsAt(filepath.Join(dir, "gate-counts.json"))
}

// readGateCountsAt is the path-injected core (testable without a profile dir).
func readGateCountsAt(path string) gateCounts {
	gc := gateCounts{}
	data, err := os.ReadFile(path)
	if err != nil {
		return gc
	}
	_ = json.Unmarshal(data, &gc) // corrupt → zeros (best-effort)
	return gc
}

// localSkillHealth tallies hygiene for the local Claude skills the daemon
// manages. Best-effort: an unreadable root reports zeros.
func localSkillHealth() skillHealth {
	root, ok, err := localSkillRootForProvider("claude")
	if err != nil || !ok {
		return skillHealth{}
	}
	return skillHealthForRoot(root)
}

// skillHealthForRoot is the root-injected core (testable with a temp dir).
// counts-only: never records skill names, paths, or content.
func skillHealthForRoot(root string) skillHealth {
	h := skillHealth{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return h
	}
	for _, e := range entries {
		if !e.IsDir() || isIgnoredLocalSkillEntry(e.Name()) {
			continue
		}
		content, err := readLocalSkillMainFile(filepath.Join(root, e.Name()))
		if err != nil {
			continue // no/oversized SKILL.md → not a countable skill
		}
		h.Total++
		owner, reviewed := parseSkillHygiene(content)
		if owner == "" {
			h.MissingOwner++
		}
		if isStaleReview(reviewed) {
			h.Stale++
		}
	}
	return h
}

// parseSkillHygiene pulls owner + last_reviewed_at from SKILL.md frontmatter
// (the two SOP ❌5 hygiene fields). Sibling of parseLocalSkillFrontmatter, which
// only needs name/description.
func parseSkillHygiene(content string) (owner, lastReviewed string) {
	if !strings.HasPrefix(content, "---") {
		return "", ""
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return "", ""
	}
	for _, line := range strings.Split(content[3:3+end], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "owner:") {
			owner = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "owner:")), "\"'")
		} else if strings.HasPrefix(line, "last_reviewed_at:") {
			lastReviewed = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "last_reviewed_at:")), "\"'")
		}
	}
	return owner, lastReviewed
}

// isStaleReview reports whether a last_reviewed_at date is missing or > 90 days
// old (mirrors the `skill lint` staleness gate).
func isStaleReview(lastReviewed string) bool {
	if lastReviewed == "" {
		return true
	}
	t, err := time.Parse("2006-01-02", lastReviewed)
	if err != nil {
		return true
	}
	return time.Since(t) > 90*24*time.Hour
}
