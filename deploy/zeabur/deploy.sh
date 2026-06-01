#!/usr/bin/env bash
#
# Lockstep deploy: point the named Zeabur image services at $TAG together.
#
# Why resolve by --id (not --name):
#   `zeabur service update tag --name X --env-id Y` does NOT work
#   non-interactively. The resolver calls util.GetServiceByName(), which needs
#   owner+project context that --env-id does not supply, so it fails with
#   "either id or ownerName, projectName, and name must be specified".
#   `zeabur service list --env-id Y` uses a different, env-scoped query that
#   works from the env id alone, so we list → map name→id → update by --id.
#
# Why we don't trust exit codes:
#   the zeabur CLI prints "ERROR ..." but still exits 0 on the resolution
#   failure above, so a naive `set -e` pipeline reports success on a no-op
#   deploy. We capture output and fail on any ERROR marker or an unresolved id.
#
# Usage:
#   ENV_ID=<env> TAG=<vX.Y.Z> [ZEABUR_TOKEN=<token>] deploy.sh <svc> [svc...]
#   - ZEABUR_TOKEN: set in CI to authenticate; omit when already logged in
#     locally (a prior `zeabur auth login`).
#   - service names default to "multica-backend multica-web" if none are passed.
set -euo pipefail

: "${ENV_ID:?ENV_ID (Zeabur environment id) is required}"
: "${TAG:?TAG (release tag, e.g. v0.4.5) is required}"

services=("$@")
if [ "${#services[@]}" -eq 0 ]; then
  services=(multica-backend multica-web)
fi

# Authenticate only when a token is provided (CI). Local runs rely on an
# existing `zeabur auth login`.
if [ -n "${ZEABUR_TOKEN:-}" ]; then
  zeabur auth login --token "$ZEABUR_TOKEN" --interactive=false
fi

# The CLI exits 0 even when it prints an error, so detect failures by scanning
# output for the "ERROR" marker the CLI emits (it is uppercase for errors).
assert_no_cli_error() {
  local label="$1" out="$2"
  if grep -q 'ERROR' <<<"$out"; then
    echo "::error::zeabur reported an error during '${label}' (CLI exit code was 0):"
    echo "$out"
    exit 1
  fi
}

echo "Listing services in environment ${ENV_ID} ..."
services_json=$(zeabur service list --env-id "$ENV_ID" --json --interactive=false 2>&1) || {
  echo "::error::failed to list services for env ${ENV_ID}:"
  echo "$services_json"
  exit 1
}
assert_no_cli_error "service list" "$services_json"

for name in "${services[@]}"; do
  # Tolerate Go-field-name ("ID"/"Name") and lowercase JSON shapes.
  id=$(jq -r --arg n "$name" \
    '.[] | select((.Name // .name) == $n) | (.ID // .id // ._id // empty)' \
    <<<"$services_json" | head -n1)
  if [ -z "$id" ] || [ "$id" = "null" ]; then
    echo "::error::could not resolve a service id for '${name}' in env ${ENV_ID}."
    echo "service list returned: ${services_json}"
    exit 1
  fi

  echo "Bumping ${name} (${id}) -> ${TAG}"
  out=$(zeabur service update tag --env-id "$ENV_ID" --id "$id" --tag "$TAG" -y --interactive=false 2>&1) || {
    echo "::error::zeabur service update tag failed for ${name} (${id}):"
    echo "$out"
    exit 1
  }
  echo "$out"
  assert_no_cli_error "update tag ${name}" "$out"
done

echo "Lockstep deploy of [${services[*]}] to ${TAG} complete."
