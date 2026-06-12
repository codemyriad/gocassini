# Installing Cassini as a Nextcloud ExApp

Cassini ships as a Nextcloud AppAPI external app. The container exposes the
admin control-panel, the recording viewer, and the operator JSON API over a
single HTTP port; Nextcloud's AppAPI proxy fronts it with access-level
enforcement.

## Prerequisites

- Nextcloud **30 or newer** with the **AppAPI** app installed and a registered
  deploy daemon (Docker or Podman).

Persistent storage is automatic: AppAPI creates a named volume for every
docker-deployed ExApp and the operator stores all durable data under it
(see [Persistent storage](#persistent-storage)).

## Image variants

The image is published at:

```
ghcr.io/codemyriad/gocassini:latest
ghcr.io/codemyriad/gocassini:<version-tag>
```

For AppAPI compatibility, identical CPU-only images are also published at
`-cuda` and `-rocm` suffixes. GPU acceleration is **not** active in v1; the
suffix variants exist only so admins on GPU-tagged daemons get a working
install. Real GPU variants are tracked as a TODO.

## Install via AppAPI admin UI

In Nextcloud → Settings → External Apps:

1. Click **Register an external app from a Docker image**.
2. Image: `ghcr.io/codemyriad/gocassini`
3. Tag: `latest`
4. AppAPI will pull the image, set up a HaRP tunnel, and run the container.
5. Once the daemon reports the image is up, enable the app. Nextcloud calls
   `PUT /enabled` on the container; a 200 response activates the app.

## Required environment

AppAPI provides these automatically when it spawns the container; if you run
the container outside AppAPI for development you must supply them yourself:

| Variable | What it does |
|---|---|
| `APP_HOST` / `APP_PORT` | Bind address inside the container (default `0.0.0.0:8080`) |
| `APP_ID` | Must match the `<id>` in `appinfo/info.xml` (`gocassini`) |
| `APP_VERSION` | Must match the `<version>` in the manifest |
| `APP_SECRET` | Shared secret with AppAPI; enables AppAPI auth middleware |
| `AA_VERSION` | AppAPI version Nextcloud is running |
| `HP_FRP_ADDRESS` / `HP_FRP_PORT` / `HP_SHARED_KEY` | HaRP tunnel parameters used by `frpc` |
| `APP_PERSISTENT_STORAGE` | Mount path of AppAPI's persistent volume; the operator defaults its data roots under it |
| `CASSINI_APPAPI_REQUIRED=true` | Makes the operator refuse to start without `APP_SECRET` (set in the ExApp Dockerfile) |

## App-specific environment

These variables are declared under `<environment-variables>` in
`appinfo/info.xml`. That declaration is what makes them settable in an AppAPI
deployment: AppAPI only passes declared variables to the container, and
`--env` values for undeclared keys are **silently dropped**. Set them at
registration time, either in the External Apps admin UI (Deploy Options) or
on the command line:

```
occ app_api:app:register gocassini <daemon-name> \
    --env CASSINI_TALK_RECORDING_SECRET=<secret> \
    --env OPENROUTER_API_KEY=<key> \
    --wait-finish
```

| Variable | Required | What it does |
|---|---|---|
| `CASSINI_TALK_RECORDING_SECRET` | For the Talk record button | Shared secret for Talk's recording backend protocol; must match the `secret` in `spreed`'s `recording_servers` (below). Unset, the operator rejects every recording request |
| `CASSINI_TALK_BACKEND_URL` | No | Override for operator→Talk callbacks (started/stopped notifications, recording upload). Leave empty to use the backend URL Talk sends with each request |
| `OPENROUTER_API_KEY` | No | API key for LLM transcript cleanup + meeting summaries. Unset, raw transcripts are published without summaries |
| `LLM_BASE_URL` | No | OpenAI-compatible API base URL; defaults to `https://openrouter.ai/api/v1` when `OPENROUTER_API_KEY` is set |
| `LLM_MODEL` | No | Model for cleanup/summaries (default `openai/gpt-4o-mini`) |

## Connect the Talk record button

Point Talk's recording backend at the AppAPI proxy with the same secret you
registered the app with. The `api/v1/welcome` and `api/v1/room/*` routes are
declared PUBLIC in the manifest, so Talk's recording protocol (authenticated
by its own HMAC, not a Nextcloud session) passes through the proxy:

```
occ config:app:set spreed recording_servers --value='{"servers":[{"server":"https://cloud.example.com/index.php/apps/app_api/proxy/gocassini","verify":true}],"secret":"<secret>"}'
occ config:app:set spreed call_recording --value=yes
```

## Persistent storage

AppAPI's docker deploy creates a named volume (`nc_app_gocassini_data`),
mounts it in the container at `/nc_app_gocassini_data`, and exposes that
path as `APP_PERSISTENT_STORAGE`. The operator stores all durable data
under it:

```
$APP_PERSISTENT_STORAGE/operator/jobs.sqlite3    # SQLite job DB
$APP_PERSISTENT_STORAGE/operator/app-state.json  # AppAPI lifecycle state
$APP_PERSISTENT_STORAGE/operator/jobs            # per-attempt artifacts (raw recordings)
$APP_PERSISTENT_STORAGE/site/published           # published meeting site (read by the viewer)
```

No manual volume mounts are required — job history, recordings, and the
published site survive app updates and container recreates.

Setting `CASSINI_OPERATOR_DB_PATH`, `CASSINI_OPERATOR_WORK_ROOT`, or
`CASSINI_OPERATOR_SITE_ROOT` to a non-default path overrides the
corresponding location (mount your own volume there). The container logs a
warning at startup when an effective data path sits on an ephemeral
filesystem (overlay or tmpfs).

Outside AppAPI (plain `docker run` without `APP_PERSISTENT_STORAGE`) the
image defaults apply: `/var/lib/cassini-operator` for the DB + work root and
`/srv/cassini-site/published` for the site — mount volumes there yourself.

## Access policy

The manifest declares per-route access levels enforced by Nextcloud's proxy:

| Route | Access | What it is |
|---|---|---|
| `/control-panel/*` | ADMIN | Svelte admin UI for starting and monitoring jobs |
| `/operator/jobs`, `/operator/jobs/...`, `/operator/events` | ADMIN | Operator JSON + SSE API |
| `/viewer/*` | USER | Viewer SPA |
| `/published/*` | USER | Published meeting bundles (catalog + recordings) |

USER means any logged-in Nextcloud user. v1 ships an **org-wide recording
archive** — anyone who can log in to your Nextcloud can browse every
published meeting. Per-recording ACLs are a future enhancement.

`PUT /enabled` and `POST /init` are AppAPI **lifecycle callbacks**, not
proxied browser routes; they do not appear in `<routes>`.

## Local development

`deployment/compose.yml` continues to work for local dev: it brings up the
operator and the control-panel as separate services without AppAPI
middleware (no `APP_SECRET` env). Use this path while developing on the
operator or UI.

## Testing the image

See [`docs/exapp-test-locally.md`](./exapp-test-locally.md) for three tiers:
image-only checks (no Nextcloud), manual install against a local Nextcloud,
and the production-shaped HaRP-fronted install (deferred).

## CI

`.github/workflows/publish-exapp-image.yml` validates the manifest, builds
the image, runs the smoke test, attempts the E2E (continue-on-error while
the harness stabilizes), and on `main` or version tags pushes to
`ghcr.io/codemyriad/gocassini`.
