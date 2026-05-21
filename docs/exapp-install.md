# Installing Cassini as a Nextcloud ExApp

Cassini ships as a Nextcloud AppAPI external app. The container exposes the
admin control-panel, the recording viewer, and the operator JSON API over a
single HTTP port; Nextcloud's AppAPI proxy fronts it with access-level
enforcement.

## Prerequisites

- Nextcloud **30 or newer** with the **AppAPI** app installed and a registered
  deploy daemon (Docker or Podman).
- A persistent volume for `/var/lib/cassini-operator` and `/srv/cassini-site`
  on the daemon host. Without persistent storage, job history and published
  recordings are lost on container recreate.

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
| `CASSINI_APPAPI_REQUIRED=true` | Makes the operator refuse to start without `APP_SECRET` (set in the ExApp Dockerfile) |

## Required volumes

Mount persistent volumes on the daemon host:

```
/var/lib/cassini-operator   # SQLite job DB + per-attempt artifacts
/srv/cassini-site           # Published meeting site root (read by the viewer)
```

The container logs a warning at startup if it detects these paths on
ephemeral filesystems (overlay or tmpfs).

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
