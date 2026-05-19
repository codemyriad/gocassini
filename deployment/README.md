# Cassini deployment bundle

This folder is the repo-root Docker Compose bundle for:

- `cassini-operator`
- `cassini-control-panel`
- `cassini-viewer`

## Quickstart

```bash
cd deployment
docker compose up --build
```

Default browser surfaces from the checked-in `.env`:

- control panel: `http://127.0.0.1:4173/`
- operator API: `http://127.0.0.1:4000/`
- viewer: `http://127.0.0.1:8765/`

The control panel talks to the operator through the shared same-origin API path set by `CASSINI_OPERATOR_BASE_PATH`.
With the default `CASSINI_OPERATOR_BASE_PATH=/`, the browser uses `/jobs` and `/events` on the control-panel origin.
If you change it to `/operator`, the browser uses `/operator/jobs` and `/operator/events` instead.

## Public bundle knobs

The checked-in `.env` exposes the deployment-facing contract:

- `CASSINI_OPERATOR_PORT`
- `CASSINI_CONTROL_PANEL_PORT`
- `CASSINI_VIEWER_PORT`
- `CASSINI_OPERATOR_BASE_PATH`
- `CASSINI_MAX_RECORD_WORKERS`
- `CASSINI_MAX_BUILD_WORKERS`

## Storage defaults

By default the bundle uses Docker-managed named volumes:

- `cassini_operator_state`
- `cassini_published_site`

The shared published-site storage is mounted at `/srv/cassini-site`.
The live deployed site is the child directory `/srv/cassini-site/published`, so the operator can atomically swap `published/` during promotion while the viewer mounts the same shared storage read-only.

## Bind-mount override

If you want host-visible storage instead of the default named volumes, set absolute paths in `.env`:

```dotenv
CASSINI_OPERATOR_STATE_STORAGE=/absolute/path/to/cassini-operator-state
CASSINI_PUBLISHED_SITE_STORAGE=/absolute/path/to/cassini-published-site-storage
```

Then run the same command:

```bash
cd deployment
docker compose up --build
```

## Optional capability pass-through

The bundle also passes these optional operator capability envs through when you set them in `.env`:

- `OPENROUTER_API_KEY`
- `OPENROUTER_BASE_URL`
- `LLM_BASE_URL`
- `LLM_MODEL`
- `SUMMARY_MODEL`
- `CASSINI_SUMMARY_DISABLED`
- `CASSINI_READABLE_STRICT_BATCHES`

They are intentionally not part of the narrow core deployment contract.

## Quick checks

```bash
cd deployment
docker compose up --build
curl -s http://127.0.0.1:4000/jobs
curl -s http://127.0.0.1:4173/jobs
curl -s http://127.0.0.1:8765/catalog.json
```
