# Zeabur production deployment (project `teamctx`)

Frontend + backend run on Zeabur as **prebuilt-image** services pinned to the
same release tag (lockstep). Images are built by `.github/workflows/release.yml`
on a `vX.Y.Z` tag and pushed to GHCR; the `deploy-zeabur` job then bumps both
services to that tag. Prod does **not** build from source.

Design rationale: `docs/superpowers/specs/2026-05-29-zeabur-lockstep-deploy-design.md`.

## Services

| Service | Type | Image / source | Port | Public domain |
|---|---|---|---|---|
| `multica-backend` | Docker image | `ghcr.io/feibo-ai/multica-backend:vX.Y.Z` (public) | 8080 | `api.teamctx.actionow.ai` |
| `multica-web` | Docker image | `ghcr.io/feibo-ai/multica-web:vX.Y.Z` (public) | 3000 | `teamctx.actionow.ai` |
| `postgresql` | Managed (Zeabur) | `pgvector/pgvector:pg18` | 5432 | internal only |

- Backend entrypoint runs `migrate up` then `server`, so DB migrations apply on
  every deploy automatically.
- Web talks to backend over the **internal** network; only `/ws` + `/api` are
  proxied through the web origin (see env below).

## Environment variables

Set these in the Zeabur dashboard per service. Secrets are marked — never commit
their values. `.env.example` in this folder lists the names with placeholders.

### multica-backend

| Key | Value | |
|---|---|---|
| `DATABASE_URL` | `${POSTGRES_CONNECTION_STRING}?sslmode=disable` | Zeabur reference var → `postgresql` |
| `PORT` | `8080` | |
| `APP_ENV` | `production` | |
| `MULTICA_PUBLIC_URL` | `https://api.teamctx.actionow.ai` | |
| `MULTICA_APP_URL` | `https://teamctx.actionow.ai` | |
| `FRONTEND_ORIGIN` | `https://teamctx.actionow.ai` | |
| `CORS_ALLOWED_ORIGINS` | `https://teamctx.actionow.ai` | |
| `COOKIE_DOMAIN` | `.actionow.ai` | |
| `ALLOW_SIGNUP` | `true` / `false` | |
| `MULTICA_CONTROL_PLANE_ENABLED` | `true` | |
| `JWT_SECRET` | … | **secret** |
| `MULTICA_SECRET_MASTER_KEY` | base64 32-byte | **secret** — losing it makes all control-plane secrets unrecoverable |
| `RESEND_API_KEY` | … | **secret** |
| `RESEND_FROM_EMAIL` | `noreply@actionow.ai` | |

### multica-web

| Key | Value | |
|---|---|---|
| `PORT` | `3000` | |
| `HOSTNAME` | `0.0.0.0` | |
| `REMOTE_API_URL` | `http://multica-backend.zeabur.internal:8080` | runtime API-proxy target (read at server start) |

- **Do NOT set `NEXT_PUBLIC_WS_URL`.** WebSocket falls back to
  `wss://<page-host>/ws` and is proxied to the backend by the Next.js `/ws`
  rewrite. (A baked WS URL would pin the image to one host.)
- **Do NOT set `ZBPACK_DOCKERFILE_NAME`.** That only applied to the old
  from-source build; image-type services ignore it.

## Lockstep deploy (CI)

The `deploy-zeabur` job in `release.yml` runs after both image-merge jobs and
calls `deploy/zeabur/deploy.sh`:

```
ENV_ID=<env> TAG=<vX.Y.Z> ZEABUR_TOKEN=<token> deploy/zeabur/deploy.sh multica-backend multica-web
```

**Why the script resolves services by `--id`, not `--name`:**
`zeabur service update tag --name X --env-id Y` does **not** work
non-interactively — the CLI's name resolver needs owner+project context that
`--env-id` doesn't supply, so it errors (`either id or ownerName, projectName,
and name must be specified`) **and still exits 0**. The script instead lists the
env (`service list --env-id --json`, an env-scoped query that *does* work from
the env id alone), maps name→id, and updates by `--id`. It treats any `ERROR` in
CLI output as failure because the exit code can't be trusted. (The maiden v0.4.5
deploy hit exactly this: green job, no-op deploy.)

Required GitHub config on the deploy repo (`feibo-ai/tc-multica`):

- Secret `ZEABUR_TOKEN` — a Zeabur API token (Settings → Secrets and variables → Actions → Secrets).
- Variable `ZEABUR_ENV_ID` = `6a1800dd5245baf7fc3dd7cc` (Actions → Variables). The job is skipped if this is unset.

A **stable** tag push (`git tag v0.x.y && git push origin v0.x.y`) builds both
images and deploys them together — no manual dashboard step. The job is gated to
stable tags (`is_stable == 'true'`, i.e. no `-suffix`), so pre-release tags
build images without touching prod.

To deploy by hand (e.g. recover from a failed CI deploy without re-tagging),
run the same script locally after `zeabur auth login`:

```
ENV_ID=6a1800dd5245baf7fc3dd7cc TAG=v0.x.y deploy/zeabur/deploy.sh
```

…or just bump both services' image tag in the Zeabur dashboard — the GHCR
images already exist once the tag's build jobs are green.

## One-time cutover (from the old from-source setup)

1. Ensure `ghcr.io/feibo-ai/multica-{backend,web}` images exist for a recent tag
   (push a tag if not). Make the GHCR packages **public**.
2. In Zeabur, change `multica-backend` and `multica-web` from **Git source** to
   **Docker image**, image `ghcr.io/feibo-ai/multica-<svc>`, tag = current
   `vX.Y.Z`.
3. On `multica-web`: delete `NEXT_PUBLIC_WS_URL`; keep `REMOTE_API_URL` →
   `http://multica-backend.zeabur.internal:8080`.
4. Add the `ZEABUR_TOKEN` secret + `ZEABUR_ENV_ID` variable on GitHub.
5. From now on: **tag = deploy.** Source builds are no longer used.

The same images also back self-hosting (`docker-compose.selfhost.yml`), where
`REMOTE_API_URL=http://backend:8080` points at that stack's internal hostname.
