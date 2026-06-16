package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TEA-115 daemon-side mirror-base rewrite tests.
//
// These tests assert the daemon redirects ONLY its three binary-anchor fetch
// points to the verified mirror, that offline verification is byte-for-byte
// equivalent through the mirror vs. direct GitHub (INV-16), that the trust
// anchor is NOT relocated by the mirror (wrong-repo SAN still fails closed),
// and that the skill second anchor is NEVER polluted by the mirror base
// (INV-18 skill-anchor isolation).
// ---------------------------------------------------------------------------

// setMirrorForTest sets the package-level mirror base for the duration of one
// test and restores it (to empty / GitHub-pinned) afterward. SetUpdateMirrorBase
// mutates a package global, so callers must not run these in parallel.
func setMirrorForTest(t *testing.T, base string) {
	t.Helper()
	prev := updateMirrorBase
	SetUpdateMirrorBase(base)
	t.Cleanup(func() { updateMirrorBase = prev })
}

// recordingServer is a tiny test double that records every request path it
// receives and serves canned responses keyed by path prefix. It stands in for
// the multica server's mirror download endpoints.
type recordingServer struct {
	mu     sync.Mutex
	hits   []string
	bundle []byte // fixture-attestations.json bytes, served verbatim for /attestations/...
	rel    []byte // release metadata JSON, served for /releases/...
}

func (rs *recordingServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.hits = append(rs.hits, r.URL.Path)
		rs.mu.Unlock()

		switch {
		case strings.Contains(r.URL.Path, "/attestations/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(rs.bundle)
		case strings.Contains(r.URL.Path, "/releases/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(rs.rel)
		default:
			http.Error(w, "unexpected mirror path: "+r.URL.Path, http.StatusNotFound)
		}
	}
}

func (rs *recordingServer) paths() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]string, len(rs.hits))
	copy(out, rs.hits)
	return out
}

// (a) The rewritable fetch points hit the mirror host, not api.github.com.
//
// We point the mirror at an httptest server and assert fetchReleaseByTag /
// FetchLatestRelease land on the mirror's /releases/... paths. attestation
// fetch host coverage is exercised by the fixture-passthrough test below.
func TestMirrorBase_ReleaseFetchPointsHitMirror(t *testing.T) {
	rel, _ := json.Marshal(GitHubRelease{
		TagName: "v0.4.22",
		Assets:  []GitHubReleaseAsset{{Name: "checksums.txt", BrowserDownloadURL: "https://github.com/x/y/releases/download/v0.4.22/checksums.txt"}},
	})
	rs := &recordingServer{rel: rel}
	srv := httptest.NewServer(rs.handler())
	t.Cleanup(srv.Close)

	setMirrorForTest(t, srv.URL+"/api/cli/mirror")

	// The base helper must resolve to the mirror, not GitHub.
	if got := releasesBaseURL(); got != srv.URL+"/api/cli/mirror/releases" {
		t.Fatalf("releasesBaseURL() = %q, want mirror-rooted /releases", got)
	}
	if strings.Contains(releasesBaseURL(), "api.github.com") {
		t.Fatalf("releasesBaseURL() must NOT contain api.github.com when mirror is set: %q", releasesBaseURL())
	}

	if _, err := fetchReleaseByTag("v0.4.22"); err != nil {
		t.Fatalf("fetchReleaseByTag via mirror: %v", err)
	}
	if _, err := FetchLatestRelease(); err != nil {
		t.Fatalf("FetchLatestRelease via mirror: %v", err)
	}

	paths := rs.paths()
	if len(paths) != 2 {
		t.Fatalf("expected 2 mirror hits, got %d: %v", len(paths), paths)
	}
	wantTag := "/api/cli/mirror/releases/tags/v0.4.22"
	wantLatest := "/api/cli/mirror/releases/latest"
	if paths[0] != wantTag {
		t.Errorf("fetchReleaseByTag hit %q, want %q", paths[0], wantTag)
	}
	if paths[1] != wantLatest {
		t.Errorf("FetchLatestRelease hit %q, want %q", paths[1], wantLatest)
	}
}

// (a') No mirror configured → fetch points fall back to the GitHub-pinned
// literals (bootstrap / old-machine path preserved).
func TestMirrorBase_EmptyFallsBackToGitHub(t *testing.T) {
	setMirrorForTest(t, "")

	if got := releasesBaseURL(); got != "https://api.github.com/repos/feibo-ai/tc-multica/releases" {
		t.Fatalf("releasesBaseURL() fallback = %q, want GitHub binary-anchor literal", got)
	}
	if got := attestationsBaseURL(); got != attestationsAPIBase {
		t.Fatalf("attestationsBaseURL() fallback = %q, want attestationsAPIBase const %q", got, attestationsAPIBase)
	}
}

// (b) Byte-for-byte passthrough equivalence (INV-16): VerifyArtifactAttestation
// run through the mirror path produces the same PASS as it would direct from
// GitHub, because the mirror serves the GitHub attestations response verbatim
// and the offline verifier only sees bytes. We assert (1) the attestation fetch
// lands on the mirror's content-addressed /attestations/sha256:<digest> path,
// and (2) verification passes with the real fixture.
func TestMirrorBase_VerifyArtifactAttestationThroughMirror(t *testing.T) {
	artifact := loadFixtureArtifact(t)
	bundleJSON := loadFixtureAttestationsRaw(t)

	rs := &recordingServer{bundle: bundleJSON}
	srv := httptest.NewServer(rs.handler())
	t.Cleanup(srv.Close)

	setMirrorForTest(t, srv.URL+"/api/cli/mirror")

	if err := VerifyArtifactAttestation(artifact, 5*time.Second); err != nil {
		t.Fatalf("VerifyArtifactAttestation through mirror should pass (byte-for-byte passthrough), got: %v", err)
	}

	// The fetch must have hit the mirror's content-addressed attestation path,
	// keyed by the daemon-computed digest (NOT a tag-derived key).
	sum := sha256.Sum256(artifact)
	wantDigest := hex.EncodeToString(sum[:])
	wantPath := "/api/cli/mirror/attestations/sha256:" + wantDigest

	paths := rs.paths()
	if len(paths) != 1 {
		t.Fatalf("expected exactly 1 mirror attestation hit, got %d: %v", len(paths), paths)
	}
	if paths[0] != wantPath {
		t.Fatalf("attestation fetch hit %q, want content-addressed %q", paths[0], wantPath)
	}
	for _, p := range paths {
		if strings.Contains(p, "github.com") {
			t.Fatalf("attestation fetch must not touch GitHub when mirror is set: %q", p)
		}
	}
}

// (c) The trust anchor is NOT relocated by the mirror: a wrong-repo SAN still
// fails closed even when bytes come from the mirror. This proves the mirror is
// a dumb byte cache — swapping the host does not swap the embedded-root + SAN
// triad. We run the SAME mirror-served bundle through the internal SAN-injection
// helper (the daemon never modifies attestationSANRegex itself).
func TestMirrorBase_WrongRepoSANStillFailsClosedThroughMirror(t *testing.T) {
	artifact := loadFixtureArtifact(t)
	bundleJSON := loadFixtureAttestationsRaw(t)

	rs := &recordingServer{bundle: bundleJSON}
	srv := httptest.NewServer(rs.handler())
	t.Cleanup(srv.Close)

	setMirrorForTest(t, srv.URL+"/api/cli/mirror")

	// Pull the bundles through the mirror, then verify with a wrong-repo regex.
	sum := sha256.Sum256(artifact)
	bundles, err := fetchAttestationBundles(hex.EncodeToString(sum[:]), 5*time.Second)
	if err != nil {
		t.Fatalf("fetch bundles via mirror: %v", err)
	}

	wrongSAN := `^https://github\.com/multica-ai/multica/\.github/workflows/release\.yml@refs/tags/v[0-9][0-9A-Za-z.\-]*$`
	if err := verifyProvenanceBundlesWithSAN(artifact, bundles, wrongSAN); err == nil {
		t.Fatal("wrong-repo SAN must fail closed even through the mirror (trust anchor not relocated)")
	}

	// Control: the real binary-anchor SAN still passes on the same mirror bytes,
	// so the rejection above is attributable to identity, not transport.
	if err := verifyProvenanceBundlesWithSAN(artifact, bundles, attestationSANRegex); err != nil {
		t.Fatalf("control: real SAN regex should pass on mirror bytes, got: %v", err)
	}
}

// (d) The skill second anchor (⑩c) is NEVER polluted by the mirror base. Even
// with a mirror configured, the skill attestation path must stay pinned to the
// GitHub-pinned skillAttestationsAPIBase const (INV-18 skill-anchor isolation):
// VerifySkillBundleAttestation passes skillAttestationsAPIBase directly to
// fetchAttestationBundlesFor and never goes through the mirror-rewritable
// attestationsBaseURL() helper. This is asserted structurally (no network), so
// it is hermetic and exact: the skill const must stay the team-context GitHub
// literal and must be disjoint from the mirror-rewritten binary-anchor base.
func TestMirrorBase_SkillAnchorNotPolluted(t *testing.T) {
	const mirror = "https://mirror.example.internal/api/cli/mirror"
	setMirrorForTest(t, mirror)

	// The binary anchor IS rewritten to the mirror...
	if !strings.HasPrefix(attestationsBaseURL(), mirror) {
		t.Fatalf("binary anchor should be mirror-rewritten, got %q", attestationsBaseURL())
	}
	// ...but the skill anchor const is untouched and stays GitHub-pinned.
	if skillAttestationsAPIBase != "https://api.github.com/repos/feibo-ai/team-context/attestations/" {
		t.Fatalf("skillAttestationsAPIBase must stay GitHub-pinned const, got %q", skillAttestationsAPIBase)
	}
	if strings.HasPrefix(skillAttestationsAPIBase, mirror) || strings.Contains(skillAttestationsAPIBase, "/api/cli/mirror") {
		t.Fatalf("skillAttestationsAPIBase must NOT be rewritten to the mirror: %q", skillAttestationsAPIBase)
	}
	// The skill releases anchor (skillsync.go:39) likewise stays GitHub-pinned.
	if skillReleasesAPIURL != "https://api.github.com/repos/feibo-ai/team-context/releases" {
		t.Fatalf("skillReleasesAPIURL must stay GitHub-pinned const, got %q", skillReleasesAPIURL)
	}
	// And the binary-anchor mirror base must NOT point at the team-context repo
	// (a mis-wired rewrite that relocated the skill anchor would show up here).
	if strings.Contains(attestationsBaseURL(), "team-context") {
		t.Fatalf("binary anchor base must not reference team-context: %q", attestationsBaseURL())
	}
}

// loadFixtureAttestationsRaw returns the raw bytes of fixture-attestations.json
// (a genuine GitHub /attestations API response). The mirror serves these
// verbatim, so the daemon receives a byte-identical body and its verifier passes
// exactly as it would直连 GitHub (INV-18 dumb passthrough, no repackaging).
func loadFixtureAttestationsRaw(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture-attestations.json"))
	if err != nil {
		t.Fatalf("read fixture-attestations.json: %v", err)
	}
	return raw
}
