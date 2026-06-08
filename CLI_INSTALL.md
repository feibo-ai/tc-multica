# Multica CLI — Installation Guide for AI Agents

> **Audience:** This document is designed for AI agents (Claude Code, Codex, etc.) to read and execute step-by-step. Each step includes the exact shell commands to run, expected outcomes, and error handling.

## One-Line Instruction for Users

Give this instruction to your AI agent:

```
Fetch https://github.com/feibo-ai/tc-multica/blob/main/CLI_INSTALL.md and follow the instructions to install Multica CLI, log in, and start the daemon on this machine.
```

---

## Step 1: Check if Multica CLI is already installed

Run:

```bash
multica version
```

- **If it prints a version string** (e.g. `multica v0.x.x`): it's already installed. To upgrade to the latest release, run `multica update` (it self-updates from GitHub Releases). Then skip to **Step 3**.
- **If command not found**: continue to **Step 2**.

---

## Step 2: Install the Multica CLI

> **Windows users:** Skip to [Option B: Windows (PowerShell)](#option-b-windows-powershell) below.

> **Do NOT use Homebrew.** The `multica-ai/tap` Homebrew formula belongs to the upstream open-source project and is **not** updated for this deployment's releases (which live on `feibo-ai/tc-multica`). `brew install` / `brew upgrade` would install a different, stale version that can't reach this deployment. Use the installer below, and `multica update` to upgrade.

### Option A: macOS / Linux

The installer detects your OS/architecture, downloads the latest release binary, installs it, and puts it on your PATH. Re-run it any time to upgrade.

```bash
curl -fsSL https://raw.githubusercontent.com/feibo-ai/tc-multica/main/scripts/install.sh | bash
```

Then verify:

```bash
multica version
```

If the version prints successfully, skip to **Step 3**.

**Manual fallback** (if piping to `bash` is not allowed) — download the archive directly:

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')   # "darwin" or "linux"
ARCH=$(uname -m)                                # "x86_64" or "arm64"

# Normalize architecture name
if [ "$ARCH" = "x86_64" ]; then
  ARCH="amd64"
fi

# Get the latest release tag from GitHub
LATEST=$(curl -sI https://github.com/feibo-ai/tc-multica/releases/latest | grep -i '^location:' | sed 's/.*tag\///' | tr -d '\r\n')

# Download and extract
VERSION="${LATEST#v}"
curl -sL "https://github.com/feibo-ai/tc-multica/releases/download/${LATEST}/multica-cli-${VERSION}-${OS}-${ARCH}.tar.gz" -o /tmp/multica.tar.gz
tar -xzf /tmp/multica.tar.gz -C /tmp multica
sudo mv /tmp/multica /usr/local/bin/multica
rm /tmp/multica.tar.gz
```

Verify:

```bash
multica version
```

**If this fails:**
- Check that `/usr/local/bin` is in `$PATH`.
- On Linux, you may need `chmod +x /usr/local/bin/multica`.
- If `sudo` is not available, install to a user-writable directory: `mv /tmp/multica ~/.local/bin/multica` and ensure `~/.local/bin` is in `$PATH`.

### Option B: Windows (PowerShell)

Run in PowerShell (no admin required):

```powershell
irm https://raw.githubusercontent.com/feibo-ai/tc-multica/main/scripts/install.ps1 | iex
```

This downloads the latest Windows binary from GitHub Releases, installs it to `%USERPROFILE%\.multica\bin\`, and adds it to your user PATH. Re-run it any time to upgrade.

Verify:

```powershell
multica version
```

**If this fails:**
- Restart your terminal so the updated PATH takes effect.
- If your execution policy blocks the script: `Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned` then re-run.

---

## Step 3: Log in

Run:

```bash
multica login
```

**Important:** This command opens a browser window for OAuth authentication. Tell the user:

> "A browser window will open for Multica login. Please complete the authentication in your browser, then come back here."

Wait for the command to complete. It will automatically discover and watch all workspaces the user belongs to.

Verify:

```bash
multica auth status
```

Expected output should show the authenticated user and server URL.

**If login fails:**
- If no browser is available (headless environment), the user can generate a Personal Access Token at `https://app.multica.ai/settings` and run: `multica login --token <mul_...>` (use `--token=` with an empty value to be prompted interactively).
- If the server URL needs to be customized: `multica config set server_url <url>` before logging in.

---

## Step 4: Start the daemon

First, check if the daemon is already running:

```bash
multica daemon status
```

- **If status is "running"**: skip to **Step 5**.
- **If status is "stopped"**: start it:

```bash
multica daemon start
```

Wait 3 seconds, then verify:

```bash
multica daemon status
```

Expected output should show `running` status with detected agents (e.g. `claude`, `codex`, `copilot`, `opencode`, `openclaw`, `hermes`, `gemini`, `pi`, `cursor-agent`).

**If daemon fails to start:**
- Check logs: `multica daemon logs`
- If a port conflict occurs, the daemon may already be running under a different profile.
- If no agents are detected, ensure at least one AI CLI (`claude`, `codex`, `copilot`, `opencode`, `openclaw`, `hermes`, `gemini`, `pi`, or `cursor-agent`) is installed and on the `$PATH`.

---

## Step 5: Verify everything is working

Run:

```bash
multica daemon status
```

Confirm:
1. Status is `running`
2. At least one agent is listed (e.g. `claude`, `codex`, `copilot`, `opencode`, `openclaw`, `hermes`, `gemini`, `pi`, or `cursor-agent`)
3. At least one workspace is being watched

If the agents list is empty, tell the user:

> "The Multica daemon is running but no AI agent CLIs were detected. Please install at least one supported CLI (`claude`, `codex`, `copilot`, `opencode`, `openclaw`, `hermes`, `gemini`, `pi`, or `cursor-agent`), then restart the daemon with `multica daemon stop && multica daemon start`."

---

## Summary

When all steps are complete, inform the user:

> "Multica CLI is installed and the daemon is running. Agents in your workspaces can now execute tasks on this machine. You can manage workspaces with `multica workspace list` and view daemon logs with `multica daemon logs -f`."
