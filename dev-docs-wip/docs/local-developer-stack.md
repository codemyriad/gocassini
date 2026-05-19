# Running the local developer stack

During development you usually run two local stacks together:

1. the **harness** for local Nextcloud Talk
2. the **deployment bundle** for operator, control panel, and viewer

If you only want the shortest possible path, use:

- [Quick start](./quick-start.md)

## The two-stack picture

### Harness

The harness is the local lab.

It gives you:

- a local Nextcloud instance
- Talk rooms you can create quickly
- repeatable smoke and fixture flows

### Deployment bundle

The deployment bundle is the packaged Cassini runtime.

It gives you:

- operator
- control panel
- viewer
- shared published-site storage

For local end-to-end testing, you normally run both.

## Default local topology

| Part | Purpose | Default local address |
|---|---|---|
| Harness / Nextcloud Talk | meeting source | `http://127.0.0.1:28080/` |
| Operator API | runtime backend | `http://127.0.0.1:4000/` |
| Control panel | operator UI | `http://127.0.0.1:4173/` |
| Viewer | published meeting UI | `http://127.0.0.1:8765/` |

## Start and stop commands

### Harness

Important harness notes before you start:

- prefer `./bin/cassini dev stack up`, `down`, and `status` over raw harness `docker compose`
- the `cassini dev` path runs the harness scripts and additional setup after Compose starts
- use `127.0.0.1`, not `localhost`, for local harness URLs, including in the browser
- the local harness currently does not work on macOS because of networking issues in the harness stack

From the repo root:

```bash
./bin/cassini dev stack up
./bin/cassini dev stack status
./bin/cassini dev stack down
```

Useful related commands:

```bash
./bin/cassini dev room create --name "Local room"
./bin/cassini dev smoke
./bin/cassini dev fixture prepare-showcase
./bin/cassini dev player showcase --call-url "$CALL_URL"
```

See more:

- [Harness component page](./components/harness.md)

### Deployment bundle

From `deployment/`:

```bash
docker compose up --build
docker compose down
```

If you want to wipe the named volumes too:

```bash
docker compose down -v
```

That removes:

- operator state storage
- published-site storage

Use the `-v` form only when you intentionally want a clean slate.

## What the deployment bundle contains

The bundle under `deployment/` starts three runtime services:

- `cassini-operator`
- `cassini-control-panel`
- `cassini-viewer`

The important storage boundary is the shared published-site volume:

- operator mounts it read-write
- viewer mounts it read-only

This is what lets the operator publish a new site while the viewer remains a simple static reader.

## Why the live site lives under `published/`

The shared storage is mounted at:

```text
/srv/cassini-site
```

The live viewer root is:

```text
/srv/cassini-site/published
```

That extra parent directory exists so the operator can:

- stage a new site beside the live one
- replace the live site safely
- keep the viewer pointed only at the final promoted root

## Local configuration surface

The checked-in `deployment/.env` exposes the main local knobs:

- `CASSINI_OPERATOR_PORT`
- `CASSINI_CONTROL_PANEL_PORT`
- `CASSINI_VIEWER_PORT`
- `CASSINI_OPERATOR_BASE_PATH`
- `CASSINI_MAX_RECORD_WORKERS`
- `CASSINI_MAX_BUILD_WORKERS`

Optional bind-mount overrides:

- `CASSINI_OPERATOR_STATE_STORAGE`
- `CASSINI_PUBLISHED_SITE_STORAGE`

See exact details here:

- [Configuration reference](./reference/configuration.md)

## Control panel proxying in local deployment

The browser talks to the control panel origin, not directly to the operator origin.

In the packaged deployment:

- the control panel serves the UI
- the control panel proxies operator requests upstream
- the browser uses the configured same-origin base path, such as `/`

Current deployment detail:

- the control panel container proxies to `http://host.docker.internal:<operator-port>`
- it does not currently proxy to the Compose service name directly

That matters mostly when you are changing packaging or proxy behavior.

## Storage model

By default the deployment bundle uses Docker named volumes:

- `cassini_operator_state`
- `cassini_published_site`

The operator state volume holds:

- SQLite DB
- work-root artifacts
- caches
- temp files

The published-site volume holds:

- the live promoted site under `published/`
- staging space next to it used during promotion

If you want host-visible storage, set bind-mount paths in `deployment/.env`.

## Typical startup sequence

A normal local startup looks like this:

1. start the harness with `./bin/cassini dev stack up`
2. create or open a Talk room
3. start the deployment bundle
4. open the control panel and viewer
5. submit the room URL to the operator

That is the path described in:

- [Quick start](./quick-start.md)

## Where to go next

- Want the runtime model behind these services: [Operator stack](./operator-stack.md)
- Want the stage-by-stage pipeline: [Core pipeline](./core-pipeline.md)
- Want exact env vars and paths: [Configuration reference](./reference/configuration.md)
