package cli

import "strings"

// ---------------------------------------------------------------------------
// TEA-115 verified-mirror: daemon-side binary-anchor fetch-base rewrite.
//
// This is the ONLY daemon-side mirror plumbing. It does exactly one thing: let
// the daemon redirect the byte-transport host of its THREE binary-anchor fetch
// points away from api.github.com to the multica server's verified-mirror
// download endpoint (<mirror>/api/cli/mirror), so a one-click fleet update no
// longer stacks N daemons onto N GitHub release/attestations API calls (the
// thundering-herd 504/403 from TEA-113).
//
// The three rewritten fetch points (and ONLY these three):
//   - fetchReleaseByTag      (update.go)      → <mirror>/releases/tags/<tag>
//   - FetchLatestRelease     (update.go)      → <mirror>/releases/latest
//   - attestationsAPIBase    (attestation.go) → <mirror>/attestations/sha256:<digest>
//
// HARD boundaries (INV-16 / INV-18 / INV-21b):
//   - The offline verification chain (verifyProvenanceBundlesWithSAN,
//     verifyAssetSHA256, checkRevocationAgainstList, pickEffectiveList, the
//     embedded trusted root, both SAN regexes) is NOT touched: it takes BYTES
//     and is independent of which host those bytes came from. The mirror is an
//     untrusted dumb cache; the daemon re-verifies every byte fail-closed.
//   - The daemon's non-200 error handling at every fetch point
//     (attestation.go:204-206 / update.go:238 / :264 / :338) is NOT touched:
//     the daemon treats any non-200 from the mirror identically to GitHub
//     (uniform error; binary anchor fail-closed, revocation path fail-open).
//     404 vs 504 is a SERVER cache-correctness concern only — never a daemon
//     behaviour dependency.
//   - The skill second trust anchor (skillAttestationsAPIBase attestation.go:61,
//     skillReleasesAPIURL skillsync.go:39) is OUT OF SCOPE this cycle: it stays
//     const, stays pinned to GitHub, and is NEVER referenced by the rewrite
//     helpers below. Only the binary anchor moves to the mirror.
//   - asset.BrowserDownloadURL is rewritten on the SERVER (MirrorReleaseByTag),
//     so the daemon downloads assets/checksums/revocations from the value the
//     server already rewrote — the daemon does NOT second-rewrite asset URLs
//     (INV-21b single rewrite authority, no config drift).
// ---------------------------------------------------------------------------

// updateMirrorBase is the base URL the daemon's three binary-anchor fetch
// points are rewritten against. Empty = no rewrite (fall back to the
// GitHub-pinned literals, preserving the bootstrap / old-machine path).
//
// It is a package-level var, set once at daemon startup via SetUpdateMirrorBase
// from cfg.UpdateMirrorBase, rather than threaded through every call site,
// because fetchReleaseByTag / FetchLatestRelease / attestationsAPIBase are
// reached from many places (auto-update poller, manual `multica update`,
// revocation gate's secondary attestation fetch) and the rewrite must apply
// uniformly to all of them.
var updateMirrorBase string

// SetUpdateMirrorBase records the verified-mirror base URL the daemon will use
// for its three binary-anchor fetch points. Called once during daemon startup
// (daemon.New) with cfg.UpdateMirrorBase. A trailing slash is trimmed so the
// join helpers below can append paths uniformly. Passing "" disables the
// rewrite (fetches fall back to the GitHub-pinned literals).
func SetUpdateMirrorBase(base string) {
	updateMirrorBase = strings.TrimRight(strings.TrimSpace(base), "/")
}

// UpdateMirrorBase returns the currently configured mirror base (trimmed), or
// "" if none is set. Exported for tests / diagnostics.
func UpdateMirrorBase() string {
	return updateMirrorBase
}

// releasesBaseURL returns the base for release-metadata fetches: the mirror's
// /releases prefix when a mirror is configured, otherwise the GitHub-pinned
// binary-anchor releases API literal (bootstrap / old-machine path). The repo
// (feibo-ai/tc-multica) is fixed on both sides — the mirror only relocates the
// host, never the trust anchor.
func releasesBaseURL() string {
	if updateMirrorBase != "" {
		return updateMirrorBase + "/releases"
	}
	return "https://api.github.com/repos/feibo-ai/tc-multica/releases"
}

// attestationsBaseURL returns the base for binary-anchor (⑨) attestation
// fetches: the mirror's /attestations/ prefix when a mirror is configured,
// otherwise the GitHub-pinned attestationsAPIBase literal. The skill anchor
// (skillAttestationsAPIBase) is deliberately NOT routed through here — it stays
// const and GitHub-pinned (INV-18 skill-anchor isolation).
func attestationsBaseURL() string {
	if updateMirrorBase != "" {
		return updateMirrorBase + "/attestations/"
	}
	return attestationsAPIBase
}
