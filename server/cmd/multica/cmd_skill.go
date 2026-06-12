package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Work with skills",
}

var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "List skills in the workspace",
	RunE:  runSkillList,
}

var skillGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Get skill details (includes files)",
	Args:  exactArgs(1),
	RunE:  runSkillGet,
}

var skillCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new skill",
	RunE:  runSkillCreate,
}

var skillUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a skill",
	Args:  exactArgs(1),
	RunE:  runSkillUpdate,
}

var skillDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a skill",
	Args:  exactArgs(1),
	RunE:  runSkillDelete,
}

var skillImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import a skill from a URL (clawhub.ai, skills.sh, or github.com)",
	RunE:  runSkillImport,
}

var skillTouchReviewedCmd = &cobra.Command{
	Use:   "touch-reviewed <id>",
	Short: "Mark a skill as reviewed today (resets the 90-day stale clock)",
	Args:  exactArgs(1),
	RunE:  runSkillTouchReviewed,
}

var skillPullCmd = &cobra.Command{
	Use:   "pull [<name-or-id>]",
	Short: "Pull skill(s) from the registry to a local dir (reconstructs SKILL.md + bundled files)",
	Long: "Download a skill (or --all) from the workspace registry into a local skills directory " +
		"(default ~/.claude/skills). Each skill becomes <dir>/<name>/SKILL.md plus its bundled files " +
		"(e.g. scripts, assets) at their stored paths — so agents get the executable skill, not just the doc. " +
		"This is the self-update path for skills, mirroring `multica update` for the CLI binary.",
	RunE: runSkillPull,
}

var skillLintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Lint local skills (frontmatter + body token budget)",
	Long: "Check every <dir>/<name>/SKILL.md: ERROR on missing name/description or body over the " +
		"2000-token budget; WARN on missing owner/last_reviewed_at or a review older than 90 days. " +
		"Exits non-zero if any skill has an error. Mirrors the MCP skill_lint as a local CLI check.",
	RunE: runSkillLint,
}

var skillSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for installable skills",
	Args:  exactArgs(1),
	RunE:  runSkillSearch,
}

// Skill file subcommands.

var skillFilesCmd = &cobra.Command{
	Use:   "files",
	Short: "Work with skill files",
}

var skillFilesListCmd = &cobra.Command{
	Use:   "list <skill-id>",
	Short: "List files for a skill",
	Args:  exactArgs(1),
	RunE:  runSkillFilesList,
}

var skillFilesUpsertCmd = &cobra.Command{
	Use:   "upsert <skill-id>",
	Short: "Create or update a skill file",
	Args:  exactArgs(1),
	RunE:  runSkillFilesUpsert,
}

var skillFilesDeleteCmd = &cobra.Command{
	Use:   "delete <skill-id> <file-id>",
	Short: "Delete a skill file",
	Args:  exactArgs(2),
	RunE:  runSkillFilesDelete,
}

func init() {
	skillCmd.AddCommand(skillListCmd)
	skillCmd.AddCommand(skillGetCmd)
	skillCmd.AddCommand(skillCreateCmd)
	skillCmd.AddCommand(skillUpdateCmd)
	skillCmd.AddCommand(skillDeleteCmd)
	skillCmd.AddCommand(skillImportCmd)
	skillCmd.AddCommand(skillTouchReviewedCmd)
	skillCmd.AddCommand(skillPullCmd)
	skillCmd.AddCommand(skillLintCmd)
	skillCmd.AddCommand(skillSearchCmd)
	skillCmd.AddCommand(skillFilesCmd)

	skillFilesCmd.AddCommand(skillFilesListCmd)
	skillFilesCmd.AddCommand(skillFilesUpsertCmd)
	skillFilesCmd.AddCommand(skillFilesDeleteCmd)

	// skill list
	skillListCmd.Flags().String("output", "table", "Output format: table or json")
	skillListCmd.Flags().Bool("stale", false, "Only show skills not reviewed in 90 days")

	// skill get
	skillGetCmd.Flags().String("output", "json", "Output format: table or json")

	// skill pull
	skillPullCmd.Flags().Bool("all", false, "Pull every skill in the workspace")
	skillPullCmd.Flags().String("dir", "", "Target skills directory (default ~/.claude/skills)")
	skillPullCmd.Flags().String("output", "json", "Output format: table or json")

	// skill lint
	skillLintCmd.Flags().String("dir", "", "Skills directory to lint (default ~/.claude/skills)")
	skillLintCmd.Flags().String("output", "table", "Output format: table or json")

	// skill create
	skillCreateCmd.Flags().String("name", "", "Skill name (required)")
	skillCreateCmd.Flags().String("description", "", "Skill description")
	skillCreateCmd.Flags().String("content", "", "Skill content (SKILL.md body)")
	skillCreateCmd.Flags().Bool("content-stdin", false, "Read skill content from stdin. Mutually exclusive with --content and --content-file.")
	skillCreateCmd.Flags().String("content-file", "", "Read skill content from a UTF-8 file. Mutually exclusive with --content and --content-stdin.")
	skillCreateCmd.Flags().String("config", "", "Skill config as JSON string")
	skillCreateCmd.Flags().String("owner", "", "User UUID to set as owner (SOP ❌5)")
	skillCreateCmd.Flags().String("output", "json", "Output format: table or json")

	// skill update
	skillUpdateCmd.Flags().String("name", "", "New name")
	skillUpdateCmd.Flags().String("description", "", "New description")
	skillUpdateCmd.Flags().String("content", "", "New content")
	skillUpdateCmd.Flags().Bool("content-stdin", false, "Read new content from stdin. Mutually exclusive with --content and --content-file.")
	skillUpdateCmd.Flags().String("content-file", "", "Read new content from a UTF-8 file. Mutually exclusive with --content and --content-stdin.")
	skillUpdateCmd.Flags().String("config", "", "New config as JSON string")
	skillUpdateCmd.Flags().String("owner", "", "Set owner user UUID (use empty string to clear)")
	skillUpdateCmd.Flags().String("output", "json", "Output format: table or json")

	// skill delete
	skillDeleteCmd.Flags().Bool("yes", false, "Skip confirmation prompt")

	// skill import
	skillImportCmd.Flags().String("url", "", "URL to import from (required)")
	skillImportCmd.Flags().String("on-conflict", "fail", "Conflict strategy when a skill with the same name exists: fail, overwrite, rename, or skip")
	skillImportCmd.Flags().String("output", "json", "Output format: table or json")

	// skill touch-reviewed
	skillTouchReviewedCmd.Flags().String("output", "text", "Output format: text or json")

	// skill search
	skillSearchCmd.Flags().String("output", "json", "Output format: table or json")

	// skill files list
	skillFilesListCmd.Flags().String("output", "table", "Output format: table or json")

	// skill files upsert
	skillFilesUpsertCmd.Flags().String("path", "", "File path within the skill (required)")
	skillFilesUpsertCmd.Flags().String("content", "", "File content (required)")
	skillFilesUpsertCmd.Flags().Bool("content-stdin", false, "Read file content from stdin. Mutually exclusive with --content and --content-file.")
	skillFilesUpsertCmd.Flags().String("content-file", "", "Read file content from a UTF-8 file. Mutually exclusive with --content and --content-stdin.")
	skillFilesUpsertCmd.Flags().String("output", "json", "Output format: table or json")
}

// ---------------------------------------------------------------------------
// Skill commands
// ---------------------------------------------------------------------------

// resolveSkillContentFlag intentionally stays separate from resolveTextFlag.
// Skill bodies are Markdown documents where byte-level preservation matters:
// inline --content is not backslash-unescaped, and stdin/file input is not
// trimmed, so agents can round-trip generated SKILL.md content exactly.
func resolveSkillContentFlag(cmd *cobra.Command) (string, bool, error) {
	useStdin, _ := cmd.Flags().GetBool("content-stdin")
	inline, _ := cmd.Flags().GetString("content")
	filePath, _ := cmd.Flags().GetString("content-file")
	inlineSet := cmd.Flags().Changed("content")

	sources := 0
	if inlineSet {
		sources++
	}
	if useStdin {
		sources++
	}
	if filePath != "" {
		sources++
	}
	if sources > 1 {
		return "", false, fmt.Errorf("--content, --content-stdin, and --content-file are mutually exclusive")
	}

	if useStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", false, fmt.Errorf("read stdin for --content-stdin: %w", err)
		}
		return skillContentBytesToString(data, "stdin content for --content-stdin")
	}
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", false, fmt.Errorf("read file for --content-file: %w", err)
		}
		return skillContentBytesToString(data, "file content for --content-file")
	}
	if inlineSet {
		return inline, true, nil
	}
	return "", false, nil
}

func skillContentBytesToString(data []byte, label string) (string, bool, error) {
	if len(data) == 0 {
		return "", false, fmt.Errorf("%s is empty", label)
	}
	if !utf8.Valid(data) {
		return "", false, fmt.Errorf("%s must be valid UTF-8", label)
	}
	return string(data), true, nil
}

func runSkillList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	path := "/api/skills"
	if stale, _ := cmd.Flags().GetBool("stale"); stale {
		path += "?stale=true"
	}

	var skills []map[string]any
	if err := client.GetJSON(ctx, path, &skills); err != nil {
		return fmt.Errorf("list skills: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, skills)
	}

	headers := []string{"ID", "NAME", "DESCRIPTION", "CREATED_AT"}
	rows := make([][]string, 0, len(skills))
	for _, s := range skills {
		rows = append(rows, []string{
			strVal(s, "id"),
			strVal(s, "name"),
			strVal(s, "description"),
			strVal(s, "created_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runSkillGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var skill map[string]any
	if err := client.GetJSON(ctx, "/api/skills/"+args[0], &skill); err != nil {
		return fmt.Errorf("get skill: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, skill)
	}

	headers := []string{"ID", "NAME", "DESCRIPTION", "CREATED_AT"}
	rows := [][]string{{
		strVal(skill, "id"),
		strVal(skill, "name"),
		strVal(skill, "description"),
		strVal(skill, "created_at"),
	}}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

// buildSkillMd reconstructs a SKILL.md (YAML frontmatter + body) from a skill
// record's name/description/last_reviewed_at + content. The registry stores the
// body in `content`; the frontmatter is rebuilt so the pulled file is a valid,
// discoverable skill.
func buildSkillMd(skill map[string]any) string {
	desc := strVal(skill, "description")
	desc = strings.ReplaceAll(desc, "\\", "\\\\")
	desc = strings.ReplaceAll(desc, "\"", "\\\"")
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + strVal(skill, "name") + "\n")
	b.WriteString("description: \"" + desc + "\"\n")
	// owner:把 skill 记录的关系字段 owner_user_id 写进 frontmatter,
	// 使 `skill pull` 后 `skill lint` 不再警告缺 owner(SOP ❌5)。
	if v := strVal(skill, "owner_user_id"); v != "" {
		b.WriteString("owner: " + v + "\n")
	}
	if v := strVal(skill, "last_reviewed_at"); v != "" {
		b.WriteString("last_reviewed_at: " + v + "\n")
	}
	b.WriteString("---\n")
	content := strVal(skill, "content")
	if !strings.HasPrefix(content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func runSkillPull(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dir, _ := cmd.Flags().GetString("dir")
	if dir == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return fmt.Errorf("resolve home dir: %w", herr)
		}
		dir = filepath.Join(home, ".claude", "skills")
	}

	all, _ := cmd.Flags().GetBool("all")
	var ids []string
	switch {
	case all:
		var skills []map[string]any
		if err := client.GetJSON(ctx, "/api/skills", &skills); err != nil {
			return fmt.Errorf("list skills: %w", err)
		}
		for _, s := range skills {
			if id := strVal(s, "id"); id != "" {
				ids = append(ids, id)
			}
		}
	case len(args) == 1:
		id := args[0]
		if !looksLikeUUID(id) {
			var skills []map[string]any
			if err := client.GetJSON(ctx, "/api/skills", &skills); err != nil {
				return fmt.Errorf("list skills to resolve name: %w", err)
			}
			found := ""
			for _, s := range skills {
				if strVal(s, "name") == id {
					found = strVal(s, "id")
					break
				}
			}
			if found == "" {
				return fmt.Errorf("no skill with id or name %q in workspace", id)
			}
			id = found
		}
		ids = []string{id}
	default:
		return fmt.Errorf("provide a skill <name-or-id>, or --all")
	}

	pulled := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		var skill map[string]any
		if err := client.GetJSON(ctx, "/api/skills/"+id, &skill); err != nil {
			return fmt.Errorf("get skill %s: %w", id, err)
		}
		name := strVal(skill, "name")
		if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
			return fmt.Errorf("skill %s has an unsafe or empty name %q", id, name)
		}
		skillDir := filepath.Join(dir, name)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", skillDir, err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(buildSkillMd(skill)), 0o644); err != nil {
			return fmt.Errorf("write SKILL.md: %w", err)
		}
		nfiles := 0
		if raw, ok := skill["files"].([]any); ok {
			for _, fa := range raw {
				f, ok := fa.(map[string]any)
				if !ok {
					continue
				}
				p := strVal(f, "path")
				// Path safety: stored paths are skill-relative; reject traversal/absolute.
				if p == "" || p == "SKILL.md" || strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
					continue
				}
				fp := filepath.Join(skillDir, filepath.FromSlash(p))
				if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
					return fmt.Errorf("create dir for %s: %w", p, err)
				}
				if err := os.WriteFile(fp, []byte(strVal(f, "content")), 0o644); err != nil {
					return fmt.Errorf("write %s: %w", p, err)
				}
				nfiles++
			}
		}
		fmt.Fprintf(os.Stderr, "Pulled %s -> %s (SKILL.md + %d file(s))\n", name, skillDir, nfiles)
		pulled = append(pulled, map[string]any{"name": name, "dir": skillDir, "files": nfiles})
	}

	if output, _ := cmd.Flags().GetString("output"); output == "table" {
		return nil
	}
	return cli.PrintJSON(os.Stdout, pulled)
}

// estimateSkillTokens mirrors the MCP estimateTokens: floor(words * 1.3), where
// words is the whitespace-split, non-empty token count of the SKILL.md body.
func estimateSkillTokens(body string) int {
	return len(strings.Fields(body)) * 13 / 10
}

// parseSkillFrontmatter splits a SKILL.md into its YAML frontmatter (as a flat
// key→value map; values have surrounding quotes trimmed) and the body after the
// closing fence. Missing/malformed frontmatter yields an empty map + full text.
func parseSkillFrontmatter(md string) (map[string]string, string) {
	data := map[string]string{}
	if !strings.HasPrefix(md, "---\n") {
		return data, md
	}
	rest := md[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return data, md
	}
	block := rest[:end]
	body := rest[end+len("\n---"):]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	} else {
		body = ""
	}
	for _, line := range strings.Split(block, "\n") {
		c := strings.IndexByte(line, ':')
		if c <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:c])
		v := strings.TrimSpace(line[c+1:])
		v = strings.Trim(v, "\"")
		if k != "" {
			data[k] = v
		}
	}
	return data, body
}

func runSkillLint(cmd *cobra.Command, _ []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	if dir == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return fmt.Errorf("resolve home dir: %w", herr)
		}
		dir = filepath.Join(home, ".claude", "skills")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read skills dir %s: %w", dir, err)
	}

	type finding struct {
		Skill    string   `json:"skill"`
		Tokens   int      `json:"tokens"`
		Errors   []string `json:"errors"`
		Warnings []string `json:"warnings"`
	}
	findings := make([]finding, 0, len(entries))
	anyError := false

	for _, e := range entries {
		name := e.Name()
		// follow symlinked skill dirs (the team installs skills as symlinks)
		info, statErr := os.Stat(filepath.Join(dir, name))
		if statErr != nil || !info.IsDir() {
			continue
		}
		text, readErr := os.ReadFile(filepath.Join(dir, name, "SKILL.md"))
		if readErr != nil {
			continue
		}
		fm, body := parseSkillFrontmatter(string(text))
		var errs, warns []string
		if fm["name"] == "" {
			errs = append(errs, "missing frontmatter: name")
		}
		if fm["description"] == "" {
			errs = append(errs, "missing frontmatter: description")
		}
		if fm["owner"] == "" {
			warns = append(warns, "missing frontmatter: owner (SOP ❌5)")
		}
		if fm["last_reviewed_at"] == "" {
			warns = append(warns, "missing frontmatter: last_reviewed_at (SOP ❌5)")
		} else if t, perr := time.Parse("2006-01-02", fm["last_reviewed_at"]); perr == nil {
			if days := int(time.Since(t).Hours() / 24); days > 90 {
				warns = append(warns, fmt.Sprintf("last_reviewed_at is %d days old (>90)", days))
			}
		}
		tokens := estimateSkillTokens(body)
		if tokens > 2000 {
			errs = append(errs, fmt.Sprintf("body ~%d tokens (hard limit 2000)", tokens))
		}
		if len(errs) > 0 {
			anyError = true
		}
		findings = append(findings, finding{Skill: name, Tokens: tokens, Errors: errs, Warnings: warns})
	}

	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		if err := cli.PrintJSON(os.Stdout, findings); err != nil {
			return err
		}
	} else {
		headers := []string{"SKILL", "TOKENS", "ERRORS", "WARNINGS"}
		rows := make([][]string, 0, len(findings))
		for _, f := range findings {
			rows = append(rows, []string{
				f.Skill,
				fmt.Sprintf("%d", f.Tokens),
				strings.Join(f.Errors, "; "),
				strings.Join(f.Warnings, "; "),
			})
		}
		cli.PrintTable(os.Stdout, headers, rows)
	}
	if anyError {
		return fmt.Errorf("skill lint found errors")
	}
	return nil
}

func runSkillCreate(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	name, _ := cmd.Flags().GetString("name")
	if name == "" {
		return fmt.Errorf("--name is required")
	}

	body := map[string]any{
		"name": name,
	}
	if v, _ := cmd.Flags().GetString("description"); v != "" {
		body["description"] = v
	}
	content, hasContent, err := resolveSkillContentFlag(cmd)
	if err != nil {
		return err
	}
	if hasContent && content != "" {
		body["content"] = content
	}
	if cmd.Flags().Changed("config") {
		v, _ := cmd.Flags().GetString("config")
		var config any
		if err := json.Unmarshal([]byte(v), &config); err != nil {
			return fmt.Errorf("--config must be valid JSON: %w", err)
		}
		body["config"] = config
	}
	if v, _ := cmd.Flags().GetString("owner"); v != "" {
		body["owner_user_id"] = v
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/skills", body, &result); err != nil {
		return fmt.Errorf("create skill: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("Skill created: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

func runSkillUpdate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		body["name"] = v
	}
	if cmd.Flags().Changed("description") {
		v, _ := cmd.Flags().GetString("description")
		body["description"] = v
	}
	content, hasContent, err := resolveSkillContentFlag(cmd)
	if err != nil {
		return err
	}
	if hasContent {
		body["content"] = content
	}
	if cmd.Flags().Changed("config") {
		v, _ := cmd.Flags().GetString("config")
		var config any
		if err := json.Unmarshal([]byte(v), &config); err != nil {
			return fmt.Errorf("--config must be valid JSON: %w", err)
		}
		body["config"] = config
	}
	if cmd.Flags().Changed("owner") {
		v, _ := cmd.Flags().GetString("owner")
		// Pass through empty string explicitly; backend treats "" as "clear".
		body["owner_user_id"] = v
	}

	if len(body) == 0 {
		return fmt.Errorf("no fields to update; use --name, --description, --content, --config, or --owner")
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/skills/"+args[0], body, &result); err != nil {
		return fmt.Errorf("update skill: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("Skill updated: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

func runSkillDelete(cmd *cobra.Command, args []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes {
		fmt.Printf("Are you sure you want to delete skill %s? This cannot be undone. [y/N] ", args[0])
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if err := client.DeleteJSON(ctx, "/api/skills/"+args[0]); err != nil {
		return fmt.Errorf("delete skill: %w", err)
	}

	fmt.Printf("Skill deleted: %s\n", args[0])
	return nil
}

func runSkillImport(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	importURL, _ := cmd.Flags().GetString("url")
	if importURL == "" {
		return fmt.Errorf("--url is required")
	}
	onConflict, _ := cmd.Flags().GetString("on-conflict")
	if !validSkillImportConflictStrategy(onConflict) {
		return fmt.Errorf("--on-conflict must be one of: fail, overwrite, rename, skip")
	}

	body := map[string]any{
		"url":         importURL,
		"on_conflict": onConflict,
	}

	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(60*time.Second))
	defer cancel()

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/skills/import", body, &result); err != nil {
		if handledErr := handleSkillImportError(cmd, err); handledErr != nil {
			return handledErr
		}
		return fmt.Errorf("import skill: %w", err)
	}

	return printSkillImportResult(cmd, result)
}

func validSkillImportConflictStrategy(strategy string) bool {
	switch strategy {
	case "fail", "overwrite", "rename", "skip":
		return true
	}
	return false
}

func handleSkillImportError(cmd *cobra.Command, err error) error {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || strings.TrimSpace(httpErr.Body) == "" {
		return nil
	}

	var body map[string]any
	if json.Unmarshal([]byte(httpErr.Body), &body) != nil {
		return nil
	}
	if _, ok := body["status"]; !ok {
		if _, hasExisting := body["existing_skill"]; !hasExisting {
			return nil
		}
		body = normalizeLegacySkillImportConflict(body)
	}

	if err := printSkillImportResult(cmd, body); err != nil {
		return err
	}
	reason := strVal(body, "reason")
	if reason == "" {
		reason = strVal(body, "error")
	}
	if reason == "" {
		reason = "skill import conflict"
	}
	return errors.New(reason)
}

func normalizeLegacySkillImportConflict(body map[string]any) map[string]any {
	reason := strVal(body, "error")
	if reason == "" {
		reason = "a skill with this name already exists"
	}
	reason += "; use --on-conflict overwrite to replace it or --on-conflict rename to import a copy"
	return map[string]any{
		"status":         "conflict",
		"reason":         reason,
		"existing_skill": body["existing_skill"],
	}
}

func printSkillImportResult(cmd *cobra.Command, result map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	status := strVal(result, "status")
	if status == "" {
		fmt.Printf("Skill imported: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
		return nil
	}

	skill := nestedMap(result, "skill")
	existing := nestedMap(result, "existing_skill")
	reason := strVal(result, "reason")
	switch status {
	case "created":
		fmt.Printf("Skill imported: %s (%s)\n", strVal(skill, "name"), strVal(skill, "id"))
	case "updated":
		fmt.Printf("Skill updated: %s (%s)\n", strVal(skill, "name"), strVal(skill, "id"))
	case "skipped":
		fmt.Printf("Skill skipped: %s (%s)\n", strVal(existing, "name"), strVal(existing, "id"))
	case "conflict":
		fmt.Printf("Skill import conflict: %s (%s)\n", strVal(existing, "name"), strVal(existing, "id"))
	case "failed":
		fmt.Printf("Skill import failed: %s\n", reason)
	default:
		fmt.Printf("Skill import %s\n", status)
	}
	if reason != "" && status != "failed" {
		fmt.Printf("Reason: %s\n", reason)
	}
	return nil
}

func nestedMap(m map[string]any, key string) map[string]any {
	nested, _ := m[key].(map[string]any)
	if nested == nil {
		return map[string]any{}
	}
	return nested
}

func runSkillSearch(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	query := strings.TrimSpace(args[0])
	if query == "" {
		return fmt.Errorf("query is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), cli.AtLeastAPITimeout(60*time.Second))
	defer cancel()

	var results []map[string]any
	path := "/api/skills/search?q=" + url.QueryEscape(query)
	if err := client.GetJSON(ctx, path, &results); err != nil {
		return fmt.Errorf("search skills: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, results)
	}

	headers := []string{"NAME", "URL", "SOURCE", "INSTALLS", "DESCRIPTION"}
	rows := make([][]string, 0, len(results))
	for _, result := range results {
		rows = append(rows, []string{
			strVal(result, "name"),
			strVal(result, "url"),
			strVal(result, "source"),
			strVal(result, "install_count"),
			strVal(result, "description"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runSkillTouchReviewed(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/skills/"+args[0]+"/touch-reviewed", nil, &result); err != nil {
		return fmt.Errorf("touch-reviewed: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	fmt.Printf("Skill %s marked reviewed at %s\n", strVal(result, "id"), strVal(result, "last_reviewed_at"))
	return nil
}

// ---------------------------------------------------------------------------
// Skill file subcommands
// ---------------------------------------------------------------------------

func runSkillFilesList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var files []map[string]any
	if err := client.GetJSON(ctx, "/api/skills/"+args[0]+"/files", &files); err != nil {
		return fmt.Errorf("list skill files: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, files)
	}

	headers := []string{"ID", "PATH", "CREATED_AT", "UPDATED_AT"}
	rows := make([][]string, 0, len(files))
	for _, f := range files {
		rows = append(rows, []string{
			strVal(f, "id"),
			strVal(f, "path"),
			strVal(f, "created_at"),
			strVal(f, "updated_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func runSkillFilesUpsert(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	filePath, _ := cmd.Flags().GetString("path")
	if filePath == "" {
		return fmt.Errorf("--path is required")
	}
	content, hasContent, err := resolveSkillContentFlag(cmd)
	if err != nil {
		return err
	}
	if !hasContent || content == "" {
		return fmt.Errorf("--content is required")
	}

	body := map[string]any{
		"path":    filePath,
		"content": content,
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result map[string]any
	if err := client.PutJSON(ctx, "/api/skills/"+args[0]+"/files", body, &result); err != nil {
		return fmt.Errorf("upsert skill file: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("Skill file upserted: %s (%s)\n", strVal(result, "path"), strVal(result, "id"))
	return nil
}

func runSkillFilesDelete(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if err := client.DeleteJSON(ctx, "/api/skills/"+args[0]+"/files/"+args[1]); err != nil {
		return fmt.Errorf("delete skill file: %w", err)
	}

	fmt.Printf("Skill file deleted: %s\n", args[1])
	return nil
}
