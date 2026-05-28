package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// `multica deployment` — runtime instance tracker CLI (Plan 4 / PR D).
//
// `register` and `heartbeat` are designed to be called from inside a
// runtime (a long-running process registers itself and pings periodically
// so the control plane knows it's alive). `list` and `active` are
// inspection commands for operators.

var deploymentCmd = &cobra.Command{
	Use:   "deployment",
	Short: "Inspect and manage control-plane deployments (runtime instances)",
}

var deploymentListCmd = &cobra.Command{
	Use:   "list <integration-id-or-name>",
	Short: "List recent deployments for an integration (newest first)",
	Args:  exactArgs(1),
	RunE:  runDeploymentList,
}

var deploymentActiveCmd = &cobra.Command{
	Use:   "active <integration-id-or-name>",
	Short: "Get the currently active deployment for an integration",
	Args:  exactArgs(1),
	RunE:  runDeploymentActive,
}

var deploymentRegisterCmd = &cobra.Command{
	Use:   "register --integration <id-or-name>",
	Short: "Register a new running deployment (typically called from a runtime on boot)",
	RunE:  runDeploymentRegister,
}

var deploymentHeartbeatCmd = &cobra.Command{
	Use:   "heartbeat <deployment-id>",
	Short: "Send a heartbeat for a running deployment (extends its TTL beyond 90s)",
	Args:  exactArgs(1),
	RunE:  runDeploymentHeartbeat,
}

func init() {
	deploymentCmd.AddCommand(deploymentListCmd)
	deploymentCmd.AddCommand(deploymentActiveCmd)
	deploymentCmd.AddCommand(deploymentRegisterCmd)
	deploymentCmd.AddCommand(deploymentHeartbeatCmd)

	deploymentListCmd.Flags().Int("limit", 20, "Maximum rows to return (1-100)")
	deploymentListCmd.Flags().String("output", "table", "Output format: table or json")

	deploymentActiveCmd.Flags().String("output", "json", "Output format: table or json")

	deploymentRegisterCmd.Flags().String("integration", "", "Integration id or name (required)")
	deploymentRegisterCmd.Flags().String("image", "", "Image tag or commit sha being deployed (required)")
	deploymentRegisterCmd.Flags().String("host-url", "", "Where the deployment is reachable")
	deploymentRegisterCmd.Flags().Int32("version", 0, "Config version this deployment applied (required, > 0)")
	deploymentRegisterCmd.Flags().String("output", "json", "Output format: table or json")
	_ = deploymentRegisterCmd.MarkFlagRequired("integration")
	_ = deploymentRegisterCmd.MarkFlagRequired("image")
	_ = deploymentRegisterCmd.MarkFlagRequired("version")

	deploymentHeartbeatCmd.Flags().Int32("config-version", 0, "Config version currently applied on this deployment")
	deploymentHeartbeatCmd.Flags().String("status", "", "Optional status update: starting | running | degraded | stopped")
	deploymentHeartbeatCmd.Flags().String("output", "json", "Output format: json")
}

// --- commands ---

func runDeploymentList(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	integrationID, err := resolveIntegrationID(ctx, c, args[0])
	if err != nil {
		return err
	}

	limit, _ := cmd.Flags().GetInt("limit")
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	var rows []map[string]any
	path := fmt.Sprintf("/api/integrations/%s/deployments?workspace_id=%s&limit=%d",
		integrationID, url.QueryEscape(c.WorkspaceID), limit)
	if err := c.GetJSON(ctx, path, &rows); err != nil {
		return fmt.Errorf("list deployments: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, rows)
	}
	headers := []string{"STATUS", "VERSION", "IMAGE/COMMIT", "STARTED", "LAST HEARTBEAT", "ID"}
	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		tableRows = append(tableRows, []string{
			strVal(r, "status"),
			fmt.Sprint(r["version"]),
			truncateMid(strVal(r, "image_or_commit"), 24),
			strVal(r, "started_at"),
			strVal(r, "last_heartbeat"),
			strVal(r, "id"),
		})
	}
	cli.PrintTable(os.Stdout, headers, tableRows)
	return nil
}

func runDeploymentActive(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	integrationID, err := resolveIntegrationID(ctx, c, args[0])
	if err != nil {
		return err
	}

	var resp map[string]any
	path := "/api/integrations/" + integrationID + "/active-deployment?workspace_id=" +
		url.QueryEscape(c.WorkspaceID)
	if err := c.GetJSON(ctx, path, &resp); err != nil {
		return fmt.Errorf("active deployment: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"STATUS", "VERSION", "IMAGE/COMMIT", "LAST HEARTBEAT"}
		cli.PrintTable(os.Stdout, headers, [][]string{{
			strVal(resp, "status"),
			fmt.Sprint(resp["version"]),
			truncateMid(strVal(resp, "image_or_commit"), 24),
			strVal(resp, "last_heartbeat"),
		}})
		return nil
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runDeploymentRegister(cmd *cobra.Command, _ []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	idOrName, _ := cmd.Flags().GetString("integration")
	integrationID, err := resolveIntegrationID(ctx, c, idOrName)
	if err != nil {
		return err
	}

	image, _ := cmd.Flags().GetString("image")
	hostURL, _ := cmd.Flags().GetString("host-url")
	version, _ := cmd.Flags().GetInt32("version")
	if version <= 0 {
		return fmt.Errorf("--version must be positive")
	}

	body := map[string]any{
		"integration_id":  integrationID,
		"image_or_commit": image,
		"host_url":        hostURL,
		"version":         version,
	}
	var resp map[string]any
	if err := c.PostJSON(ctx,
		"/api/deployments?workspace_id="+url.QueryEscape(c.WorkspaceID),
		body, &resp); err != nil {
		return fmt.Errorf("register deployment: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"ID", "STATUS", "VERSION", "IMAGE/COMMIT"}
		cli.PrintTable(os.Stdout, headers, [][]string{{
			strVal(resp, "id"),
			strVal(resp, "status"),
			fmt.Sprint(resp["version"]),
			truncateMid(strVal(resp, "image_or_commit"), 24),
		}})
		return nil
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runDeploymentHeartbeat(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	deploymentID := args[0]
	configVersion, _ := cmd.Flags().GetInt32("config-version")
	status, _ := cmd.Flags().GetString("status")

	body := map[string]any{}
	if configVersion > 0 {
		body["config_applied_version"] = configVersion
	}
	if status != "" {
		body["status"] = status
	}

	var resp map[string]any
	if err := c.PostJSON(ctx,
		"/api/deployments/"+deploymentID+"/heartbeat?workspace_id="+url.QueryEscape(c.WorkspaceID),
		body, &resp); err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	return cli.PrintJSON(os.Stdout, resp)
}

// truncateMid shortens a long sha/tag to a fixed width keeping the head and
// tail so it's still recognizable (e.g. "abc123…789def"). For shorter inputs
// returns the string unchanged.
func truncateMid(s string, max int) string {
	if len(s) <= max {
		return s
	}
	head := (max - 1) / 2
	tail := max - 1 - head
	return s[:head] + "…" + s[len(s)-tail:]
}

// Ensure json import is used even if PrintJSON path is unreachable in some
// platform-specific test setup. (Real usage: not a no-op.)
var _ = json.NewEncoder
