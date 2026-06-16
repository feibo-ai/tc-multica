package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TEA-115: server-origin helper structural guards. These never hit the network
// — they assert the input validation (SSRF补强) that runs BEFORE any URL is
// built / fetched.

func TestFetchAttestationBundleBytes_RejectsNon64Hex(t *testing.T) {
	for _, bad := range []string{
		"",
		"sha256:abc",        // contains prefix + too short
		"ABCDEF",            // uppercase + short
		"g" + repeatHex(63), // non-hex char
		repeatHex(63),       // 63 chars
		repeatHex(65),       // 65 chars
	} {
		_, err := FetchAttestationBundleBytesFromGitHub(bad, time.Second)
		if err == nil {
			t.Errorf("digest %q: expected rejection, got nil error", bad)
		}
	}
}

func TestFetchAssetBytes_RejectsNonGitHubHost(t *testing.T) {
	for _, bad := range []string{
		"http://github.com/x",            // not https
		"https://api.github.com/repos/x", // api host not whitelisted
		"https://169.254.169.254/meta",   // internal
		"https://github.com.evil.com/x",  // lookalike
		"https://evil.com/?u=github.com", // unrelated host
	} {
		_, err := FetchAssetBytesFromGitHub(bad, time.Second)
		if err == nil {
			t.Errorf("download URL %q: expected host rejection, got nil error", bad)
		}
	}
}

// TEA-115 finding 3: every server origin http.Client installs mirrorRedirectCheck
// so a 30x Location is re-asserted against the pinned host whitelist on EVERY
// hop (SSRF defense-in-depth, INV-21) — not just the first URL.

func TestIsAllowedMirrorRedirectHost(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		// Asset-download hosts (superset of IsAllowedMirrorOriginHost).
		{"https://github.com/feibo-ai/tc-multica/releases/download/v/x.tar.gz", true},
		{"https://objects.githubusercontent.com/abc", true},
		{"https://release-assets.githubusercontent.com/x", true},
		// Metadata / attestation start hosts (the fetch begins at api.github.com).
		{"https://api.github.com/repos/feibo-ai/tc-multica/attestations/sha256:x", true},
		{"https://codeload.github.com/x", true},
		// Denied: internal, lookalike, non-https, link-shortener.
		{"https://169.254.169.254/latest/meta-data", false},
		{"http://github.com/x", false},
		{"https://github.com.evil.com/x", false},
		{"https://evil.com/github.com", false},
		{"https://bit.ly/x", false},
		{"://broken", false},
	}
	for _, tc := range cases {
		u, _ := url.Parse(tc.url)
		if got := isAllowedMirrorRedirectHost(u); got != tc.want {
			t.Errorf("isAllowedMirrorRedirectHost(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
	if isAllowedMirrorRedirectHost(nil) {
		t.Errorf("isAllowedMirrorRedirectHost(nil) should be false")
	}
}

func TestMirrorRedirectCheck_RejectsNonWhitelistHop(t *testing.T) {
	// A redirect target on an internal host must be refused mid-chain.
	internal, _ := http.NewRequest(http.MethodGet, "https://169.254.169.254/latest/meta-data", nil)
	if err := mirrorRedirectCheck(internal, nil); err == nil {
		t.Fatal("redirect to internal host should be refused")
	}
	// A legitimate github.com → objects.githubusercontent.com hop is allowed.
	asset, _ := http.NewRequest(http.MethodGet, "https://objects.githubusercontent.com/x", nil)
	if err := mirrorRedirectCheck(asset, nil); err != nil {
		t.Fatalf("legitimate githubusercontent redirect should be allowed: %v", err)
	}
	// Too many redirects is refused (loop guard).
	via := make([]*http.Request, mirrorOriginMaxRedirects)
	if err := mirrorRedirectCheck(asset, via); err == nil {
		t.Fatalf("a chain of %d redirects should be refused", mirrorOriginMaxRedirects)
	}
}

// TestMirrorOriginClient_FollowsThenRefusesRedirect drives the SAME redirect
// policy the production clients install (CheckRedirect: mirrorRedirectCheck)
// against a local httptest server that 302s to a non-whitelisted host, proving
// the wiring rejects the hop end-to-end (the error mentions the refused host).
func TestMirrorOriginClient_RefusesRedirectToNonWhitelistHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to an internal metadata endpoint — exactly the SSRF target the
		// per-hop check must block.
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: time.Second, CheckRedirect: mirrorRedirectCheck}
	_, err := client.Get(srv.URL) // httptest host is 127.0.0.1, but we never reach the redirect target
	if err == nil {
		t.Fatal("expected the redirect to a non-whitelisted host to be refused")
	}
	if !strings.Contains(err.Error(), "refusing redirect") {
		t.Fatalf("expected a 'refusing redirect' error, got: %v", err)
	}
}

func TestMirrorNotFound_Sentinel(t *testing.T) {
	base := &MirrorNotFound{What: "x"}
	if !IsMirrorNotFound(base) {
		t.Fatal("IsMirrorNotFound should match a *MirrorNotFound")
	}
	wrapped := errWrap(base)
	if !IsMirrorNotFound(wrapped) {
		t.Fatal("IsMirrorNotFound should match a wrapped *MirrorNotFound")
	}
	if IsMirrorNotFound(errors.New("plain")) {
		t.Fatal("IsMirrorNotFound should NOT match a plain error")
	}
}

func repeatHex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func errWrap(err error) error { return &wrappedErr{err} }

type wrappedErr struct{ inner error }

func (e *wrappedErr) Error() string { return "wrapped: " + e.inner.Error() }
func (e *wrappedErr) Unwrap() error { return e.inner }
