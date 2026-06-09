package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// User namespace exists so the daemon-injected `## Requesting User` brief
// has a CLI surface a human can mirror without having to construct
// PATCH /api/me by hand. Today only profile-description is wired; future
// per-user knobs (e.g. preferred language) should land as further
// subcommands here rather than expand the verb surface elsewhere.

var userCmd = &cobra.Command{
	Use:   "user",
	Short: "Work with your user account",
}

var userProfileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Get or update your personal profile",
	Long: "Manage the personal profile that agents see when they pick up a task " +
		"on your behalf. The description is injected into the agent brief under " +
		"`## Requesting User`, so use it to share role, stack, and collaboration " +
		"preferences.",
}

var userProfileGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show your current user profile",
	RunE:  runUserProfileGet,
}

var userCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new user and add to the current workspace (admin-only)",
	Long: "Self-host operators use this to onboard service users (e.g. autopilot-bot) " +
		"without going through the email-verification flow. If the user already " +
		"exists with the same email, only the workspace membership is added.",
	RunE: runUserCreate,
}

var userListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspace users with id / name / email / role",
	Long: "List the users in the current workspace with their UUIDs. This is the " +
		"resolution source for turning a human owner name into the user UUID that " +
		"`skill create/update --owner` and frontmatter `owner:` require (SOP ❌5).",
	RunE: runUserList,
}

var userProfileUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update your user profile (currently: profile description)",
	Long: "Set the personal profile description that gets injected into agent " +
		"briefs as `## Requesting User`. Pass an empty value to clear it.\n\n" +
		"Pick the input mode that preserves your content:\n" +
		"  --description \"...\"          inline (decodes \\n / \\t escapes)\n" +
		"  --description-stdin           pipe a HEREDOC (preserves verbatim)\n" +
		"  --description-file <path>     read a UTF-8 file (Windows-safe)\n",
	RunE: runUserProfileUpdate,
}

func init() {
	userCmd.AddCommand(userProfileCmd)
	userCmd.AddCommand(userCreateCmd)
	userCmd.AddCommand(userListCmd)
	userProfileCmd.AddCommand(userProfileGetCmd)
	userProfileCmd.AddCommand(userProfileUpdateCmd)

	userListCmd.Flags().String("output", "table", "Output format: table or json")

	userCreateCmd.Flags().String("email", "", "Service-user email address (required)")
	userCreateCmd.Flags().String("name", "", "Display name (defaults to local-part of email)")
	userCreateCmd.Flags().String("role", "member", "Workspace role: owner | admin | member")
	userCreateCmd.Flags().String("output", "json", "Output format: table or json")
	_ = userCreateCmd.MarkFlagRequired("email")

	userProfileGetCmd.Flags().String("output", "table", "Output format: table or json")

	userProfileUpdateCmd.Flags().String("description", "", "New profile description (decodes \\n, \\r, \\t, \\\\; pipe via --description-stdin to preserve literal backslashes)")
	userProfileUpdateCmd.Flags().Bool("description-stdin", false, "Read description from stdin (preserves multi-line content verbatim)")
	userProfileUpdateCmd.Flags().String("description-file", "", "Read description from a UTF-8 file (preserves multi-line content verbatim; use this on Windows when stdin piping mangles non-ASCII bytes)")
	userProfileUpdateCmd.Flags().Bool("clear", false, "Clear the profile description (equivalent to --description \"\")")
	userProfileUpdateCmd.Flags().String("output", "table", "Output format: table or json")
}

func runUserProfileGet(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var me map[string]any
	if err := client.GetJSON(ctx, "/api/me", &me); err != nil {
		return fmt.Errorf("get user profile: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, me)
	}

	printUserProfileTable(os.Stdout, me)
	return nil
}

func runUserProfileUpdate(cmd *cobra.Command, _ []string) error {
	// `--clear` is its own flag (not "pass an empty string") because cobra's
	// default value for a Changed("") flag would otherwise be ambiguous with
	// "user typed `--description ""`". Keep both forms supported — the inline
	// empty string is what someone scripting bash would reach for.
	clearFlag, _ := cmd.Flags().GetBool("clear")
	desc, hasDesc, err := resolveTextFlag(cmd, "description")
	if err != nil {
		return err
	}

	if clearFlag && hasDesc {
		return fmt.Errorf("--clear cannot be combined with --description / --description-stdin / --description-file")
	}
	if !clearFlag && !hasDesc && !cmd.Flags().Changed("description") {
		return fmt.Errorf("nothing to update; pass --description, --description-stdin, --description-file, or --clear")
	}

	if clearFlag {
		desc = ""
	}

	body := map[string]any{"profile_description": desc}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var me map[string]any
	if err := client.PatchJSON(ctx, "/api/me", body, &me); err != nil {
		return fmt.Errorf("update user profile: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, me)
	}

	printUserProfileTable(os.Stdout, me)
	return nil
}

func printUserProfileTable(out *os.File, me map[string]any) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	defer w.Flush()

	fmt.Fprintf(w, "ID\t%s\n", strVal(me, "id"))
	fmt.Fprintf(w, "NAME\t%s\n", strVal(me, "name"))
	fmt.Fprintf(w, "EMAIL\t%s\n", strVal(me, "email"))
	desc := strVal(me, "profile_description")
	if desc == "" {
		desc = "(not set)"
	}
	fmt.Fprintf(w, "PROFILE DESCRIPTION\t%s\n", desc)
}

func runUserCreate(cmd *cobra.Command, _ []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if _, err := requireWorkspaceID(cmd); err != nil {
		return err
	}

	email, _ := cmd.Flags().GetString("email")
	name, _ := cmd.Flags().GetString("name")
	role, _ := cmd.Flags().GetString("role")
	body := map[string]any{"email": email, "name": name, "role": role}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var resp map[string]any
	if err := c.PostJSON(ctx, "/api/admin/users", body, &resp); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		headers := []string{"USER_ID", "EMAIL", "NAME", "ROLE", "MEMBER_ID"}
		cli.PrintTable(os.Stdout, headers, [][]string{{
			strVal(resp, "user_id"), strVal(resp, "email"),
			strVal(resp, "name"), strVal(resp, "role"),
			strVal(resp, "member_id"),
		}})
		return nil
	}
	return cli.PrintJSON(os.Stdout, resp)
}

func runUserList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	wsID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}
	output, _ := cmd.Flags().GetString("output")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return listWorkspaceUsers(ctx, client, wsID, output, os.Stdout)
}

// listWorkspaceUsers fetches workspace members (id/name/email/role) and writes
// them as a table or JSON. Split from runUserList so it can be tested against an
// httptest server without going through CLI config resolution.
func listWorkspaceUsers(ctx context.Context, client *cli.APIClient, wsID, output string, out io.Writer) error {
	var members []map[string]any
	if err := client.GetJSON(ctx, "/api/workspaces/"+wsID+"/members", &members); err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	if output == "json" {
		return cli.PrintJSON(out, members)
	}
	cli.PrintTable(out, []string{"USER ID", "NAME", "EMAIL", "ROLE"}, userRows(members))
	return nil
}

// userRows maps workspace-member records to table rows (pure; unit-tested).
func userRows(members []map[string]any) [][]string {
	rows := make([][]string, 0, len(members))
	for _, m := range members {
		rows = append(rows, []string{
			strVal(m, "user_id"), strVal(m, "name"),
			strVal(m, "email"), strVal(m, "role"),
		})
	}
	return rows
}
