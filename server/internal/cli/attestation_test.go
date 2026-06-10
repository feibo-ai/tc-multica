package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// loadFixtureBundles reads testdata/fixture-attestations.json (a real GitHub
// /attestations API response) and returns the raw JSON of each
// .attestations[].bundle. These are real keyless OIDC attestation bundles, so
// they exercise the full offline crypto path.
func loadFixtureBundles(t *testing.T) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture-attestations.json"))
	if err != nil {
		t.Fatalf("read fixture-attestations.json: %v", err)
	}
	var parsed githubAttestationsResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal fixture attestations: %v", err)
	}
	bundles := make([][]byte, 0, len(parsed.Attestations))
	for _, a := range parsed.Attestations {
		if len(a.Bundle) == 0 {
			continue
		}
		bundles = append(bundles, []byte(a.Bundle))
	}
	if len(bundles) == 0 {
		t.Fatal("no bundles extracted from fixture-attestations.json")
	}
	return bundles
}

func loadFixtureArtifact(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fixture-artifact"))
	if err != nil {
		t.Fatalf("read fixture-artifact: %v", err)
	}
	return data
}

// T1 — the real fixture verifies. This is the keystone: it proves the embedded
// trusted root, verifier option combination, certificate identity policy, and
// digest binding are all correct against a genuine GitHub attestation.
func TestVerifyProvenanceBundles_RealFixturePasses(t *testing.T) {
	artifact := loadFixtureArtifact(t)
	bundles := loadFixtureBundles(t)

	if err := verifyProvenanceBundles(artifact, bundles); err != nil {
		t.Fatalf("expected real fixture to verify, got error: %v", err)
	}
}

// T2 — a tampered artifact (digest changes) must be rejected. The bundles are
// genuine but no longer bind to these bytes, so the digest policy fails closed.
func TestVerifyProvenanceBundles_TamperedArtifactRejected(t *testing.T) {
	artifact := loadFixtureArtifact(t)
	bundles := loadFixtureBundles(t)

	tampered := make([]byte, len(artifact)+1)
	copy(tampered, artifact)
	tampered[len(artifact)] = 0x00 // append one byte → digest differs

	if err := verifyProvenanceBundles(tampered, bundles); err == nil {
		t.Fatal("expected tampered artifact to be rejected, got nil error")
	}
}

// T3 — an empty bundle list must be rejected (fail-closed: no attestation =
// no verification = no update).
func TestVerifyProvenanceBundles_EmptyBundlesRejected(t *testing.T) {
	artifact := loadFixtureArtifact(t)

	if err := verifyProvenanceBundles(artifact, nil); err == nil {
		t.Fatal("expected nil bundles to be rejected, got nil error")
	}
	if err := verifyProvenanceBundles(artifact, [][]byte{}); err == nil {
		t.Fatal("expected empty bundles to be rejected, got nil error")
	}
}

// T4 — a wrong SAN identity (different repo) must be rejected. This proves the
// triple-bound assertion (repo/workflow/ref) is a real cryptographic check,
// not a rubber stamp: swapping the repo in the SAN regex causes the genuine,
// otherwise-valid fixture to fail. The exported attestationSANRegex constant is
// NOT modified — we inject an alternate regex via the internal helper.
func TestVerifyProvenanceBundles_WrongIdentityRejected(t *testing.T) {
	artifact := loadFixtureArtifact(t)
	bundles := loadFixtureBundles(t)

	// Same shape as the real regex but pointed at the upstream repo. The
	// fixture is signed by feibo-ai/tc-multica, so this must NOT match.
	wrongSAN := `^https://github\.com/multica-ai/multica/\.github/workflows/release\.yml@refs/tags/v[0-9][0-9A-Za-z.\-]*$`

	if err := verifyProvenanceBundlesWithSAN(artifact, bundles, wrongSAN); err == nil {
		t.Fatal("expected wrong-repo SAN identity to be rejected, got nil error")
	}

	// Sanity: the real SAN regex must still pass with the same inputs, so the
	// rejection above is attributable to identity, not a broken setup.
	if err := verifyProvenanceBundlesWithSAN(artifact, bundles, attestationSANRegex); err != nil {
		t.Fatalf("control: real SAN regex should pass, got: %v", err)
	}
}
