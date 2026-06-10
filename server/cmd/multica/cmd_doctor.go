package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// RequiredCLIVersion is the single preflight floor that consolidates the
// previously scattered per-feature version gates: `comment add --inline`
// (v0.4.11), `skill pull` (v0.4.12), `skill lint` (v0.4.13). Publishing (命门B)
// and skill sync need all three, so the floor is the highest of them. Bump this
// one constant when a new floor lands, instead of sprinkling version prose.
const RequiredCLIVersion = "0.4.13"

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Preflight: CLI version floor + config — non-zero exit if a hard gate fails",
	Long: "Consolidates the scattered version gates (comment add --inline v0.4.11 / " +
		"skill pull v0.4.12 / skill lint v0.4.13) into one publish-time hard gate, plus a " +
		"config check. Run it in CI or before publishing: a non-zero exit means 'do not " +
		"publish until fixed' — e.g. an outdated CLI silently lacks --inline, producing " +
		"dirty comments instead of inline renders.",
	RunE: runDoctor,
}

func init() {
	doctorCmd.Flags().String("output", "table", "Output format: table or json")
}

// doctorCheck is one preflight line. A failed Hard check forces a non-zero exit.
type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Hard   bool   `json:"hard"`
	Detail string `json:"detail"`
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	cfg, _ := cli.LoadCLIConfigForProfile(resolveProfile(cmd))
	checks := doctorChecks(cli.ClientVersion, RequiredCLIVersion, cfg)

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		_ = cli.PrintJSON(os.Stdout, checks)
	} else {
		rows := make([][]string, 0, len(checks))
		for _, c := range checks {
			status := "OK"
			if !c.OK {
				status = "FAIL"
				if !c.Hard {
					status = "WARN"
				}
			}
			rows = append(rows, []string{status, c.Name, c.Detail})
		}
		cli.PrintTable(os.Stdout, []string{"STATUS", "CHECK", "DETAIL"}, rows)
	}

	if n := hardFailures(checks); n > 0 {
		return fmt.Errorf("doctor: %d hard preflight check(s) failed — do not publish until fixed", n)
	}
	return nil
}

// doctorChecks assembles the preflight checks. Pure (no I/O) for testability:
// callers inject the current/required versions and the loaded config.
func doctorChecks(current, required string, cfg cli.CLIConfig) []doctorCheck {
	return []doctorCheck{
		cliVersionGate(current, required),
		configGate(cfg),
	}
}

// cliVersionGate is the consolidated version floor. Dev builds skip the floor
// (developers run unreleased binaries); a release build below the floor fails
// hard — it would silently lack --inline / skill pull / lint.
func cliVersionGate(current, required string) doctorCheck {
	c := doctorCheck{Name: "cli-version", Hard: true}
	switch {
	case !cli.IsReleaseVersion(current):
		c.OK = true
		c.Detail = fmt.Sprintf("dev build %q — version floor (≥%s) skipped", current, required)
	case cli.IsNewerVersion(required, current):
		c.OK = false
		c.Detail = fmt.Sprintf("CLI %s < required %s — run `multica update`", current, required)
	default:
		c.OK = true
		c.Detail = fmt.Sprintf("CLI %s ≥ %s", current, required)
	}
	return c
}

// configGate fails when the CLI cannot publish: server_url / token / workspace_id
// must all be set (run `multica setup`).
func configGate(cfg cli.CLIConfig) doctorCheck {
	c := doctorCheck{Name: "config", Hard: true}
	var missing []string
	if cfg.ServerURL == "" {
		missing = append(missing, "server_url")
	}
	if cfg.Token == "" {
		missing = append(missing, "token")
	}
	if cfg.WorkspaceID == "" {
		missing = append(missing, "workspace_id")
	}
	if len(missing) > 0 {
		c.OK = false
		c.Detail = "missing " + strings.Join(missing, ", ") + " — run `multica setup`"
		return c
	}
	c.OK = true
	c.Detail = "server_url / token / workspace_id set"
	return c
}

func hardFailures(checks []doctorCheck) int {
	n := 0
	for _, c := range checks {
		if c.Hard && !c.OK {
			n++
		}
	}
	return n
}
