package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/middleware"
)

// ---------------------------------------------------------------------------
// TEA-115 verified-mirror server tests.
//
// These are SERVER cache-correctness tests, NOT daemon behaviour tests
// (BLOCKER-1): they assert the server's three-bucket cache / single-flight /
// SSRF whitelist / atomic revocation unit, never that the daemon distinguishes
// 404 from 504 (it does not — INV-16).
// ---------------------------------------------------------------------------

// fakeOrigin is an injectable mirrorOriginFetcher that counts upstream calls and
// can inject 404 (cli.MirrorNotFound) / 504 (plain error) / specific bytes.
type fakeOrigin struct {
	mu sync.Mutex

	releaseByTag map[string]*cli.GitHubRelease
	latest       *cli.GitHubRelease
	attestations map[string][]byte // digestHex -> bundle bytes
	assets       map[string][]byte // downloadURL -> bytes

	// per-method error injection. When set, the corresponding fetch returns
	// this error instead of a value (use cli.MirrorNotFound for 404,
	// errTransient for 504/timeout).
	errReleaseByTag error
	errLatest       error
	errAttestation  error
	errAsset        error

	callsReleaseByTag int32
	callsLatest       int32
	callsAttestation  int32
	callsAsset        int32

	// slowAsset, when set, blocks the asset fetch on this channel so concurrent
	// single-flight behaviour can be exercised deterministically.
	slowAsset chan struct{}
}

var errTransient = &transientErr{}

type transientErr struct{}

func (*transientErr) Error() string { return "simulated upstream 504/timeout" }

func newFakeOrigin() *fakeOrigin {
	return &fakeOrigin{
		releaseByTag: map[string]*cli.GitHubRelease{},
		attestations: map[string][]byte{},
		assets:       map[string][]byte{},
	}
}

func (f *fakeOrigin) fetchReleaseByTag(tag string) (*cli.GitHubRelease, error) {
	atomic.AddInt32(&f.callsReleaseByTag, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errReleaseByTag != nil {
		return nil, f.errReleaseByTag
	}
	rel, ok := f.releaseByTag[tag]
	if !ok {
		return nil, &cli.MirrorNotFound{What: "release " + tag}
	}
	return rel, nil
}

func (f *fakeOrigin) fetchLatestRelease() (*cli.GitHubRelease, error) {
	atomic.AddInt32(&f.callsLatest, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errLatest != nil {
		return nil, f.errLatest
	}
	if f.latest == nil {
		return nil, &cli.MirrorNotFound{What: "latest"}
	}
	return f.latest, nil
}

func (f *fakeOrigin) fetchAttestation(digestHex string) ([]byte, error) {
	atomic.AddInt32(&f.callsAttestation, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errAttestation != nil {
		return nil, f.errAttestation
	}
	b, ok := f.attestations[digestHex]
	if !ok {
		return nil, &cli.MirrorNotFound{What: "attestation " + digestHex}
	}
	return b, nil
}

func (f *fakeOrigin) fetchAsset(downloadURL string) ([]byte, error) {
	atomic.AddInt32(&f.callsAsset, 1)
	if f.slowAsset != nil {
		<-f.slowAsset
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errAsset != nil {
		return nil, f.errAsset
	}
	b, ok := f.assets[downloadURL]
	if !ok {
		return nil, &cli.MirrorNotFound{What: "asset " + downloadURL}
	}
	return b, nil
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func newMirrorTestHandler(origin mirrorOriginFetcher) *Handler {
	return &Handler{
		MirrorCache: newMirrorCache(origin),
		cfg:         Config{PublicURL: "https://mirror.example"},
	}
}

func mirrorReq(method, target string, params map[string]string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// --- INV-21b: MirrorReleaseByTag rewrites asset URLs to the mirror -----------

func TestMirrorReleaseByTag_RewritesAssetURLs(t *testing.T) {
	origin := newFakeOrigin()
	origin.releaseByTag["v0.4.22"] = &cli.GitHubRelease{
		TagName: "v0.4.22",
		Assets: []cli.GitHubReleaseAsset{
			{Name: "multica-cli-0.4.22-linux-amd64.tar.gz", BrowserDownloadURL: "https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/multica-cli-0.4.22-linux-amd64.tar.gz"},
			{Name: "checksums.txt", BrowserDownloadURL: "https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/checksums.txt"},
		},
	}
	h := newMirrorTestHandler(origin)

	w := httptest.NewRecorder()
	h.MirrorReleaseByTag(w, mirrorReq("GET", "/api/cli/mirror/releases/tags/v0.4.22", map[string]string{"tag": "v0.4.22"}))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var rel cli.GitHubRelease
	if err := json.Unmarshal(w.Body.Bytes(), &rel); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, a := range rel.Assets {
		want := "https://mirror.example/api/cli/mirror/assets/v0.4.22/" + a.Name
		if a.BrowserDownloadURL != want {
			t.Errorf("asset %q URL = %q, want %q", a.Name, a.BrowserDownloadURL, want)
		}
	}
}

// --- INV-21a: MirrorAsset SSRF whitelist -------------------------------------

func TestMirrorAsset_SSRFWhitelist_RejectsUnknownAsset(t *testing.T) {
	origin := newFakeOrigin()
	origin.releaseByTag["v0.4.22"] = &cli.GitHubRelease{
		TagName: "v0.4.22",
		Assets: []cli.GitHubReleaseAsset{
			{Name: "checksums.txt", BrowserDownloadURL: "https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/checksums.txt"},
		},
	}
	h := newMirrorTestHandler(origin)

	// A forged asset name not in the authoritative set must 404 and NEVER
	// trigger an asset origin fetch.
	w := httptest.NewRecorder()
	h.MirrorAsset(w, mirrorReq("GET", "/api/cli/mirror/assets/v0.4.22/etc-passwd",
		map[string]string{"tag": "v0.4.22", "asset": "etc-passwd"}))

	if w.Code != http.StatusNotFound {
		t.Fatalf("forged asset: expected 404, got %d", w.Code)
	}
	if got := atomic.LoadInt32(&origin.callsAsset); got != 0 {
		t.Fatalf("forged asset must NOT origin-fetch; callsAsset=%d", got)
	}
}

func TestMirrorAsset_SSRFWhitelist_RejectsNonGitHubHost(t *testing.T) {
	origin := newFakeOrigin()
	// Authoritative metadata that (maliciously / by accident) carries a
	// non-GitHub host. Even though the asset name matches, the host whitelist
	// (INV-21 pinned host) must reject it without origin-fetching.
	origin.releaseByTag["v0.4.22"] = &cli.GitHubRelease{
		TagName: "v0.4.22",
		Assets: []cli.GitHubReleaseAsset{
			{Name: "checksums.txt", BrowserDownloadURL: "http://169.254.169.254/latest/meta-data/checksums.txt"},
		},
	}
	h := newMirrorTestHandler(origin)

	w := httptest.NewRecorder()
	h.MirrorAsset(w, mirrorReq("GET", "/api/cli/mirror/assets/v0.4.22/checksums.txt",
		map[string]string{"tag": "v0.4.22", "asset": "checksums.txt"}))

	if w.Code != http.StatusNotFound {
		t.Fatalf("non-GitHub host: expected 404, got %d", w.Code)
	}
	if got := atomic.LoadInt32(&origin.callsAsset); got != 0 {
		t.Fatalf("non-GitHub host must NOT origin-fetch; callsAsset=%d", got)
	}
}

func TestMirrorAsset_ServesWhitelistedAsset(t *testing.T) {
	origin := newFakeOrigin()
	url := "https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/checksums.txt"
	body := []byte("deadbeef  checksums.txt\n")
	origin.releaseByTag["v0.4.22"] = &cli.GitHubRelease{
		TagName: "v0.4.22",
		Assets:  []cli.GitHubReleaseAsset{{Name: "checksums.txt", BrowserDownloadURL: url}},
	}
	origin.assets[url] = body
	h := newMirrorTestHandler(origin)

	w := httptest.NewRecorder()
	h.MirrorAsset(w, mirrorReq("GET", "/api/cli/mirror/assets/v0.4.22/checksums.txt",
		map[string]string{"tag": "v0.4.22", "asset": "checksums.txt"}))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != string(body) {
		t.Fatalf("body mismatch: got %q", w.Body.String())
	}
}

// --- single-flight: N daemons -> 1 upstream fetch ---------------------------

func TestMirrorAsset_ImmutableCache_OneUpstreamFetch(t *testing.T) {
	origin := newFakeOrigin()
	url := "https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/checksums.txt"
	origin.releaseByTag["v0.4.22"] = &cli.GitHubRelease{
		TagName: "v0.4.22",
		Assets:  []cli.GitHubReleaseAsset{{Name: "checksums.txt", BrowserDownloadURL: url}},
	}
	origin.assets[url] = []byte("manifest")
	h := newMirrorTestHandler(origin)

	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		h.MirrorAsset(w, mirrorReq("GET", "/api/cli/mirror/assets/v0.4.22/checksums.txt",
			map[string]string{"tag": "v0.4.22", "asset": "checksums.txt"}))
		if w.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d", i, w.Code)
		}
	}
	// 5 daemon requests for an immutable artifact => exactly 1 upstream asset
	// fetch (subsequent hits served from the immutable bucket).
	if got := atomic.LoadInt32(&origin.callsAsset); got != 1 {
		t.Fatalf("immutable bucket: expected 1 upstream asset fetch, got %d", got)
	}
}

func TestMirrorCache_SingleFlight_ConcurrentColdMissOneFetch(t *testing.T) {
	origin := newFakeOrigin()
	origin.slowAsset = make(chan struct{})
	url := "https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/x.tar.gz"
	origin.assets[url] = []byte("archive")
	c := newMirrorCache(origin)

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.getAsset("v0.4.22", "x.tar.gz", url)
		}()
	}
	// Let all goroutines reach the blocked origin fetch, then release.
	time.Sleep(50 * time.Millisecond)
	close(origin.slowAsset)
	wg.Wait()

	if got := atomic.LoadInt32(&origin.callsAsset); got != 1 {
		t.Fatalf("single-flight: %d concurrent cold-miss should collapse to 1 upstream fetch, got %d", n, got)
	}
}

// --- INV-18: attestation 404 vs 504 server cache correctness -----------------

func TestMirrorAttestations_404CacheableNegative(t *testing.T) {
	origin := newFakeOrigin()
	// digest absent in fake => fetchAttestation returns MirrorNotFound (404).
	h := newMirrorTestHandler(origin)
	dg := digestOf([]byte("nonexistent"))

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		h.MirrorAttestations(w, mirrorReq("GET", "/api/cli/mirror/attestations/sha256:"+dg,
			map[string]string{"digest": "sha256:" + dg}))
		if w.Code != http.StatusNotFound {
			t.Fatalf("call %d: expected 404, got %d", i, w.Code)
		}
	}
	// 404 is an authoritative negative; the short-TTL negative cache means the
	// 3 calls cost at most 1 upstream attestation fetch.
	if got := atomic.LoadInt32(&origin.callsAttestation); got != 1 {
		t.Fatalf("404 negative should be cached: expected 1 upstream fetch, got %d", got)
	}
}

func TestMirrorAttestations_504NotCached_ReorigineachTime(t *testing.T) {
	origin := newFakeOrigin()
	origin.errAttestation = errTransient // simulate upstream 504/timeout
	h := newMirrorTestHandler(origin)
	dg := digestOf([]byte("artifact"))

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		h.MirrorAttestations(w, mirrorReq("GET", "/api/cli/mirror/attestations/sha256:"+dg,
			map[string]string{"digest": "sha256:" + dg}))
		// Transient upstream error => 502 to the daemon (which treats any
		// non-200 uniformly, INV-16). NOT cached.
		if w.Code != http.StatusBadGateway {
			t.Fatalf("call %d: expected 502, got %d", i, w.Code)
		}
	}
	// A 504/timeout must NEVER poison the cache: every request re-origins.
	if got := atomic.LoadInt32(&origin.callsAttestation); got != 3 {
		t.Fatalf("504 must not be cached: expected 3 upstream fetches, got %d", got)
	}
}

func TestMirrorAttestations_RejectsMalformedDigest_NoOrigin(t *testing.T) {
	origin := newFakeOrigin()
	h := newMirrorTestHandler(origin)

	for _, bad := range []string{"notsha", "sha256:tooshort", "sha512:" + digestOf([]byte("x")), "../../etc"} {
		w := httptest.NewRecorder()
		h.MirrorAttestations(w, mirrorReq("GET", "/api/cli/mirror/attestations/"+bad,
			map[string]string{"digest": bad}))
		if w.Code != http.StatusNotFound {
			t.Errorf("malformed digest %q: expected 404, got %d", bad, w.Code)
		}
	}
	if got := atomic.LoadInt32(&origin.callsAttestation); got != 0 {
		t.Fatalf("malformed digest must NOT origin-fetch; callsAttestation=%d", got)
	}
}

func TestMirrorAttestations_DumbBytePassThrough(t *testing.T) {
	origin := newFakeOrigin()
	raw := []byte(`{"attestations":[{"bundle":{"foo":"bar"}}]}`)
	dg := digestOf([]byte("artifact-bytes"))
	origin.attestations[dg] = raw
	h := newMirrorTestHandler(origin)

	w := httptest.NewRecorder()
	h.MirrorAttestations(w, mirrorReq("GET", "/api/cli/mirror/attestations/sha256:"+dg,
		map[string]string{"digest": "sha256:" + dg}))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	// Byte-for-byte identical to the GitHub original (INV-18: no repackaging).
	if w.Body.String() != string(raw) {
		t.Fatalf("attestation bytes not byte-identical:\n got %q\nwant %q", w.Body.String(), string(raw))
	}
}

// --- INV-20 / BLOCKER-2: revocations atomic cache unit -----------------------

func TestMirrorRevocations_AtomicUnit_DigestMatchesServedBytes(t *testing.T) {
	origin := newFakeOrigin()
	tag := "v0.4.22"
	revURL := "https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/revocations.json"
	revBytes := []byte(`{"revoked":[],"counter":7}`)
	dR := digestOf(revBytes)
	attBytes := []byte(`{"attestations":[{"bundle":{"rev":"att"}}]}`)

	origin.releaseByTag[tag] = &cli.GitHubRelease{
		TagName: tag,
		Assets:  []cli.GitHubReleaseAsset{{Name: cli.RevocationsAssetName, BrowserDownloadURL: revURL}},
	}
	origin.assets[revURL] = revBytes
	origin.attestations[dR] = attBytes
	h := newMirrorTestHandler(origin)

	// 1) Daemon downloads revocations.json content via MirrorAsset.
	wc := httptest.NewRecorder()
	h.MirrorAsset(wc, mirrorReq("GET", "/api/cli/mirror/assets/"+tag+"/"+cli.RevocationsAssetName,
		map[string]string{"tag": tag, "asset": cli.RevocationsAssetName}))
	if wc.Code != http.StatusOK {
		t.Fatalf("rev content: expected 200, got %d: %s", wc.Code, wc.Body.String())
	}
	served := wc.Body.Bytes()
	dServed := digestOf(served)
	if dServed != dR {
		t.Fatalf("served rev bytes digest %s != expected D_R %s", dServed, dR)
	}

	// 2) Daemon then requests the attestation for sha256(served bytes). It MUST
	// hit the SAME-batch attestation bundle (BLOCKER-2: no digest mismatch →
	// no吊销门 fail-open).
	wa := httptest.NewRecorder()
	h.MirrorAttestations(wa, mirrorReq("GET", "/api/cli/mirror/attestations/sha256:"+dServed,
		map[string]string{"digest": "sha256:" + dServed}))
	if wa.Code != http.StatusOK {
		t.Fatalf("rev attestation: expected 200, got %d: %s", wa.Code, wa.Body.String())
	}
	if wa.Body.String() != string(attBytes) {
		t.Fatalf("rev attestation bytes mismatch:\n got %q\nwant %q", wa.Body.String(), string(attBytes))
	}
}

func TestMirrorRevocations_ServeStale_DigestStaysConsistent(t *testing.T) {
	origin := newFakeOrigin()
	tag := "v0.4.22"
	revURL := "https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/revocations.json"
	oldBytes := []byte(`{"revoked":[],"counter":1}`)
	dOld := digestOf(oldBytes)
	attOld := []byte(`{"attestations":[{"bundle":{"rev":"old"}}]}`)

	origin.releaseByTag[tag] = &cli.GitHubRelease{
		TagName: tag,
		Assets:  []cli.GitHubReleaseAsset{{Name: cli.RevocationsAssetName, BrowserDownloadURL: revURL}},
	}
	origin.assets[revURL] = oldBytes
	origin.attestations[dOld] = attOld

	c := newMirrorCache(origin)
	// Force the revocation unit into a known-good but expired state so the next
	// read takes the serve-stale path on an injected transient origin error.
	unit, err := c.getRevocationUnit(tag, cli.RevocationsAssetName, revURL)
	if err != nil {
		t.Fatalf("warm rev unit: %v", err)
	}
	if unit.digestHex != dOld {
		t.Fatalf("warm unit digest %s != %s", unit.digestHex, dOld)
	}
	// Expire it and make the origin fail transiently.
	c.mu.Lock()
	c.revocation.expires = time.Now().Add(-time.Second)
	c.mu.Unlock()
	origin.mu.Lock()
	origin.errAsset = errTransient
	origin.mu.Unlock()

	stale, err := c.getRevocationUnit(tag, cli.RevocationsAssetName, revURL)
	if err != nil {
		t.Fatalf("serve-stale should not error: %v", err)
	}
	// Serve-stale: the unit served is still D_old, and its bound attestation is
	// still D_old's bundle — content and attestation stay the SAME atomic unit
	// (never a stale-content / fresh-attestation split).
	if stale.digestHex != dOld {
		t.Fatalf("serve-stale unit digest %s != D_old %s", stale.digestHex, dOld)
	}
	if digestOf(stale.content) != dOld {
		t.Fatalf("serve-stale content digest != D_old")
	}
	if string(stale.attBytes) != string(attOld) {
		t.Fatalf("serve-stale attestation != D_old's bundle")
	}
}

// --- INV-20: /latest serve-stale on upstream failure ------------------------

func TestMirrorLatest_ServeStaleOnUpstreamFail(t *testing.T) {
	origin := newFakeOrigin()
	origin.latest = &cli.GitHubRelease{TagName: "v0.4.22"}
	c := newMirrorCache(origin)

	rel, err := c.getLatest()
	if err != nil || rel.TagName != "v0.4.22" {
		t.Fatalf("warm latest: rel=%v err=%v", rel, err)
	}
	// Expire + inject transient failure => serve-stale.
	c.mu.Lock()
	c.latest.expires = time.Now().Add(-time.Second)
	c.mu.Unlock()
	origin.mu.Lock()
	origin.errLatest = errTransient
	origin.mu.Unlock()

	rel2, err := c.getLatest()
	if err != nil {
		t.Fatalf("serve-stale latest should not error: %v", err)
	}
	if rel2.TagName != "v0.4.22" {
		t.Fatalf("serve-stale latest tag = %q, want v0.4.22", rel2.TagName)
	}
}

// --- finding 1: getReleaseByTag resident cache — N daemons → 1 origin --------

// The release-metadata path is hit on EVERY MirrorReleaseByTag AND every
// MirrorAsset SSRF-whitelist resolution. Before the resident cache, single-flight
// only collapsed overlapping requests, so a fleet spread over time re-origined
// the metadata N times (callsReleaseByTag == N) — the herd moved from the daemons
// to the server while GitHub's metadata API stayed punched through. These assert
// the previously-missing fold: N requests → callsReleaseByTag == 1.

func TestMirrorReleaseByTag_MetadataCache_OneUpstreamFetch(t *testing.T) {
	origin := newFakeOrigin()
	origin.releaseByTag["v0.4.22"] = &cli.GitHubRelease{
		TagName: "v0.4.22",
		Assets:  []cli.GitHubReleaseAsset{{Name: "checksums.txt", BrowserDownloadURL: "https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/checksums.txt"}},
	}
	h := newMirrorTestHandler(origin)

	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		h.MirrorReleaseByTag(w, mirrorReq("GET", "/api/cli/mirror/releases/tags/v0.4.22", map[string]string{"tag": "v0.4.22"}))
		if w.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d", i, w.Code)
		}
	}
	if got := atomic.LoadInt32(&origin.callsReleaseByTag); got != 1 {
		t.Fatalf("metadata resident cache: 5 requests for the same tag should cost 1 upstream metadata fetch, got %d", got)
	}
}

func TestMirrorAsset_MetadataCache_SharedAcrossAssets_OneUpstreamFetch(t *testing.T) {
	origin := newFakeOrigin()
	archiveURL := "https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/multica.tar.gz"
	sumsURL := "https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/checksums.txt"
	origin.releaseByTag["v0.4.22"] = &cli.GitHubRelease{
		TagName: "v0.4.22",
		Assets: []cli.GitHubReleaseAsset{
			{Name: "multica.tar.gz", BrowserDownloadURL: archiveURL},
			{Name: "checksums.txt", BrowserDownloadURL: sumsURL},
		},
	}
	origin.assets[archiveURL] = []byte("archive")
	origin.assets[sumsURL] = []byte("sums")
	h := newMirrorTestHandler(origin)

	// 5 requests across two different assets of the SAME tag → the SSRF-whitelist
	// metadata resolution must hit the resident cache, costing 1 metadata fetch.
	for i := 0; i < 5; i++ {
		for _, name := range []string{"multica.tar.gz", "checksums.txt"} {
			w := httptest.NewRecorder()
			h.MirrorAsset(w, mirrorReq("GET", "/api/cli/mirror/assets/v0.4.22/"+name,
				map[string]string{"tag": "v0.4.22", "asset": name}))
			if w.Code != http.StatusOK {
				t.Fatalf("asset %s call %d: expected 200, got %d", name, i, w.Code)
			}
		}
	}
	if got := atomic.LoadInt32(&origin.callsReleaseByTag); got != 1 {
		t.Fatalf("metadata resident cache: 10 asset requests on one tag should cost 1 metadata fetch, got %d", got)
	}
}

func TestMirrorReleaseByTag_404NegativeCache_OneUpstreamFetch(t *testing.T) {
	origin := newFakeOrigin() // tag absent → fetchReleaseByTag returns MirrorNotFound (404)
	h := newMirrorTestHandler(origin)

	for i := 0; i < 4; i++ {
		w := httptest.NewRecorder()
		h.MirrorReleaseByTag(w, mirrorReq("GET", "/api/cli/mirror/releases/tags/v9.9.9", map[string]string{"tag": "v9.9.9"}))
		if w.Code != http.StatusNotFound {
			t.Fatalf("call %d: expected 404, got %d", i, w.Code)
		}
	}
	// An authoritative 404 is a short-TTL cacheable negative: 4 requests cost 1
	// upstream metadata fetch (blunts random-tag probing on the public endpoint).
	if got := atomic.LoadInt32(&origin.callsReleaseByTag); got != 1 {
		t.Fatalf("404 negative should be cached: expected 1 upstream metadata fetch, got %d", got)
	}
}

func TestMirrorReleaseByTag_504NotCached_ReoriginsEachTime(t *testing.T) {
	origin := newFakeOrigin()
	origin.errReleaseByTag = errTransient // simulate upstream 504/timeout
	h := newMirrorTestHandler(origin)

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		h.MirrorReleaseByTag(w, mirrorReq("GET", "/api/cli/mirror/releases/tags/v0.4.22", map[string]string{"tag": "v0.4.22"}))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("call %d: expected 502, got %d", i, w.Code)
		}
	}
	// A 504/timeout must NEVER poison the metadata cache: every request re-origins.
	if got := atomic.LoadInt32(&origin.callsReleaseByTag); got != 3 {
		t.Fatalf("504 must not be cached: expected 3 upstream metadata fetches, got %d", got)
	}
}

func TestMirrorReleaseByTag_MetadataServeStaleOnTransientFail(t *testing.T) {
	origin := newFakeOrigin()
	origin.releaseByTag["v0.4.22"] = &cli.GitHubRelease{TagName: "v0.4.22"}
	c := newMirrorCache(origin)

	rel, err := c.getReleaseByTag("v0.4.22")
	if err != nil || rel.TagName != "v0.4.22" {
		t.Fatalf("warm metadata: rel=%v err=%v", rel, err)
	}
	// Expire it and inject a transient origin error → serve-stale the good value.
	c.mu.Lock()
	c.releaseByTag["v0.4.22"].expires = time.Now().Add(-time.Second)
	c.mu.Unlock()
	origin.mu.Lock()
	origin.errReleaseByTag = errTransient
	origin.mu.Unlock()

	rel2, err := c.getReleaseByTag("v0.4.22")
	if err != nil {
		t.Fatalf("serve-stale metadata should not error: %v", err)
	}
	if rel2.TagName != "v0.4.22" {
		t.Fatalf("serve-stale metadata tag = %q, want v0.4.22", rel2.TagName)
	}
}

// --- final-round BLOCKER: releaseByTag is LRU-bounded (no unbounded-growth OOM)

// The mirror routes are PUBLIC and the tag key comes straight off the URL path,
// so an attacker can probe /releases/tags/<random-unique> — including misses that
// land in the 404 negative cache — to pin entries. Without a bound this grows
// without limit until OOM; rate limiting only throttles rate, not long-term
// residency. These assert the releaseByTag map AND its LRU slice never exceed
// mirrorReleaseTagMaxEntries, for both positive and negative entries, and that a
// fresh tag still caches correctly after eviction.

func TestMirrorReleaseByTag_PositiveCache_LRUBounded(t *testing.T) {
	origin := newFakeOrigin()
	c := newMirrorCache(origin)

	// Insert well past the bound; every tag exists (positive caching).
	total := mirrorReleaseTagMaxEntries + 500
	for i := 0; i < total; i++ {
		tag := "v0.0." + strconv.Itoa(i)
		origin.mu.Lock()
		origin.releaseByTag[tag] = &cli.GitHubRelease{TagName: tag}
		origin.mu.Unlock()
		if _, err := c.getReleaseByTag(tag); err != nil {
			t.Fatalf("getReleaseByTag(%s): %v", tag, err)
		}
	}

	c.mu.Lock()
	mapLen := len(c.releaseByTag)
	lruLen := len(c.releaseByTagLRU)
	c.mu.Unlock()
	if mapLen > mirrorReleaseTagMaxEntries {
		t.Fatalf("releaseByTag map unbounded: %d entries > cap %d", mapLen, mirrorReleaseTagMaxEntries)
	}
	if lruLen > mirrorReleaseTagMaxEntries {
		t.Fatalf("releaseByTagLRU slice unbounded: %d entries > cap %d", lruLen, mirrorReleaseTagMaxEntries)
	}
	if mapLen != lruLen {
		t.Fatalf("map/LRU drift: map=%d lru=%d (must stay in lockstep)", mapLen, lruLen)
	}

	// After eviction the cache is still usable: a brand-new tag caches and serves.
	newTag := "v9.9.9"
	origin.mu.Lock()
	origin.releaseByTag[newTag] = &cli.GitHubRelease{TagName: newTag}
	origin.mu.Unlock()
	rel, err := c.getReleaseByTag(newTag)
	if err != nil || rel.TagName != newTag {
		t.Fatalf("post-eviction re-cache: rel=%v err=%v", rel, err)
	}
	c.mu.Lock()
	stillBounded := len(c.releaseByTag) <= mirrorReleaseTagMaxEntries
	c.mu.Unlock()
	if !stillBounded {
		t.Fatalf("releaseByTag exceeded cap after post-eviction insert")
	}
}

func TestMirrorReleaseByTag_NegativeCache_LRUBounded(t *testing.T) {
	origin := newFakeOrigin() // every tag absent → 404 negative cached
	c := newMirrorCache(origin)

	// Attacker-controlled random tags that all 404: the negatives must be bounded
	// by the SAME LRU, not grow without limit.
	total := mirrorReleaseTagMaxEntries + 500
	for i := 0; i < total; i++ {
		tag := "vbogus." + strconv.Itoa(i)
		if _, err := c.getReleaseByTag(tag); !cli.IsMirrorNotFound(err) {
			t.Fatalf("getReleaseByTag(%s): expected MirrorNotFound, got %v", tag, err)
		}
	}

	c.mu.Lock()
	mapLen := len(c.releaseByTag)
	lruLen := len(c.releaseByTagLRU)
	c.mu.Unlock()
	if mapLen > mirrorReleaseTagMaxEntries {
		t.Fatalf("releaseByTag negative cache unbounded: %d entries > cap %d", mapLen, mirrorReleaseTagMaxEntries)
	}
	if lruLen > mirrorReleaseTagMaxEntries {
		t.Fatalf("releaseByTagLRU slice unbounded under negatives: %d > cap %d", lruLen, mirrorReleaseTagMaxEntries)
	}
	if mapLen != lruLen {
		t.Fatalf("map/LRU drift under negatives: map=%d lru=%d", mapLen, lruLen)
	}
}

// --- finding 2: the 4 public mirror routes are rate-limited (429 over budget) -

// The mirror routes are PUBLIC and origin-fetch from GitHub on a cold miss, so
// they are wrapped with the same shared-storage per-IP fixed-window limiter as
// authRL/FleetRateLimiter (router.go). This exercises that wrapper end-to-end
// over a mirror route path: under budget → 200/served, over budget → 429. It
// skips cleanly when REDIS_TEST_URL is unset (suite convention) since the
// limiter is fail-open with a nil client.
func TestMirrorRoute_RateLimited_429OverBudget(t *testing.T) {
	rdb := newRedisTestClient(t) // skips if REDIS_TEST_URL unset

	origin := newFakeOrigin()
	origin.releaseByTag["v0.4.22"] = &cli.GitHubRelease{TagName: "v0.4.22"}
	h := newMirrorTestHandler(origin)

	const limit = 3
	wrapped := middleware.RateLimit(rdb, limit, time.Minute, nil)(http.HandlerFunc(h.MirrorReleaseByTag))

	mkReq := func() *http.Request {
		req := mirrorReq("GET", "/api/cli/mirror/releases/tags/v0.4.22", map[string]string{"tag": "v0.4.22"})
		req.RemoteAddr = "203.0.113.7:5000"
		return req
	}
	for i := 0; i < limit; i++ {
		w := httptest.NewRecorder()
		wrapped.ServeHTTP(w, mkReq())
		if w.Code != http.StatusOK {
			t.Fatalf("request %d under budget: expected 200, got %d", i+1, w.Code)
		}
	}
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, mkReq())
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("over budget: expected 429, got %d", w.Code)
	}
}

// --- INV-21: host whitelist primitive ---------------------------------------

func TestIsAllowedMirrorOriginHost(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://github.com/feibo-ai/tc-multica/releases/download/v0.4.22/x.tar.gz", true},
		{"https://objects.githubusercontent.com/abc", true},
		{"https://release-assets.githubusercontent.com/x", true},
		{"https://githubusercontent.com/x", true},
		{"http://github.com/x", false},                         // not https
		{"https://api.github.com/repos/x/attestations", false}, // api host not in whitelist
		{"https://evil.com/github.com", false},
		{"https://github.com.evil.com/x", false},
		{"https://169.254.169.254/latest/meta-data", false},
		{"://broken", false},
	}
	for _, tc := range cases {
		if got := cli.IsAllowedMirrorOriginHost(tc.url); got != tc.want {
			t.Errorf("IsAllowedMirrorOriginHost(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}
