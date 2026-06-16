package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/cli"
)

// ---------------------------------------------------------------------------
// TEA-115 verified-mirror download endpoints (mini-ADR delta INV-16..INV-21).
//
// The server is a DUMB, UNTRUSTED cache (INV-19): it caches and serves GitHub
// release artifacts (metadata / archive / checksums / revocations / attestation
// bundles) so that a fleet of daemons hammering the same artifact costs GitHub
// ONE upstream request, not N (TEA-113 thundering-herd root cause). It makes
// ZERO security adjudication — the daemon re-runs the full offline verification
// chain (attestation triad + revocation + SHA-256, fail-closed) on every byte
// (INV-16). The trust anchor lives in the daemon's embedded trusted root + SAN
// triad, NOT in this server; the mirror can swap a host but cannot swap the
// anchor. The daemon's verification code and its non-200 error handling are NOT
// changed by this feature — these endpoints exist purely to move WHICH HOST the
// bytes come from, off the GitHub分发 hot path.
//
// All four routes are PUBLIC (no DaemonAuth/Auth): an old v0.4.x daemon doing
// its bootstrap first-hop cannot reach an authenticated endpoint, the bytes are
// non-secret, and their authenticity is anchored by attestation (INV-19). The
// worst case of a poisoned/wrong-version mirror is DoS or downgrade, never code
// injection (INV-17).
// ---------------------------------------------------------------------------

// mirrorAssetTimeout bounds a single asset download origin fetch.
const mirrorAssetTimeout = 120 * time.Second

// MirrorReleaseByTag handles GET /api/cli/mirror/releases/tags/{tag}.
//
// It serves the GitHub release metadata for {tag}, with every
// asset.BrowserDownloadURL REWRITTEN to point at this mirror's asset endpoint
// (/api/cli/mirror/assets/{tag}/{name}) so the daemon's subsequent downloads
// naturally come back to the mirror (INV-21b: the server is the SINGLE rewrite
// authority for asset URLs; the daemon does NOT re-rewrite them).
func (h *Handler) MirrorReleaseByTag(w http.ResponseWriter, r *http.Request) {
	tag := strings.TrimSpace(chi.URLParam(r, "tag"))
	if tag == "" {
		writeError(w, http.StatusBadRequest, "missing tag")
		return
	}

	rel, err := h.MirrorCache.getReleaseByTag(tag)
	if err != nil {
		h.writeMirrorOriginError(w, err, "release "+tag)
		return
	}

	writeJSON(w, http.StatusOK, h.rewriteReleaseForMirror(rel, rel.TagName))
}

// MirrorLatestRelease handles GET /api/cli/mirror/releases/latest.
//
// Same as MirrorReleaseByTag but for the latest release (bucket ② 60s TTL +
// serve-stale). The daemon's revocations path resolves the revocations.json
// asset URL from THIS response (it calls FetchLatestRelease), so the rewrite
// here routes the吊销表 content download back through the mirror's asset
// endpoint keyed by the latest tag.
func (h *Handler) MirrorLatestRelease(w http.ResponseWriter, r *http.Request) {
	rel, err := h.MirrorCache.getLatest()
	if err != nil {
		h.writeMirrorOriginError(w, err, "latest release")
		return
	}
	writeJSON(w, http.StatusOK, h.rewriteReleaseForMirror(rel, rel.TagName))
}

// MirrorAttestations handles GET /api/cli/mirror/attestations/{digest}.
//
// {digest} is the daemon's content digest in the form "sha256:<64-hex>" (the
// daemon builds the URL as base+"sha256:"+hex, attestation.go:186). The handler
// strips the algorithm prefix, validates 64 lowercase hex (SSRF补强 — the
// public endpoint must never concatenate an arbitrary path segment into the
// upstream attestations URL), and dumbly passes through the byte-for-byte GitHub
// sigstore bundle JSON (INV-18: no repackaging, so the daemon's verifier sees a
// byte-identical bundle and验过 exactly as it would直连 GitHub).
//
// Cache key = digest (content-addressed, NOT tag-derived). 404≠504 (INV-18,
// pure server cache correctness): upstream 404 → cacheable negative, passed
// through as 404; upstream 504/timeout → never cached, passed through as 502 so
// the daemon retries. The daemon does NOT distinguish these (INV-16) — it is a
// server cache-correctness property only.
func (h *Handler) MirrorAttestations(w http.ResponseWriter, r *http.Request) {
	digestParam := strings.TrimSpace(chi.URLParam(r, "digest"))
	// The daemon sends "sha256:<hex>"; accept that exact algorithm and reject
	// anything else. Strip the prefix to the bare hex used as the cache key and
	// upstream digest.
	digestHex := strings.TrimPrefix(digestParam, "sha256:")
	if digestHex == digestParam || !isLowerHex64Handler(digestHex) {
		// Not "sha256:<64-hex>". Reject as 404 (an unknown digest objectively
		// has no attestation) WITHOUT touching the origin — closes the
		// arbitrary-{digest} SSRF probing vector on the public endpoint.
		writeError(w, http.StatusNotFound, "no attestation for digest")
		return
	}

	bundleBytes, err := h.MirrorCache.getAttestation(digestHex)
	if err != nil {
		h.writeMirrorOriginError(w, err, "attestation sha256:"+digestHex)
		return
	}

	// Dumb byte-for-byte pass-through of the GitHub attestations API response.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bundleBytes)
}

// MirrorAsset handles GET /api/cli/mirror/assets/{tag}/{asset}.
//
// archive / checksums.txt / revocations.json all flow through this one endpoint.
//
// INV-21a SSRF whitelist: the handler NEVER concatenates the daemon-supplied
// {asset} into an arbitrary upstream URL. It resolves the AUTHORITATIVE release
// metadata for {tag} (cli.FetchReleaseByTag, cached + single-flight'd), looks up
// {asset} in that release's asset set, and only re-origins using the
// GitHub-authoritative BrowserDownloadURL when {asset} is present. An asset name
// not in the authoritative set → 404, no origin fetch. The origin fetch path
// additionally re-asserts host ∈ {github.com, *.githubusercontent.com}
// (INV-21 pinned host whitelist).
//
// revocations.json gets special handling: it is served from the atomic cache
// unit (INV-20 / BLOCKER-2) so that the attestation the daemon subsequently
// requests for sha256(served-bytes) is guaranteed to be the bundle from the
// SAME origin batch — no stale-content / fresh-attestation digest mismatch that
// would fail-open the吊销门.
func (h *Handler) MirrorAsset(w http.ResponseWriter, r *http.Request) {
	tag := strings.TrimSpace(chi.URLParam(r, "tag"))
	assetName := strings.TrimSpace(chi.URLParam(r, "asset"))
	if tag == "" || assetName == "" {
		writeError(w, http.StatusBadRequest, "missing tag or asset")
		return
	}

	// INV-21a: resolve the authoritative asset set and whitelist {asset}.
	rel, err := h.MirrorCache.getReleaseByTag(tag)
	if err != nil {
		h.writeMirrorOriginError(w, err, "release "+tag)
		return
	}
	downloadURL, ok := authoritativeAssetURL(rel, assetName)
	if !ok {
		// Not in the authoritative asset set → never origin-fetch. Closes the
		// "rename the asset to make the server fetch an arbitrary github/internal
		// path" SSRF vector.
		writeError(w, http.StatusNotFound, "no such asset in release")
		return
	}
	// Second, structural host guard (INV-21 pinned host whitelist): even an
	// authoritative URL must resolve to a GitHub release-asset host.
	if !cli.IsAllowedMirrorOriginHost(downloadURL) {
		writeError(w, http.StatusNotFound, "asset host not allowed")
		return
	}

	// revocations.json → atomic cache unit (INV-20 / BLOCKER-2).
	if assetName == cli.RevocationsAssetName {
		unit, uerr := h.MirrorCache.getRevocationUnit(tag, assetName, downloadURL)
		if uerr != nil {
			h.writeMirrorOriginError(w, uerr, "revocations "+tag)
			return
		}
		if unit.negative {
			writeError(w, http.StatusNotFound, "no revocations asset in release")
			return
		}
		writeMirrorBytes(w, unit.content)
		return
	}

	// archive / checksums.txt → immutable bucket.
	data, derr := h.MirrorCache.getAsset(tag, assetName, downloadURL)
	if derr != nil {
		h.writeMirrorOriginError(w, derr, "asset "+assetName)
		return
	}
	writeMirrorBytes(w, data)
}

// rewriteReleaseForMirror returns a copy of rel with every asset's
// BrowserDownloadURL rewritten to this mirror's asset endpoint for tag (INV-21b
// single rewrite authority). When the server has no PublicURL configured the
// URLs are left UNMODIFIED (the daemon then falls back to GitHub for those
// bytes; the rewrite is the optimization, not a security gate — the bytes are
// re-verified regardless).
func (h *Handler) rewriteReleaseForMirror(rel *cli.GitHubRelease, tag string) cli.GitHubRelease {
	out := *rel
	out.Assets = make([]cli.GitHubReleaseAsset, len(rel.Assets))
	copy(out.Assets, rel.Assets)

	base := strings.TrimRight(h.cfg.PublicURL, "/")
	if base == "" {
		return out
	}
	for i := range out.Assets {
		out.Assets[i].BrowserDownloadURL = base + "/api/cli/mirror/assets/" +
			tag + "/" + out.Assets[i].Name
	}
	return out
}

// authoritativeAssetURL returns the GitHub-authoritative BrowserDownloadURL for
// assetName within rel, or ("", false) if absent. Exact-name match only.
func authoritativeAssetURL(rel *cli.GitHubRelease, assetName string) (string, bool) {
	for i := range rel.Assets {
		if rel.Assets[i].Name == assetName {
			return rel.Assets[i].BrowserDownloadURL, true
		}
	}
	return "", false
}

// writeMirrorOriginError maps an origin fetch error to a daemon-facing status.
// An authoritative upstream 404 passes through as 404; everything else
// (504/timeout/conn/origin error) becomes 502. The daemon treats ANY non-200
// uniformly (INV-16), so this mapping is for cache correctness / diagnosability
// only, never a daemon behaviour dependency.
func (h *Handler) writeMirrorOriginError(w http.ResponseWriter, err error, what string) {
	if cli.IsMirrorNotFound(err) {
		writeError(w, http.StatusNotFound, "not found: "+what)
		return
	}
	writeError(w, http.StatusBadGateway, "mirror origin unavailable: "+what)
}

func writeMirrorBytes(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// isLowerHex64Handler mirrors cli.isLowerHex64 for the handler layer's
// validation of the {digest} path param before it ever reaches the cache /
// origin (SSRF补强).
func isLowerHex64Handler(s string) bool {
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
