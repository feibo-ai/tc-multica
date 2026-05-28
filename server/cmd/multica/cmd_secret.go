package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// `multica secret` — control plane secret CLI (Plan 4 / PR D, Task D-11).
//
// Values are NEVER accepted on argv: shell history, /proc/<pid>/cmdline, and
// `ps` snapshots would leak them. The three accepted input channels are
// stdin (default), a file path, or — only for the rare interactive case — a
// terminal prompt with echo off. The `--value` flag does not exist.

var secretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Manage encrypted credentials attached to integrations (audited)",
}

var secretSetCmd = &cobra.Command{
	Use:   "set <KEY>",
	Short: "Create or rotate a secret (value read from stdin or --value-file)",
	Args:  exactArgs(1),
	RunE:  runSecretSet,
}

var secretRotateCmd = &cobra.Command{
	Use:   "rotate <KEY>",
	Short: "Alias for set — semantically clearer when replacing a value",
	Args:  exactArgs(1),
	RunE:  runSecretSet,
}

var secretListCmd = &cobra.Command{
	Use:   "list",
	Short: "List secret keys for an integration (no values)",
	RunE:  runSecretList,
}

var secretGetCmd = &cobra.Command{
	Use:   "get <KEY>",
	Short: "Print the decrypted value to stdout (every call is audited)",
	Args:  exactArgs(1),
	RunE:  runSecretGet,
}

var secretDeleteCmd = &cobra.Command{
	Use:   "delete <KEY>",
	Short: "Delete a secret",
	Args:  exactArgs(1),
	RunE:  runSecretDelete,
}

func init() {
	secretCmd.AddCommand(secretSetCmd)
	secretCmd.AddCommand(secretRotateCmd)
	secretCmd.AddCommand(secretListCmd)
	secretCmd.AddCommand(secretGetCmd)
	secretCmd.AddCommand(secretDeleteCmd)

	for _, c := range []*cobra.Command{secretSetCmd, secretRotateCmd, secretListCmd, secretGetCmd, secretDeleteCmd} {
		c.Flags().String("integration", "", "Integration id or name (required)")
		_ = c.MarkFlagRequired("integration")
	}

	secretSetCmd.Flags().Bool("value-stdin", true, "Read the value from stdin until EOF (default; opt out with --value-stdin=false + --value-file)")
	secretSetCmd.Flags().String("value-file", "", "Read the value from a file (mutually exclusive with --value-stdin)")

	secretRotateCmd.Flags().Bool("value-stdin", true, "Read the value from stdin until EOF (default)")
	secretRotateCmd.Flags().String("value-file", "", "Read the value from a file")

	secretListCmd.Flags().String("output", "table", "Output format: table or json")
	secretGetCmd.Flags().String("output", "value", "Output format: value (raw plaintext) or json")
}

// --- helpers ---

func readSecretValue(cmd *cobra.Command) (string, error) {
	filePath, _ := cmd.Flags().GetString("value-file")
	useStdin, _ := cmd.Flags().GetBool("value-stdin")

	switch {
	case filePath != "":
		raw, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read --value-file: %w", err)
		}
		return string(raw), nil
	case useStdin:
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		if len(raw) == 0 {
			return "", errors.New("empty value on stdin — pipe the secret in, e.g. `echo -n $TOKEN | multica secret set ...`")
		}
		return string(raw), nil
	default:
		return "", errors.New("provide --value-file PATH or pipe a value on stdin")
	}
}

func integrationParam(c *cli.APIClient, idOrName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return resolveIntegrationID(ctx, c, idOrName)
}

// --- commands ---

func runSecretSet(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	idOrName, _ := cmd.Flags().GetString("integration")
	integrationID, err := integrationParam(c, idOrName)
	if err != nil {
		return err
	}
	key := args[0]

	value, err := readSecretValue(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var resp map[string]any
	if err := c.PutJSON(ctx,
		"/api/integrations/"+integrationID+"/secrets/"+url.PathEscape(key)+"?workspace_id="+url.QueryEscape(c.WorkspaceID),
		map[string]any{"value": value}, &resp); err != nil {
		return fmt.Errorf("set secret: %w", err)
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runSecretList(cmd *cobra.Command, _ []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	idOrName, _ := cmd.Flags().GetString("integration")
	integrationID, err := integrationParam(c, idOrName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var rows []map[string]any
	if err := c.GetJSON(ctx,
		"/api/integrations/"+integrationID+"/secrets?workspace_id="+url.QueryEscape(c.WorkspaceID), &rows); err != nil {
		return fmt.Errorf("list secrets: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, rows)
	}
	headers := []string{"KEY", "VERSION", "UPDATED"}
	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		tableRows = append(tableRows, []string{
			strVal(r, "key"),
			fmt.Sprint(r["version"]),
			strVal(r, "updated_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, tableRows)
	return nil
}

func runSecretGet(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	idOrName, _ := cmd.Flags().GetString("integration")
	integrationID, err := integrationParam(c, idOrName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var resp map[string]any
	if err := c.GetJSON(ctx,
		"/api/integrations/"+integrationID+"/secrets/"+url.PathEscape(args[0])+"?workspace_id="+url.QueryEscape(c.WorkspaceID), &resp); err != nil {
		return fmt.Errorf("get secret: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	// Default: raw plaintext to stdout, no trailing newline, so the value can
	// be piped (`multica secret get FOO | other-tool`). The audit row is
	// already written server-side.
	fmt.Fprint(os.Stdout, strVal(resp, "value"))
	return nil
}

func runSecretDelete(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}
	idOrName, _ := cmd.Flags().GetString("integration")
	integrationID, err := integrationParam(c, idOrName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.DeleteJSON(ctx,
		"/api/integrations/"+integrationID+"/secrets/"+url.PathEscape(args[0])+"?workspace_id="+url.QueryEscape(c.WorkspaceID)); err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	fmt.Fprintln(os.Stdout, "deleted")
	return nil
}
