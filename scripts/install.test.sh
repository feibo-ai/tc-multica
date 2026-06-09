#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Build a self-contained sandbox with a stub `curl` and a tarball that the
# release-binary install path downloads. The installer is binary-only (no
# Homebrew), so the test exercises the GitHub Releases download path end to end.
_setup_sandbox() {
  local tmp="$1"
  local stub_bin="$tmp/stub-bin"
  local install_bin="$tmp/install-bin"
  local payload_dir="$tmp/payload"
  mkdir -p "$stub_bin" "$install_bin" "$payload_dir"

  cat >"$payload_dir/multica" <<'STUB'
#!/usr/bin/env bash
echo "multica v0.3.2 (commit: test)"
STUB
  chmod +x "$payload_dir/multica"
  tar -czf "$tmp/multica.tar.gz" -C "$payload_dir" multica

  cat >"$stub_bin/curl" <<'STUB'
#!/usr/bin/env bash
if [[ "$*" == *"-sI"* ]]; then
  printf 'HTTP/2 302\r\nlocation: https://github.com/feibo-ai/tc-multica/releases/tag/v0.3.2\r\n'
  exit 0
fi

out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

if [[ -z "$out" ]]; then
  echo "stub curl expected -o" >&2
  exit 2
fi
cp "$MULTICA_TEST_ARCHIVE" "$out"
STUB
  chmod +x "$stub_bin/curl"
}

_run_installer() {
  local tmp="$1"
  local out="$tmp/install.out"
  local err="$tmp/install.err"
  if ! PATH="$tmp/stub-bin:$tmp/install-bin:/usr/bin:/bin" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_ARCHIVE="$tmp/multica.tar.gz" \
    bash "$ROOT_DIR/scripts/install.sh" >"$out" 2>"$err"; then
    echo "install.sh exited non-zero" >&2
    cat "$out" >&2 || true
    cat "$err" >&2 || true
    return 1
  fi

  if [[ ! -x "$tmp/install-bin/multica" ]]; then
    echo "expected installed binary at $tmp/install-bin/multica" >&2
    cat "$out" >&2 || true
    cat "$err" >&2 || true
    return 1
  fi
}

test_installs_release_binary() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  _run_installer "$tmp"
}

# Write a richer stub `multica` that logs every invocation to $calls and reports
# a fresh (workspace empty) or configured (workspace set) state via `config show`.
_write_logging_multica() {
  local payload="$1" calls="$2" ws="$3"
  cat >"$payload/multica" <<STUB
#!/usr/bin/env bash
echo "\$@" >> "$calls"
case "\$1" in
  version) echo "multica v0.3.2 (commit: test)" ;;
  config)  [ "\$2" = show ] && printf 'server_url:   (not set)\nworkspace_id: %s\n' "$ws" ;;
  login)
    # The real CLI reads the token from stdin when --token has no value;
    # capture it so the test can prove the secret never rode argv.
    IFS= read -r piped || true
    printf 'login-stdin=%s\n' "\$piped" >> "$calls"
    echo "logged in"
    ;;
esac
exit 0
STUB
  chmod +x "$payload/multica"
  tar -czf "$payload/../multica.tar.gz" -C "$payload" multica
}

# ⑫ onboarding: with MULTICA_TOKEN in env, install.sh configures non-interactively
# via `multica login --token` (which auto-discovers the workspace) — one command,
# zero manual config.json editing.
test_env_onboarding_runs_token_login() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  _write_logging_multica "$tmp/payload" "$tmp/calls" "(not set)" # fresh (real sentinel)

  PATH="$tmp/stub-bin:$tmp/install-bin:/usr/bin:/bin" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_ARCHIVE="$tmp/multica.tar.gz" \
    MULTICA_TOKEN="mul_test123" \
    bash "$ROOT_DIR/scripts/install.sh" >"$tmp/out" 2>"$tmp/err" || {
    echo "installer exited non-zero" >&2
    cat "$tmp/err" >&2
    return 1
  }

  # Token must arrive via stdin (secret out of argv) ...
  if ! grep -q "login-stdin=mul_test123" "$tmp/calls"; then
    echo "expected token via stdin (login-stdin=mul_test123); calls were:" >&2
    cat "$tmp/calls" >&2 || true
    return 1
  fi
  # ... and must NOT appear as a flag value in argv.
  if grep -q "login --token mul_test123" "$tmp/calls"; then
    echo "token leaked into argv (ps-visible); calls were:" >&2
    cat "$tmp/calls" >&2 || true
    return 1
  fi
}

# ⑫ onboarding: a machine that already has a workspace configured must be left
# untouched — no token login, even if MULTICA_TOKEN happens to be set.
test_configured_machine_skips_onboarding() {
  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  _setup_sandbox "$tmp"
  # Already configured: a real UUID workspace_id (not the (not set) sentinel).
  _write_logging_multica "$tmp/payload" "$tmp/calls" "11111111-1111-1111-1111-111111111111"

  PATH="$tmp/stub-bin:$tmp/install-bin:/usr/bin:/bin" \
    MULTICA_BIN_DIR="$tmp/install-bin" \
    MULTICA_TEST_ARCHIVE="$tmp/multica.tar.gz" \
    MULTICA_TOKEN="mul_should_not_be_used" \
    bash "$ROOT_DIR/scripts/install.sh" >"$tmp/out" 2>"$tmp/err" || {
    echo "installer exited non-zero" >&2
    cat "$tmp/err" >&2
    return 1
  }

  if grep -q "^login" "$tmp/calls"; then
    echo "configured machine should NOT run login; calls were:" >&2
    cat "$tmp/calls" >&2 || true
    return 1
  fi
}

test_installs_release_binary
test_env_onboarding_runs_token_login
test_configured_machine_skips_onboarding
echo "install.sh tests passed"
