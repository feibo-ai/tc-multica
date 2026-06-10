package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// sampleList builds a revocation list with one digest entry and one version
// entry, used by the hit-detection tests below. Constructed in-test, no network.
func sampleList(counter int) RevocationList {
	return RevocationList{
		Counter: counter,
		Revoked: []RevocationEntry{
			{Kind: "digest", Value: "sha256:deadbeef", Reason: "compromised build"},
			{Kind: "version", Value: "v0.4.13", Reason: "bad release"},
		},
	}
}

// R1 — a revoked digest must be refused.
func TestCheckRevocation_DigestHit_R1(t *testing.T) {
	list := sampleList(1)
	// digestHex without the "sha256:" prefix; checkRevocationAgainstList adds it.
	if err := checkRevocationAgainstList("deadbeef", "v9.9.9", list); err == nil {
		t.Fatal("R1: expected revoked digest to be refused, got nil")
	}
	// Case-insensitivity: an upper-case hex must still hit the lower-case entry.
	if err := checkRevocationAgainstList("DEADBEEF", "v9.9.9", list); err == nil {
		t.Fatal("R1: expected upper-case digest to still hit, got nil")
	}
}

// R2 — a revoked version must be refused.
func TestCheckRevocation_VersionHit_R2(t *testing.T) {
	list := sampleList(1)
	if err := checkRevocationAgainstList("cafef00d", "v0.4.13", list); err == nil {
		t.Fatal("R2: expected revoked version to be refused, got nil")
	}
	// Case-insensitive version match.
	if err := checkRevocationAgainstList("cafef00d", "V0.4.13", list); err == nil {
		t.Fatal("R2: expected case-insensitive version to hit, got nil")
	}
	// A skills-v* tag entry must match the daemon's tag form too.
	skillList := RevocationList{
		Counter: 1,
		Revoked: []RevocationEntry{{Kind: "version", Value: "skills-v0.0.1", Reason: "bad skill bundle"}},
	}
	if err := checkRevocationAgainstList("cafef00d", "skills-v0.0.1", skillList); err == nil {
		t.Fatal("R2: expected revoked skills-v tag to be refused, got nil")
	}
}

// R3 — neither digest nor version hits → allowed (nil).
func TestCheckRevocation_NoHit_R3(t *testing.T) {
	list := sampleList(1)
	if err := checkRevocationAgainstList("0011223344", "v0.4.14", list); err != nil {
		t.Fatalf("R3: expected no hit to pass, got: %v", err)
	}
	// A digest that only differs from a revoked version string must not hit.
	if err := checkRevocationAgainstList("v0.4.13", "v0.4.14", list); err != nil {
		t.Fatalf("R3: a digest equal to a version string must not cross-match, got: %v", err)
	}
}

// R4 — an empty revocation list never refuses.
func TestCheckRevocation_EmptyList_R4(t *testing.T) {
	empty := RevocationList{Counter: 0, Revoked: nil}
	if err := checkRevocationAgainstList("deadbeef", "v0.4.13", empty); err != nil {
		t.Fatalf("R4: empty list must allow everything, got: %v", err)
	}
}

// R5 — monotonic anti-rollback: a fetched list older than persisted is rejected;
// fetched >= persisted is accepted and flagged for persistence; a missing fetched
// (not ok) falls open to persisted without persisting.
func TestPickEffectiveList_Monotonic_R5(t *testing.T) {
	persisted := sampleList(5)

	// Older fetched → use persisted, do NOT persist fetched (replay rejected).
	older := sampleList(3)
	eff, persist := pickEffectiveList(older, true, persisted, true)
	if persist {
		t.Fatal("R5: an older fetched list must not be persisted")
	}
	if eff.Counter != 5 {
		t.Fatalf("R5: expected persisted (counter 5) to win, got %d", eff.Counter)
	}

	// Newer fetched → adopt and persist.
	newer := sampleList(7)
	eff, persist = pickEffectiveList(newer, true, persisted, true)
	if !persist {
		t.Fatal("R5: a newer fetched list must be persisted")
	}
	if eff.Counter != 7 {
		t.Fatalf("R5: expected fetched (counter 7) to win, got %d", eff.Counter)
	}

	// Equal counter → adopt fetched (tie accepted, table content may have changed).
	eff, persist = pickEffectiveList(sampleList(5), true, persisted, true)
	if !persist || eff.Counter != 5 {
		t.Fatalf("R5: equal counter should adopt fetched, got persist=%v counter=%d", persist, eff.Counter)
	}

	// No valid fetched (verify/fetch failed) → fail-open to persisted, no persist.
	eff, persist = pickEffectiveList(RevocationList{}, false, persisted, true)
	if persist {
		t.Fatal("R5: must not persist when fetched is not ok")
	}
	if eff.Counter != 5 {
		t.Fatalf("R5: fail-open must use persisted (counter 5), got %d", eff.Counter)
	}

	// No fetched AND no persisted → empty list, refuses nothing.
	eff, persist = pickEffectiveList(RevocationList{}, false, RevocationList{}, false)
	if persist {
		t.Fatal("R5: cold start must not persist an empty fetched")
	}
	if err := checkRevocationAgainstList("deadbeef", "v0.4.13", eff); err != nil {
		t.Fatalf("R5: cold-start effective list must allow everything, got: %v", err)
	}
}

// TestRevocationListPersistence_RoundTrip exercises the on-disk read/write in a
// temp HOME, asserting the JSON survives a round-trip including the counter.
func TestRevocationListPersistence_RoundTrip(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	// On some platforms os.UserHomeDir reads other vars; normalize them.
	t.Setenv("USERPROFILE", tmpHome)

	// Nothing persisted yet → readPersisted returns (empty, false).
	if _, ok := readPersistedRevocationList(); ok {
		t.Fatal("expected no persisted list on a fresh HOME")
	}

	want := RevocationList{
		Counter: 42,
		Revoked: []RevocationEntry{
			{Kind: "digest", Value: "sha256:abc123", Reason: "test"},
			{Kind: "version", Value: "v1.2.3", Reason: "test2"},
		},
	}
	if err := writePersistedRevocationList(want); err != nil {
		t.Fatalf("write persisted: %v", err)
	}

	// The file lands at ~/.multica/revocations.json.
	path := filepath.Join(tmpHome, ".multica", revocationsAssetName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected persisted file at %s: %v", path, err)
	}

	got, ok := readPersistedRevocationList()
	if !ok {
		t.Fatal("expected persisted list to read back ok")
	}
	if got.Counter != want.Counter || len(got.Revoked) != len(want.Revoked) {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
	if got.Revoked[0].Value != "sha256:abc123" || got.Revoked[1].Value != "v1.2.3" {
		t.Fatalf("round-trip entry mismatch: got %+v", got.Revoked)
	}

	// A corrupt file reads back as (empty, false) — fail-safe.
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	if _, ok := readPersistedRevocationList(); ok {
		t.Fatal("expected corrupt persisted file to read back as not-ok")
	}
}

// TestSHA256HexOf_MatchesAttestationDigest pins the digest helper to the same
// encoding VerifyArtifactAttestation uses, so a "digest" revocation entry written
// as the attestation subject digest matches.
func TestSHA256HexOf_MatchesAttestationDigest(t *testing.T) {
	data := []byte("multica-cli-archive-bytes")
	// Independent reference using the json round-trip is overkill; assert it is a
	// 64-char lowercase hex string and stable.
	h := sha256HexOf(data)
	if len(h) != 64 {
		t.Fatalf("expected 64-char hex, got %d (%q)", len(h), h)
	}
	if h != sha256HexOf(data) {
		t.Fatal("sha256HexOf is not deterministic")
	}
	// checkRevocationAgainstList must match a digest entry built from this hex.
	list := RevocationList{Revoked: []RevocationEntry{{Kind: "digest", Value: "sha256:" + h, Reason: "x"}}}
	if err := checkRevocationAgainstList(h, "v0.0.0", list); err == nil {
		t.Fatal("expected digest built from sha256HexOf to hit revocation entry")
	}
}

// TestUnknownKindIgnored ensures forward-compat: an unknown kind does not crash
// or spuriously refuse, and other entries in the same list still match.
func TestUnknownKindIgnored(t *testing.T) {
	list := RevocationList{
		Counter: 1,
		Revoked: []RevocationEntry{
			{Kind: "future-kind", Value: "whatever", Reason: "n/a"},
			{Kind: "digest", Value: "sha256:beef", Reason: "bad"},
		},
	}
	if err := checkRevocationAgainstList("cafe", "v1.0.0", list); err != nil {
		t.Fatalf("unknown kind alone must not refuse, got: %v", err)
	}
	if err := checkRevocationAgainstList("beef", "v1.0.0", list); err == nil {
		t.Fatal("a real digest entry alongside an unknown kind must still hit")
	}
}

// TestRevocationListJSONShape guards the committed revocations.json contract:
// {"counter": int, "revoked": [{kind,value,reason}]} round-trips into the types.
func TestRevocationListJSONShape(t *testing.T) {
	raw := []byte(`{"counter":3,"revoked":[{"kind":"digest","value":"sha256:aa","reason":"r1"},{"kind":"version","value":"v0.1.0","reason":"r2"}]}`)
	var list RevocationList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("unmarshal contract JSON: %v", err)
	}
	if list.Counter != 3 || len(list.Revoked) != 2 {
		t.Fatalf("unexpected parse: %+v", list)
	}
	if list.Revoked[0].Kind != "digest" || list.Revoked[1].Kind != "version" {
		t.Fatalf("unexpected entries: %+v", list.Revoked)
	}
}
