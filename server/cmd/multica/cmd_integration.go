package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// `multica integration` — control plane CLI (Plan 4 / PR D, Task D-10).

var integrationCmd = &cobra.Command{
	Use:   "integration",
	Short: "Manage control-plane integrations (MCP servers, feishu, autopilot bots)",
}

var integrationCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new integration",
	RunE:  runIntegrationCreate,
}

var integrationListCmd = &cobra.Command{
	Use:   "list",
	Short: "List integrations in the workspace",
	RunE:  runIntegrationList,
}

var integrationGetCmd = &cobra.Command{
	Use:   "get <id-or-name>",
	Short: "Get integration details",
	Args:  exactArgs(1),
	RunE:  runIntegrationGet,
}

var integrationStatusCmd = &cobra.Command{
	Use:   "status <id-or-name>",
	Short: "Get integration status + active deployment",
	Args:  exactArgs(1),
	RunE:  runIntegrationStatus,
}

var integrationSetCmd = &cobra.Command{
	Use:   "set <id-or-name> KEY=VALUE [KEY=VALUE...]",
	Short: "Set one or more top-level config keys (merge with existing)",
	Args:  minArgs(2),
	RunE:  runIntegrationSet,
}

var integrationPatchCmd = &cobra.Command{
	Use:   "patch <id-or-name>",
	Short: "Replace the whole config with --config-file (JSON)",
	Args:  exactArgs(1),
	RunE:  runIntegrationPatch,
}

var integrationRestartCmd = &cobra.Command{
	Use:   "restart <id-or-name>",
	Short: "Publish a restart event (subscribers re-pull)",
	Args:  exactArgs(1),
	RunE:  runIntegrationRestart,
}

var integrationRedeployCmd = &cobra.Command{
	Use:   "redeploy <id-or-name>",
	Short: "Trigger the configured deployment webhook",
	Args:  exactArgs(1),
	RunE:  runIntegrationRedeploy,
}

var integrationDeleteCmd = &cobra.Command{
	Use:   "delete <id-or-name>",
	Short: "Delete an integration (cascade-deletes secrets + deployments)",
	Args:  exactArgs(1),
	RunE:  runIntegrationDelete,
}

func init() {
	integrationCmd.AddCommand(integrationCreateCmd)
	integrationCmd.AddCommand(integrationListCmd)
	integrationCmd.AddCommand(integrationGetCmd)
	integrationCmd.AddCommand(integrationStatusCmd)
	integrationCmd.AddCommand(integrationSetCmd)
	integrationCmd.AddCommand(integrationPatchCmd)
	integrationCmd.AddCommand(integrationRestartCmd)
	integrationCmd.AddCommand(integrationRedeployCmd)
	integrationCmd.AddCommand(integrationDeleteCmd)

	integrationCreateCmd.Flags().String("kind", "", "Integration kind: mcp-server | feishu | autopilot-bot (required)")
	integrationCreateCmd.Flags().String("name", "", "Integration name (required, unique within workspace)")
	integrationCreateCmd.Flags().String("config", "", "Inline JSON config (mutually exclusive with --config-file)")
	integrationCreateCmd.Flags().String("config-file", "", "Path to a JSON file containing the initial config")
	integrationCreateCmd.Flags().String("deployment-webhook", "", "URL invoked by `multica integration redeploy`")
	integrationCreateCmd.Flags().String("config-schema-ref", "", "Optional reference for runtime config validation")
	integrationCreateCmd.Flags().String("output", "json", "Output format: table or json")
	integrationCreateCmd.MarkFlagsMutuallyExclusive("config", "config-file")

	integrationListCmd.Flags().String("kind", "", "Filter by kind")
	integrationListCmd.Flags().String("output", "table", "Output format: table or json")

	integrationGetCmd.Flags().String("output", "json", "Output format: table or json")
	integrationStatusCmd.Flags().String("output", "json", "Output format: table or json")

	integrationPatchCmd.Flags().String("config-file", "", "Path to a JSON file with the replacement config (required)")
	integrationPatchCmd.Flags().String("output", "json", "Output format: table or json")

	integrationRedeployCmd.Flags().String("output", "json", "Output format: table or json")
}

// --- helpers ---

// minArgs is like exactArgs but requires at least n positional arguments.
func minArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < n {
			fmt.Fprintf(cmd.ErrOrStderr(), "Error: requires at least %d args, received %d\n\n", n, len(args))
			cmd.Help()
			return errSilent
		}
		return nil
	}
}

// resolveIntegrationID accepts a UUID directly or resolves a name to UUID
// by listing the workspace's integrations. The cli helper layer doesn't
// have a dedicated id resolver for integrations yet, so we list-and-match
// here. For workspaces with thousands of integrations this would want a
// dedicated endpoint, but realistic counts are small.
func resolveIntegrationID(ctx context.Context, c *cli.APIClient, idOrName string) (string, error) {
	if looksLikeUUID(idOrName) {
		return idOrName, nil
	}
	var rows []map[string]any
	path := "/api/integrations?workspace_id=" + url.QueryEscape(c.WorkspaceID)
	if err := c.GetJSON(ctx, path, &rows); err != nil {
		return "", fmt.Errorf("list integrations to resolve name: %w", err)
	}
	for _, row := range rows {
		if strVal(row, "name") == idOrName {
			return strVal(row, "id"), nil
		}
	}
	return "", fmt.Errorf("no integration with id or name %q in workspace", idOrName)
}

func looksLikeUUID(s string) bool {
	return len(s) == 36 && strings.Count(s, "-") == 4
}

// --- commands ---

func runIntegrationCreate(cmd *cobra.Command, _ []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	kind, _ := cmd.Flags().GetString("kind")
	name, _ := cmd.Flags().GetString("name")
	if kind == "" || name == "" {
		return fmt.Errorf("--kind and --name are required")
	}

	cfg := map[string]any{}
	if inline, _ := cmd.Flags().GetString("config"); inline != "" {
		if err := json.Unmarshal([]byte(inline), &cfg); err != nil {
			return fmt.Errorf("--config is not valid JSON: %w", err)
		}
	} else if path, _ := cmd.Flags().GetString("config-file"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read config-file: %w", err)
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("config-file is not valid JSON: %w", err)
		}
	}

	body := map[string]any{
		"kind":                   kind,
		"name":                   name,
		"config":                 cfg,
		"deployment_webhook_url": getString(cmd, "deployment-webhook"),
		"config_schema_ref":      getString(cmd, "config-schema-ref"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var resp map[string]any
	if err := c.PostJSON(ctx, "/api/integrations?workspace_id="+url.QueryEscape(c.WorkspaceID), body, &resp); err != nil {
		return fmt.Errorf("create integration: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	return printIntegration(resp, output)
}

func runIntegrationList(cmd *cobra.Command, _ []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	params := url.Values{}
	params.Set("workspace_id", c.WorkspaceID)
	if k, _ := cmd.Flags().GetString("kind"); k != "" {
		params.Set("kind", k)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var rows []map[string]any
	if err := c.GetJSON(ctx, "/api/integrations?"+params.Encode(), &rows); err != nil {
		return fmt.Errorf("list integrations: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, rows)
	}
	headers := []string{"NAME", "KIND", "STATUS", "VERSION", "ID"}
	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		tableRows = append(tableRows, []string{
			strVal(r, "name"),
			strVal(r, "kind"),
			strVal(r, "status"),
			fmt.Sprint(r["version"]),
			strVal(r, "id"),
		})
	}
	cli.PrintTable(os.Stdout, headers, tableRows)
	return nil
}

func runIntegrationGet(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := resolveIntegrationID(ctx, c, args[0])
	if err != nil {
		return err
	}
	var resp map[string]any
	if err := c.GetJSON(ctx, "/api/integrations/"+id+"?workspace_id="+url.QueryEscape(c.WorkspaceID), &resp); err != nil {
		return fmt.Errorf("get integration: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	return printIntegration(resp, output)
}

func runIntegrationStatus(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := resolveIntegrationID(ctx, c, args[0])
	if err != nil {
		return err
	}
	var resp map[string]any
	if err := c.GetJSON(ctx, "/api/integrations/"+id+"/status?workspace_id="+url.QueryEscape(c.WorkspaceID), &resp); err != nil {
		return fmt.Errorf("get status: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		// Compact summary
		headers := []string{"INTEGRATION", "VERSION", "ACTIVE DEPLOYMENT", "DEP STATUS"}
		dep, _ := resp["active_deployment"].(map[string]any)
		row := []string{
			strVal(resp, "integration_status"),
			fmt.Sprint(resp["config_version"]),
			strVal(dep, "image_or_commit"),
			strVal(dep, "status"),
		}
		cli.PrintTable(os.Stdout, headers, [][]string{row})
		return nil
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runIntegrationSet(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := resolveIntegrationID(ctx, c, args[0])
	if err != nil {
		return err
	}

	// Fetch current config, merge KEY=VALUE pairs in.
	var current map[string]any
	if err := c.GetJSON(ctx, "/api/integrations/"+id+"?workspace_id="+url.QueryEscape(c.WorkspaceID), &current); err != nil {
		return fmt.Errorf("fetch current config: %w", err)
	}
	cfg, _ := current["config"].(map[string]any)
	if cfg == nil {
		cfg = map[string]any{}
	}
	for _, kv := range args[1:] {
		idx := strings.IndexByte(kv, '=')
		if idx < 1 {
			return fmt.Errorf("expected KEY=VALUE, got %q", kv)
		}
		k := kv[:idx]
		v := kv[idx+1:]
		// Best-effort JSON-decode the value so callers can pass numbers /
		// booleans / arrays. Falls back to raw string on parse error.
		var decoded any = v
		_ = json.Unmarshal([]byte(v), &decoded)
		cfg[k] = decoded
	}

	body := map[string]any{"config": cfg}
	var resp map[string]any
	if err := c.PatchJSON(ctx, "/api/integrations/"+id+"/config?workspace_id="+url.QueryEscape(c.WorkspaceID), body, &resp); err != nil {
		return fmt.Errorf("patch config: %w", err)
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runIntegrationPatch(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	path, _ := cmd.Flags().GetString("config-file")
	if path == "" {
		return fmt.Errorf("--config-file is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config-file: %w", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("config-file is not valid JSON: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := resolveIntegrationID(ctx, c, args[0])
	if err != nil {
		return err
	}
	var resp map[string]any
	if err := c.PatchJSON(ctx, "/api/integrations/"+id+"/config?workspace_id="+url.QueryEscape(c.WorkspaceID),
		map[string]any{"config": cfg}, &resp); err != nil {
		return fmt.Errorf("patch config: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	return printIntegration(resp, output)
}

func runIntegrationRestart(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := resolveIntegrationID(ctx, c, args[0])
	if err != nil {
		return err
	}
	var resp map[string]any
	if err := c.PostJSON(ctx, "/api/integrations/"+id+"/restart?workspace_id="+url.QueryEscape(c.WorkspaceID),
		map[string]any{}, &resp); err != nil {
		return fmt.Errorf("restart: %w", err)
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runIntegrationRedeploy(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := resolveIntegrationID(ctx, c, args[0])
	if err != nil {
		return err
	}
	var resp map[string]any
	if err := c.PostJSON(ctx, "/api/integrations/"+id+"/redeploy?workspace_id="+url.QueryEscape(c.WorkspaceID),
		map[string]any{}, &resp); err != nil {
		return fmt.Errorf("redeploy: %w", err)
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runIntegrationDelete(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	id, err := resolveIntegrationID(ctx, c, args[0])
	if err != nil {
		return err
	}
	if err := c.DeleteJSON(ctx, "/api/integrations/"+id+"?workspace_id="+url.QueryEscape(c.WorkspaceID)); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	fmt.Fprintln(os.Stdout, "deleted")
	return nil
}

// --- output helpers ---

func printIntegration(row map[string]any, output string) error {
	if output == "json" {
		return cli.PrintJSON(os.Stdout, row)
	}
	headers := []string{"ID", "NAME", "KIND", "STATUS", "VERSION"}
	cli.PrintTable(os.Stdout, headers, [][]string{{
		strVal(row, "id"),
		strVal(row, "name"),
		strVal(row, "kind"),
		strVal(row, "status"),
		fmt.Sprint(row["version"]),
	}})
	return nil
}

func getString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}
