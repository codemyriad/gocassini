# Running the local developer stack

For production-shaped end-to-end testing, use the installed ExApp path in the
[Quick start](./quick-start.md). This page describes the alternative standalone
developer topology, with two stacks:

1. the **harness** for local Nextcloud Talk
2. the **deployment bundle** for operator and viewer

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
- viewer
- shared published-site storage

For local end-to-end testing, you normally run both.

## Default local topology

| Part | Purpose | Default local address |
|---|---|---|
| Harness / Nextcloud Talk | meeting source | `http://127.0.0.1:28080/` |
| Operator API | runtime backend | `http://127.0.0.1:4000/` |
| Viewer | published meeting UI | `http://127.0.0.1:8765/` |

## Start and stop commands

### Harness

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

The bundle under `deployment/` starts two runtime services:

- `cassini-operator`
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

The checked-in `deployment/.env.example` exposes the main local knobs:

- `CASSINI_OPERATOR_PORT`
- `CASSINI_VIEWER_PORT`
- `CASSINI_OPERATOR_BASE_PATH`
- `CASSINI_MAX_RECORD_WORKERS`
- `CASSINI_MAX_BUILD_WORKERS`

Optional bind-mount overrides:

- `CASSINI_OPERATOR_STATE_STORAGE`
- `CASSINI_PUBLISHED_SITE_STORAGE`

See exact details here:

- [Configuration reference](./reference/configuration.md)

## Developing the Operator UI

The Compose bundle has no separate control-panel container. Run `cassini-app`
with Vite to work on its Operator section against the standalone backend:

```bash
npm ci
cd cassini-app
CASSINI_OPERATOR_URL=http://127.0.0.1:4000 npm run dev
```

Open the address Vite prints. The default operator base path is `/`; the Vite
server proxies the API requests to port 4000. Browse and Nextcloud provisioning
features need the installed ExApp topology for end-to-end validation.

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

## Downloading recordings for local work

`cassini dev meetings pull` downloads the recordings your Nextcloud account can
read into a local static archive. The archive contains `catalog.json` and
`meetings/<id>.opus`. Treat it as confidential: it can include audio, transcripts,
and summaries. See [CLI reference](./cli.md) for filters and usage.

The harness creates new recordings through Talk. Imported archives are not used
for permission testing because their Nextcloud shares cannot be transferred to
test accounts.

## Typical startup sequence

A normal local startup looks like this:

1. start the harness
2. create or open a Talk room
3. start the deployment bundle
4. start the app’s Vite server above and open its Operator section
5. submit the room URL to the operator

For the recommended installed ExApp path, see [Quick start](./quick-start.md).

## Where to go next

- Want the runtime model behind these services: [Operator stack](./operator-stack.md)
- Want the stage-by-stage pipeline: [Core pipeline](./core-pipeline.md)
- Want exact env vars and paths: [Configuration reference](./reference/configuration.md)
