#!/usr/bin/env bash
#
# Lockstep deploy: point the named Zeabur image services at $TAG together.
#
# Why resolve by --id (not --name):
#   `zeabur service update tag --name X --env-id Y` does NOT work
#   non-interactively. The resolver calls util.GetServiceByName(), which needs
#   owner+project context that --env-id does not supply, so it fails with
#   "either id or ownerName, projectName, and name must be specified".
#   `zeabur service list --project-id P --env-id Y` lists the services, so we
#   map name→id and update by --id. (`service list --env-id` ALONE returns an
#   empty list non-interactively — it needs --project-id as well.)
#
# Why `update tag` alone ships it (no redeploy):
#   These are prebuilt-IMAGE services. `service update tag` retags AND deploys
#   the new image — Zeabur ships it on the tag change. We deliberately do NOT
#   call `service redeploy`: that path only works for git-bound services and
#   fails on image services with CANNOT_REDEPLOY_INPLACE ("You must bind a
#   GitHub repository to the service to allow redeploying in-place"). So the
#   flow is just: list → update tag. (The maiden v0.4.5/v0.4.6 deploys never
#   shipped because PROJECT_ID/ENV_ID and the service names were wrong, so the
#   list returned [] and nothing was ever retagged — not a missing redeploy.)
#
# Why we don't trust exit codes:
#   the zeabur CLI prints "ERROR ..." but still exits 0 on the resolution
#   failure above, so a naive `set -e` pipeline reports success on a no-op
#   deploy. We capture output and fail on any ERROR marker or an unresolved id.
#
# Usage:
#   PROJECT_ID=<proj> ENV_ID=<env> TAG=<vX.Y.Z> [ZEABUR_TOKEN=<token>] deploy.sh <svc> [svc...]
#   - ZEABUR_TOKEN: set in CI to authenticate; omit when already logged in
#     locally (a prior `zeabur auth login`).
#   - service names default to "backend frontend" if none are passed (the
#     actual Zeabur service names in the team-context project).
set -euo pipefail

: "${PROJECT_ID:?PROJECT_ID (Zeabur project id) is required}"
: "${ENV_ID:?ENV_ID (Zeabur environment id) is required}"
: "${TAG:?TAG (release tag, e.g. v0.4.6) is required}"

services=("$@")
if [ "${#services[@]}" -eq 0 ]; then
  services=(backend frontend)
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
services_json=$(zeabur service list --project-id "$PROJECT_ID" --env-id "$ENV_ID" --json --interactive=false 2>&1) || {
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
