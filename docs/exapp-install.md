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
