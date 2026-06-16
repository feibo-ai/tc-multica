package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// TEA-115 verified-mirror: server-side origin fetchers.
//
// These exported helpers are the ONLY entry points the multica server's mirror
// download endpoints (internal/handler/fleet_mirror.go) use to pull artifacts
// from GitHub on a cache miss. They are deliberately separate from the daemon's
// fetch points (fetchReleaseByTag/FetchLatestRelease/attestationsAPIBase):
//
//   - The server IS the mirror origin, so it must ALWAYS hit GitHub directly
//     and must NEVER be subject to the daemon-side mirror-base rewrite — if the
//     server's origin fetch were ever pointed at a mirror, the cache would loop
//     onto itself. Keeping these in a separate, GitHub-pinned helper makes that
//     invariant structural rather than a comment.
//   - The daemon's离线验签 code path and its non-200 error handling
//     (attestation.go:204-206 / update.go:238/:264/:338) are NOT touched by
//     this file. These helpers return distinct error shapes the server cache
//     layer needs (notably MirrorNotFound for upstream 404 vs. transport errors
//     for 504/timeout, per INV-18 cache correctness) — that distinction lives
//     entirely on the SERVER side and is never observed by the daemon.
//
// INV-16: nothing here weakens or alters the daemon's offline verification —
// the bytes these functions return are byte-for-byte the GitHub originals and
// are re-verified by the daemon against the embedded trusted root + SAN triad.
// ---------------------------------------------------------------------------

// RevocationsAssetName is the exported name of the revocations table asset
// (mirrors the private revocationsAssetName used by the verification path). The
// server mirror layer uses it to route revocations.json content through the
// atomic cache unit (INV-20). Exported here rather than touching revocation.go
// so the verification logic stays byte-untouched (INV-16).
const RevocationsAssetName = revocationsAssetName

// githubReleasesAPIBase is the binary anchor (⑨) GitHub releases API prefix.
// Pinned to feibo-ai/tc-multica — the same repo as attestationSANRegex and
// attestationsAPIBase. This is the SERVER's origin constant; it is intentionally
// independent of any daemon mirror-base rewrite (the daemon agent rewrites
// fetchReleaseByTag/FetchLatestRelease/attestationsAPIBase, NOT this).
const githubReleasesAPIBase = "https://api.github.com/repos/feibo-ai/tc-multica/releases"

// mirrorOriginTimeout bounds a single upstream GitHub fetch from the server
// mirror layer. Matches the daemon's 10s metadata client; the cache layer's
// single-flight + serve-stale (INV-20) absorbs slow origins without stacking N
// daemon requests onto N GitHub calls.
const mirrorOriginTimeout = 10 * time.Second

// mirrorOriginMaxRedirects caps the redirect chain length on every server origin
// fetch. GitHub release-asset downloads legitimately redirect github.com →
// objects.githubusercontent.com (and api.github.com attestation responses may
// redirect within github.com); a small bound is generous for that while denying
// an open-redirect amplification loop.
const mirrorOriginMaxRedirects = 10

// mirrorRedirectCheck is the per-hop redirect policy installed on EVERY server
// origin http.Client (release metadata / attestation / asset). The stdlib calls
// it BEFORE following each redirect, so every intermediate target — not just the
// first URL — is re-asserted against the pinned host whitelist
// (isAllowedMirrorRedirectHost). This forecloses a future where a GitHub (or
// spoofed) 30x Location points at a non-GitHub / internal host: the server
// refuses to follow it (SSRF defense-in-depth, INV-21). It also bounds the chain
// length to deny redirect loops.
func mirrorRedirectCheck(req *http.Request, via []*http.Request) error {
	if len(via) >= mirrorOriginMaxRedirects {
		return fmt.Errorf("mirror origin: too many redirects (>%d) to %q", mirrorOriginMaxRedirects, req.URL.String())
	}
	if !isAllowedMirrorRedirectHost(req.URL) {
		return fmt.Errorf("mirror origin: refusing redirect to non-GitHub host %q", req.URL.String())
	}
	return nil
}

// isAllowedMirrorRedirectHost reports whether u is an https URL on a host that a
// legitimate GitHub origin fetch may redirect to. Superset of
// IsAllowedMirrorOriginHost (which covers asset-download hosts github.com /
// *.githubusercontent.com): also permits api.github.com and *.github.com because
// the release-metadata and attestation fetches start there and GitHub may
// redirect within its own API/asset surface. Any other host — internal
// addresses, link-shorteners, http:// — is denied.
func isAllowedMirrorRedirectHost(u *url.URL) bool {
	if u == nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "github.com" || host == "api.github.com" || strings.HasSuffix(host, ".github.com") {
		return true
	}
	if host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com") {
		return true
	}
	return false
}

// MirrorNotFound is the sentinel the server mirror layer uses to distinguish an
// authoritative upstream 404 ("this digest/asset objectively has no
// attestation/artifact") from a transport error (504 / timeout / connection
// reset). INV-18 (server cache correctness, NOT a daemon behaviour dependency):
//   - upstream 404  → MirrorNotFound → cache layer MAY cache a short-TTL
//     negative result and pass 404 through.
//   - upstream 504 / timeout / conn error → a plain error → cache layer MUST
//     NOT cache a negative result; it serve-stales a verified cached value or
//     passes the error through so the next request re-origins.
//
// The daemon never sees this distinction: it gets either bytes (200) or a
// non-200 status it treats uniformly as fail-closed (binary anchor) / fail-open
// (revocation path) — see INV-16.
type MirrorNotFound struct {
	What string
}

func (e *MirrorNotFound) Error() string { return "mirror origin 404: " + e.What }

// IsMirrorNotFound reports whether err is (or wraps) a MirrorNotFound. The
// server cache layer uses this to decide whether a negative result is cacheable
// (404, yes) vs. a transport error that must not poison the cache (no).
func IsMirrorNotFound(err error) bool {
	for err != nil {
		if _, ok := err.(*MirrorNotFound); ok {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// FetchReleaseByTag fetches a release's metadata for the given tag directly from
// the binary-anchor GitHub releases API. It is the server-side origin used by
// the mirror's MirrorReleaseByTag handler and by the MirrorAsset SSRF whitelist
// (INV-21a): the server resolves the authoritative asset set for (tag) and only
// re-origins assets present in this metadata.
//
// On upstream 404 it returns a *MirrorNotFound (cacheable negative); any other
// non-200 or transport failure returns a plain error (not cacheable). The
// returned *GitHubRelease carries the GitHub-authoritative BrowserDownloadURLs
// UNMODIFIED — the handler rewrites them for the daemon-facing response, but the
// SSRF whitelist re-origins against these originals.
func FetchReleaseByTag(tag string) (*GitHubRelease, error) {
	return fetchReleaseMetaFromGitHub(githubReleasesAPIBase + "/tags/" + url.PathEscape(tag))
}

// FetchLatestReleaseFromGitHub fetches the latest release metadata directly from
// the binary-anchor GitHub releases API. The server mirror's MirrorLatestRelease
// handler uses this rather than cli.FetchLatestRelease because the latter is one
// of the THREE daemon binary-anchor fetch points the daemon agent rewrites to a
// mirror base; the server origin must stay GitHub-pinned to avoid a self-loop.
func FetchLatestReleaseFromGitHub() (*GitHubRelease, error) {
	return fetchReleaseMetaFromGitHub(githubReleasesAPIBase + "/latest")
}

func fetchReleaseMetaFromGitHub(apiURL string) (*GitHubRelease, error) {
	client := &http.Client{Timeout: mirrorOriginTimeout, CheckRedirect: mirrorRedirectCheck}
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &MirrorNotFound{What: "release " + apiURL}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub releases API returned %d for %s", resp.StatusCode, apiURL)
	}

	var rel GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	return &rel, nil
}

// FetchAttestationBundleBytesFromGitHub pulls the raw GitHub attestations API
// response (the byte-for-byte sigstore bundle JSON, githubAttestationsResponse
// shape) for the binary anchor by content digest. The server mirror's
// MirrorAttestations handler caches and dumbly passes through these bytes
// UNMODIFIED (INV-18: no repackaging — the daemon must receive a byte-identical
// bundle so its embedded-root verifier passes exactly as it would直连 GitHub).
//
// digestHex is the lowercase hex sha256 (no "sha256:" prefix). It MUST already
// be validated by the caller (the handler enforces 64-hex per the SSRF补强);
// this function additionally rejects anything that is not exactly 64 lowercase
// hex chars as a defense-in-depth guard so a malformed digest can never be
// concatenated into the upstream URL.
//
// On upstream 404 returns *MirrorNotFound (cacheable negative); 504/timeout/conn
// error returns a plain error (not cacheable). The returned bytes are the raw
// upstream response body — NOT re-encoded.
func FetchAttestationBundleBytesFromGitHub(digestHex string, timeout time.Duration) ([]byte, error) {
	if !isLowerHex64(digestHex) {
		return nil, fmt.Errorf("invalid attestation digest %q (want 64 lowercase hex)", digestHex)
	}
	// attestationsAPIBase is the binary-anchor (⑨) constant; the daemon agent
	// turns the daemon's copy into a mirror-base variable, but here on the
	// SERVER we want the GitHub origin. attestationsAPIBase itself is the
	// GitHub URL literal, and this function lives in the server's origin path,
	// so referencing it directly is the GitHub origin. (skillAttestationsAPIBase
	// is NEVER referenced here — INV-18 skill-anchor isolation.)
	apiURL := attestationsAPIBase + "sha256:" + digestHex

	client := &http.Client{Timeout: updateDownloadTimeoutOrDefault(timeout), CheckRedirect: mirrorRedirectCheck}
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build attestations request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch attestations: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &MirrorNotFound{What: "attestations sha256:" + digestHex}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub attestations API returned %d for %s", resp.StatusCode, apiURL)
	}
	return io.ReadAll(resp.Body)
}

// FetchAssetBytesFromGitHub downloads an asset's bytes directly from a
// GitHub-authoritative BrowserDownloadURL. The caller (MirrorAsset handler) MUST
// have obtained downloadURL from FetchReleaseByTag's authoritative asset set
// (INV-21a SSRF whitelist) AND validated its host ∈ {github.com,
// *.githubusercontent.com} (INV-21 pinned host whitelist). This function
// re-asserts the host whitelist as a second, structural guard so a future caller
// cannot accidentally hand it an arbitrary URL.
//
// On upstream 404 returns *MirrorNotFound; other non-200 / transport errors
// return a plain error. Bytes are returned UNMODIFIED.
func FetchAssetBytesFromGitHub(downloadURL string, timeout time.Duration) ([]byte, error) {
	if !IsAllowedMirrorOriginHost(downloadURL) {
		return nil, fmt.Errorf("refusing to origin-fetch non-GitHub host: %q", downloadURL)
	}
	client := &http.Client{Timeout: updateDownloadTimeoutOrDefault(timeout), CheckRedirect: mirrorRedirectCheck}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return nil, fmt.Errorf("fetch asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, &MirrorNotFound{What: "asset " + downloadURL}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("asset origin returned %d for %s", resp.StatusCode, downloadURL)
	}
	return io.ReadAll(resp.Body)
}

// IsAllowedMirrorOriginHost reports whether rawURL is an https URL whose host is
// in the pinned GitHub release-asset download whitelist (INV-21 / review
// suggestion ①): host == github.com OR host is a subdomain of
// githubusercontent.com (release assets resolve to objects.githubusercontent.com
// and friends). Any other host — including api.github.com paths, internal
// addresses, or http:// — is rejected. This makes "the public mirror endpoint
// only ever fetches GitHub bytes" testable on the SERVER side, not merely
// implied by the (tag,asset) whitelist.
func IsAllowedMirrorOriginHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "github.com" {
		return true
	}
	if host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com") {
		return true
	}
	return false
}

// isLowerHex64 reports whether s is exactly 64 lowercase hexadecimal characters.
// The daemon's digest is always hex.EncodeToString(sha256(...)) (attestation.go:232),
// i.e. 64 lowercase hex; the public mirror endpoint enforces this so an external
// caller cannot inject an arbitrary {digest} path segment into the upstream
// attestations URL (SSRF补强, mirrors INV-21a for the attestation path).
func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}
