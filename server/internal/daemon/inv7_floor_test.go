package daemon

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TEA-113 INV-7 — adversarial coverage for the daemon-side anti-downgrade
// memory floor in handleUpdate. These tests assert that a server-triggered
// nudge pointing at a non-newer (or dev-build) target is refused *before*
// runUpdateFn is ever invoked — i.e. the floor never reaches the
// UpdateViaDownload/attestation/revocation/SHA verification chain. They reuse
// the runUpdateFn-injection stub idiom from auto_update_test.go: runUpdateFn
// fails the test on call, so "runUpdateFn was not called" is enforced
// structurally rather than by side-effect inspection.
//
// The client is wired to a swallowing httptest server because handleUpdate's
// refusal path reports a "failed" terminal result over HTTP; we don't want the
// floor logic coupled to a nil client, and the reported body is asserted to
// carry status=failed (never running/completed) on the refusal path.

// floorTestDaemon builds a Daemon with the given running CLI version, a client
// pointing at a swallowing httptest server (so reportUpdateResult on the
// refusal path succeeds without a real network call), and a runUpdateFn that
// fails the test if the floor ever lets execution through to it.
func floorTestDaemon(t *testing.T, runningVersion string) (*Daemon, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	withFastUpdateReportBackoffs(t)

	var runUpdateCalls atomic.Int32
	var restartCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	d := &Daemon{
		cfg:    Config{CLIVersion: runningVersion, AutoUpdateEnabled: true},
		client: NewClient(srv.URL),
		logger: slog.Default(),
		cancelFunc: func() {
			restartCalls.Add(1)
		},
	}
	d.runUpdateFn = func(string) (string, error) {
		runUpdateCalls.Add(1)
		t.Fatalf("runUpdateFn must not be called: INV-7 floor should have refused before reaching UpdateViaDownload")
		return "", nil
	}
	return d, &runUpdateCalls, &restartCalls
}

// (a) Nudge pointing at an OLDER, validly-signed tag (TargetVersion <
// CLIVersion) must be refused by the memory floor before runUpdateFn runs.
// This is the core downgrade-via-spoofed-server threat (mini-ADR §3 row
// "降级(指向未吊销旧版)").
func TestHandleUpdate_INV7_RefusesOlderTarget(t *testing.T) {
	d, runUpdateCalls, restartCalls := floorTestDaemon(t, "v0.4.15")

	d.handleUpdate(context.Background(), "rt-1", &PendingUpdate{
		ID:            "upd-old",
		TargetVersion: "v0.4.13", // older than the running binary
	})

	if runUpdateCalls.Load() != 0 {
		t.Fatalf("runUpdateFn was called for an older target; INV-7 floor must refuse before the upgrade path")
	}
	if restartCalls.Load() != 0 {
		t.Fatalf("triggerRestart fired on a refused downgrade")
	}
	if d.updating.Load() {
		t.Fatalf("updating CAS must not be claimed when the floor refuses (refusal is before the CAS)")
	}
}

// (a') Equal version must also be refused (<= semantics): a replayed nudge for
// the exact running version must not re-run the same tag.
func TestHandleUpdate_INV7_RefusesEqualTarget(t *testing.T) {
	d, runUpdateCalls, restartCalls := floorTestDaemon(t, "v0.4.15")

	d.handleUpdate(context.Background(), "rt-1", &PendingUpdate{
		ID:            "upd-equal",
		TargetVersion: "v0.4.15", // exactly equal — must be rejected (not <)
	})

	if runUpdateCalls.Load() != 0 {
		t.Fatalf("runUpdateFn was called for an equal target; INV-7 uses <= semantics and must reject equality")
	}
	if restartCalls.Load() != 0 {
		t.Fatalf("triggerRestart fired on a refused equal-version nudge")
	}
}

// (b) Offline revocation (fetch fails → fail-open) combined with an older
// target: the memory floor is the only defence that does not depend on
// revocation reachability. Even with the revocation门 fail-open, an older
// target must still be refused — and because the floor runs entirely before
// runUpdateFn (and thus before any attestation/revocation/SHA call), the
// revocation fetch is never even reached. We model "the network is down" by
// asserting runUpdateFn (the sole entry into UpdateViaDownload, where the
// revocation fetch lives) is never invoked: the floor short-circuits ahead of
// it. force is set true here to also prove force does not bypass the floor.
func TestHandleUpdate_INV7_RefusesOlderTargetEvenWhenRevocationWouldFailOpen(t *testing.T) {
	d, runUpdateCalls, restartCalls := floorTestDaemon(t, "v0.4.15")

	d.handleUpdate(context.Background(), "rt-1", &PendingUpdate{
		ID:            "upd-old-offline",
		TargetVersion: "v0.4.13",
		Force:         true, // force must NOT bypass the anti-downgrade floor (INV-2 + INV-5 spirit)
	})

	if runUpdateCalls.Load() != 0 {
		t.Fatalf("runUpdateFn (which owns the revocation fetch) was reached; the memory floor must refuse an older target independent of revocation reachability, and force must not bypass it")
	}
	if restartCalls.Load() != 0 {
		t.Fatalf("triggerRestart fired on a refused offline downgrade")
	}
}

// (c) Dev/source build (isReleaseVersion(d.cfg.CLIVersion) == false): the floor
// is fail-closed — an unparseable running version must refuse the update, never
// fall through to "unparseable → allow" (which would be a downgrade bypass).
// Mirrors auto_update.go:53.
func TestHandleUpdate_INV7_RefusesDevBuild(t *testing.T) {
	d, runUpdateCalls, restartCalls := floorTestDaemon(t, "v0.4.15-235-gdeadbee")

	d.handleUpdate(context.Background(), "rt-1", &PendingUpdate{
		ID:            "upd-dev",
		TargetVersion: "v0.4.16", // strictly newer, but the running build is dev
	})

	if runUpdateCalls.Load() != 0 {
		t.Fatalf("runUpdateFn was called on a dev/source build; INV-7 floor is fail-closed and must refuse")
	}
	if restartCalls.Load() != 0 {
		t.Fatalf("triggerRestart fired on a refused dev-build update")
	}
}

// Sanity counterpart: a strictly-newer target on a release build passes the
// floor and reaches runUpdateFn exactly once. This pins the floor's <=
// semantics from the allow side so a future tightening that accidentally
// rejects legitimate upgrades is caught. A distinct stub is installed here
// because floorTestDaemon's stub fails on call.
func TestHandleUpdate_INV7_AllowsStrictlyNewerTarget(t *testing.T) {
	withFastUpdateReportBackoffs(t)

	var restartCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	d := &Daemon{
		cfg:        Config{CLIVersion: "v0.4.15", AutoUpdateEnabled: true},
		client:     NewClient(srv.URL),
		logger:     slog.Default(),
		cancelFunc: func() { restartCalls.Add(1) },
	}
	var gotTarget string
	var runUpdateCalls atomic.Int32
	d.runUpdateFn = func(target string) (string, error) {
		runUpdateCalls.Add(1)
		gotTarget = target
		return "upgraded", nil
	}

	d.handleUpdate(context.Background(), "rt-1", &PendingUpdate{
		ID:            "upd-new",
		TargetVersion: "v0.4.16",
	})

	if runUpdateCalls.Load() != 1 {
		t.Fatalf("runUpdateFn called %d times for a strictly-newer target, want 1", runUpdateCalls.Load())
	}
	if gotTarget != "v0.4.16" {
		t.Fatalf("runUpdateFn target = %q, want v0.4.16", gotTarget)
	}
	if restartCalls.Load() != 1 {
		t.Fatalf("triggerRestart fired %d times after a successful upgrade, want 1", restartCalls.Load())
	}
}
