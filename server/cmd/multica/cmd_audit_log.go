package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// `multica audit-log` — read-only inspection of the audit_log table.

var auditLogCmd = &cobra.Command{
	Use:   "audit-log",
	Short: "Inspect the audit log (control-plane writes + secret reads)",
}

var auditLogListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent audit entries (newest first)",
	RunE:  runAuditLogList,
}

func init() {
	auditLogCmd.AddCommand(auditLogListCmd)

	auditLogListCmd.Flags().String("resource", "", "Filter by exact resource string (e.g. 'secret:<integration-id>:FEISHU_APP_SECRET')")
	auditLogListCmd.Flags().StringSlice("event-type", nil, "Filter by event type (repeatable; e.g. --event-type secret:read --event-type secret:set)")
	auditLogListCmd.Flags().String("since", "", "Only entries after this timestamp. Accepts YYYY-MM-DD (UTC) or RFC3339. Also accepts relative durations like 24h, 7d, 30d.")
	auditLogListCmd.Flags().Int("limit", 100, "Maximum rows (1-500, default 100)")
	auditLogListCmd.Flags().String("output", "table", "Output format: table or json")
}

func runAuditLogList(cmd *cobra.Command, _ []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	params := url.Values{}
	params.Set("workspace_id", c.WorkspaceID)
	if v, _ := cmd.Flags().GetString("resource"); v != "" {
		params.Set("resource", v)
	}
	if v, _ := cmd.Flags().GetStringSlice("event-type"); len(v) > 0 {
		params.Set("event_types", strings.Join(v, ","))
	}
	if v, _ := cmd.Flags().GetString("since"); v != "" {
		since, err := resolveSinceFlag(v)
		if err != nil {
			return err
		}
		params.Set("since", since)
	}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprint(v))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var rows []map[string]any
	if err := c.GetJSON(ctx, "/api/audit-logs?"+params.Encode(), &rows); err != nil {
		return fmt.Errorf("list audit log: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, rows)
	}
	headers := []string{"CREATED", "ACTOR", "EVENT", "RESOURCE"}
	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		actor := strVal(r, "actor_type")
		if id := strVal(r, "actor_user_id"); id != "" {
			actor = actor + ":" + id[:8]
		}
		tableRows = append(tableRows, []string{
			strVal(r, "created_at"),
			actor,
			strVal(r, "event_type"),
			truncateMid(strVal(r, "resource"), 40),
		})
	}
	cli.PrintTable(os.Stdout, headers, tableRows)
	return nil
}

// resolveSinceFlag accepts the shapes the audit endpoint accepts (YYYY-MM-DD
// and RFC3339) plus the operator-friendly relative durations like "24h",
// "7d", "30d". Relative forms are computed against the current wall clock.
func resolveSinceFlag(v string) (string, error) {
	v = strings.TrimSpace(v)
	// Absolute forms — pass through.
	if _, err := time.Parse(time.RFC3339, v); err == nil {
		return v, nil
	}
	if _, err := time.Parse("2006-01-02", v); err == nil {
		return v, nil
	}
	// Relative: NNNd or NNNh / NNNm / NNNs (Go duration with d alias for days).
	if d, err := parseRelativeDuration(v); err == nil {
		return time.Now().Add(-d).UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("--since: expected YYYY-MM-DD, RFC3339, or relative duration like 24h / 7d / 30d (got %q)", v)
}

func parseRelativeDuration(v string) (time.Duration, error) {
	// Try stdlib first (supports h/m/s).
	if d, err := time.ParseDuration(v); err == nil {
		return d, nil
	}
	// Manual "Nd" handling (Go's ParseDuration doesn't accept days).
	if strings.HasSuffix(v, "d") {
		var n int
		_, err := fmt.Sscanf(v, "%dd", &n)
		if err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	return 0, fmt.Errorf("unrecognized duration")
}
