package main

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestCliVersionGate(t *testing.T) {
	req := "0.4.13"
	cases := []struct {
		current string
		wantOK  bool
		note    string
	}{
		{"dev", true, "dev build skips the floor"},
		{"v0.2.15-235-gdaf0e935", true, "git-describe dev build skips the floor"},
		{"0.4.10", false, "below floor fails"},
		{"v0.4.12", false, "still below floor fails"},
		{"0.4.13", true, "exactly at floor passes"},
		{"0.4.14", true, "above floor passes"},
		{"1.0.0", true, "major above floor passes"},
	}
	for _, tc := range cases {
		got := cliVersionGate(tc.current, req)
		if got.OK != tc.wantOK {
			t.Errorf("cliVersionGate(%q,%q).OK = %v, want %v (%s) — detail: %s",
				tc.current, req, got.OK, tc.wantOK, tc.note, got.Detail)
		}
		if !got.Hard {
			t.Errorf("cli-version must be a Hard gate")
		}
	}
}

func TestConfigGate(t *testing.T) {
	full := cli.CLIConfig{ServerURL: "https://api.x", Token: "mul_x", WorkspaceID: "ws-1"}
	if c := configGate(full); !c.OK {
		t.Errorf("complete config should pass, got: %s", c.Detail)
	}
	missingTok := cli.CLIConfig{ServerURL: "https://api.x", WorkspaceID: "ws-1"}
	c := configGate(missingTok)
	if c.OK {
		t.Errorf("missing token should fail")
	}
	if c.Detail == "" || !strings.Contains(c.Detail, "token") {
		t.Errorf("detail should name the missing key, got: %s", c.Detail)
	}
}

func TestDoctorChecks_HardFailures(t *testing.T) {
	// outdated CLI + empty config → 2 hard failures → non-zero exit territory
	checks := doctorChecks("0.4.10", "0.4.13", cli.CLIConfig{})
	if n := hardFailures(checks); n != 2 {
		t.Errorf("hardFailures = %d, want 2 (version + config)", n)
	}
	// healthy preflight → 0 hard failures
	ok := doctorChecks("0.4.13", "0.4.13",
		cli.CLIConfig{ServerURL: "s", Token: "t", WorkspaceID: "w"})
	if n := hardFailures(ok); n != 0 {
		t.Errorf("healthy preflight hardFailures = %d, want 0", n)
	}
}

func TestDoctorCommandRegistered(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"doctor"})
	if err != nil || c.Name() != "doctor" {
		t.Fatalf("doctor not registered: %v (got %v)", err, c.Name())
	}
	if c.Flags().Lookup("output") == nil {
		t.Error("doctor missing --output flag")
	}
}
