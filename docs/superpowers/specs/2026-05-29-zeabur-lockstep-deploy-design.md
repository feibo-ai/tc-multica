# Zeabur lockstep image deployment — design

Date: 2026-05-29
Status: approved — ready for implementation plan
Scope: frontend (`multica-web`) + backend (`multica-backend`) production
deployment on Zeabur (project `teamctx`). Database stays managed.

## Goal

Make the frontend + backend deployment **coherent and lockstep**: front and back
always run the same version, the release pipeline that already builds images is
the thing that actually ships to prod, and the deploy wiring is documented in the
repo instead of living only in the Zeabur dashboard.

## Completion criteria

- `multica-web` and `multica-backend` on Zeabur are **prebuilt-image** services
  pulling `ghcr.io/feibo-ai/multica-{web,backend}:vX.Y.Z`, not building from
  source.
- A tag push (`vX.Y.Z`) builds both images and then **automatically** bumps both
  Zeabur services to that tag — front and back move together, no manual step.
- A fresh clone + the in-repo deploy doc is enough to recreate the prod wiring
  (service env var names, internal hostnames, ports) without reverse-engineering
  the dashboard.
- Existing behavior preserved: WS works, `/api/*` proxying works, migrations run
  on backend boot, self-host (`docker-compose.selfhost.yml`) keeps working off
  the same images.

## Current state (the mess we're removing)

- Two **disconnected** delivery paths: `release.yml` builds versioned multi-arch
  GHCR images (for self-host) that prod never uses; Zeabur rebuilds from
  `feibo-ai/tc-multica` source on every push.
- Deploy config split repo↔dashboard; the Dockerfile selection lives in a
  dashboard env var (`ZBPACK_DOCKERFILE_NAME`) and the committed `zbpack.json`
  was dropped, so the repo no longer documents the wiring.
- No lockstep: web and backend observed on different commits.
- PG version drift: prod pg18 vs CI/local pg17.

(Daemon / cloud agent runtimes are **out of scope** — each member runs their own
local autopilot daemon; there is no cloud daemon.)

## Target architecture

```
Zeabur project teamctx (env 6a1800dd5245baf7fc3dd7cc)
├─ multica-backend  ← image ghcr.io/feibo-ai/multica-backend:vX.Y.Z   (entrypoint: migrate up → server)
├─ multica-web      ← image ghcr.io/feibo-ai/multica-web:vX.Y.Z       (Next.js standalone)
└─ postgresql       ← managed Zeabur Postgres pg18 (CI + local aligned to pg18)
both image services pinned to the SAME vX.Y.Z = lockstep
```

Runtime env (set in Zeabur, NOT baked into images):
- backend: as today (`DATABASE_URL` ref, `MULTICA_CONTROL_PLANE_ENABLED`,
  secrets, URLs).
- web: `REMOTE_API_URL=http://multica-backend.zeabur.internal:8080`. Drop
  `NEXT_PUBLIC_WS_URL` (let WS use the same-origin `/ws` rewrite).

## Why one image works for every environment (key de-risk)

The web app is already runtime-configurable, so a single image serves Zeabur and
self-host with no per-environment build:

- `REMOTE_API_URL` is read in `next.config.ts` at standalone-server **startup**
  (runtime), not inlined into the client bundle — so the API proxy target is set
  per-deploy via env.
- WS uses `deriveWsUrl()`: with no `NEXT_PUBLIC_WS_URL` it falls back to
  `wss://<page-host>/ws`, proxied to the backend by the `/ws` rewrite. No
  build-time WS config needed.
- PostHog / Google OAuth / signup come from the backend `/api/config` at runtime
  (explicit design: "so self-hosted deployments do not need to rebuild the
  frontend image").

The only build-time-inlined value that matters is `NEXT_PUBLIC_APP_VERSION`,
which `release.yml` already sets to the tag — desirable (stamps the version).

## CI/CD changes

The image build/push jobs already exist in `release.yml` (`docker-backend-*`,
`docker-web-*`, no owner guard → they run on `feibo-ai` tags and push to
`ghcr.io/feibo-ai/...`). One new job ties them to prod:

```yaml
  deploy-zeabur:
    needs: [verify, docker-backend-merge, docker-web-merge]
    runs-on: ubuntu-latest
    # Only on the deploy repo (the upstream/forks without the token skip it).
    if: ${{ vars.ZEABUR_ENV_ID != '' }}
    steps:
      - name: Install Zeabur CLI
        run: |
          curl -fsSL -o zeabur \
            https://github.com/zeabur/cli/releases/download/v0.18.0/zeabur_0.18.0_linux_amd64
          chmod +x zeabur && sudo mv zeabur /usr/local/bin/zeabur
      - name: Bump both services to the release tag (lockstep)
        env:
          ZEABUR_TOKEN: ${{ secrets.ZEABUR_TOKEN }}            # API token
          ENV_ID: ${{ vars.ZEABUR_ENV_ID }}                    # 6a1800dd5245baf7fc3dd7cc
          TAG: ${{ needs.verify.outputs.tag_name }}
        run: |
          zeabur auth login --token "$ZEABUR_TOKEN" --interactive=false
          zeabur service update tag --env-id "$ENV_ID" --name multica-backend --tag "$TAG" -y --interactive=false
          zeabur service update tag --env-id "$ENV_ID" --name multica-web     --tag "$TAG" -y --interactive=false
```

- Auth: `zeabur auth login --token "$ZEABUR_TOKEN" --interactive=false`
  (non-interactive). `secrets.ZEABUR_TOKEN` = Zeabur API token, `vars.ZEABUR_ENV_ID`
  = environment id. CLI pinned to a GHCR release binary (v0.18.0 linux_amd64).
- `zeabur service update tag` is confirmed to update a prebuilt service's image
  tag (`--name/--id`, `--tag`, `--env-id`, `-y`).
- The GoReleaser/Homebrew `release` job stays guarded to `multica-ai`; it is
  unrelated to prod and unchanged.

## Config-in-repo

Add `deploy/zeabur/README.md` documenting the two services (image refs, ports,
internal hostnames) and a `deploy/zeabur/.env.example` listing every required
env var **name** with placeholder values (secrets excluded). This replaces the
documentation gap left when `zbpack.json` was dropped.

## Cutover (one-time)

1. Confirm `ghcr.io/feibo-ai/multica-{backend,web}` images exist for a recent
   tag (grant `gh` `read:packages` to check, or push a tag to build them).
2. Switch both Zeabur services from **Git source → Docker image** (image type),
   pinned to the current `vX.Y.Z`. Removes the `ZBPACK_DOCKERFILE_NAME` hack.
   Images are public, so no registry credentials are needed.
3. On `multica-web`: delete `NEXT_PUBLIC_WS_URL`; confirm `REMOTE_API_URL` →
   `http://multica-backend.zeabur.internal:8080`.
4. Bump CI + local Postgres to pg18 (`ci.yml` service image + `docker-compose.yml`).
5. Add `secrets.ZEABUR_TOKEN` + `vars.ZEABUR_ENV_ID`; merge the `deploy-zeabur`
   job. From here: **tag = deploy**; stop the from-source builds.

This matches CLAUDE.md's existing rule that a production deploy accompanies a CLI
release tag, so the cadence is already familiar.

## Decisions

1. **GHCR image visibility = public.** Zeabur pulls without credentials — no
   registry-secret setup in the cutover.
2. **Postgres = pg18 everywhere.** Bump `ci.yml` + `docker-compose.yml` to
   `pgvector/pgvector:pg18` to match prod (pg17→pg18 is schema-compatible).

## Risks & mitigations

- **First image-source deploy** differs from the running from-source one — do it
  off-peak; `zeabur service redeploy`/rollback to a prior tag is available.
- **Self-host parity** — the same images already power `docker-compose.selfhost`;
  confirm self-host still sets `REMOTE_API_URL=http://backend:8080` (its internal
  hostname) so the runtime-config path holds there too.

## Out of scope

- Cloud daemon / cloud codex|claude runtimes (members run local autopilot
  instances).
- The control-plane MCP service `tcmcp-remote-gres` (separate repo, unaffected).
- Mobile / desktop release flows.
