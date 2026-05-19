# Deployment stack

The packaged deployment lives under `deployment/` and starts three services:

- `cassini-operator`
- `cassini-control-panel`
- `cassini-viewer`

## Topology

```text
browser
  -> control panel
  -> viewer

control panel
  -> operator API

operator
  -> SQLite
  -> work root
  -> shared published-site volume
  -> cassini CLI subprocesses

viewer
  -> same shared published-site volume (read only)
```

## Default browser surfaces

The checked-in `deployment/.env` exposes these defaults:

- operator API: `4000`
- control panel: `4173`
- viewer: `8765`
- public operator base path: `/`

## Shared published-site volume

This is the most important deployment boundary.

The operator and viewer share the same storage with different permissions:

- operator: read-write
- viewer: read-only

Mounted parent path:

```text
/srv/cassini-site
```

Live site root:

```text
/srv/cassini-site/published
```

That parent directory exists so the operator can stage and replace the live site safely.

## Why `published/` lives under a parent directory

This layout exists because:

- standalone `cassini publish` expects an empty output directory
- operator publish therefore targets attempt-local `.site` bundles first
- successful attempt sites are promoted into the live root afterward
- the operator needs adjacent staging space during promotion

## Container roles

### Operator container

The operator image packages:

- `cassini`
- `cassini-operator`
- transcription/runtime dependencies
- `ffmpeg`
- the export runner
- a built viewer distribution used to seed the initial site

Before starting the operator, its entrypoint:

1. ensures state directories exist
2. ensures the published-site directory exists
3. migrates older flat site layouts into `published/` when needed
4. seeds an initial empty site if no live site exists yet

### Control panel container

The control panel serves the built UI through Vite preview and writes a runtime config file at startup so the browser knows which same-origin base path to call.

Current packaged proxying targets the operator through `host.docker.internal:<operator-port>`.

### Viewer container

The viewer is plain nginx over the live `published/` root. It contains no operator logic and does not mutate site content.

## Persistent storage

By default the Compose bundle uses named volumes:

- `cassini_operator_state`
- `cassini_published_site`

Optional host-visible bind-mount overrides:

- `CASSINI_OPERATOR_STATE_STORAGE`
- `CASSINI_PUBLISHED_SITE_STORAGE`

## Startup sequence

A normal startup looks like this:

1. Compose starts the operator container
2. the entrypoint prepares storage and seeds the live site if needed
3. the operator process applies migrations and marks incomplete jobs `interrupted`
4. Compose starts the control panel container
5. Compose starts the viewer container

## Failure isolation

The deployment keeps attempt-local publish output separate from the live site.

Consequences:

- a failed publish does not corrupt the currently served site
- the viewer can continue serving the previous successful site
- failed attempt-local `.site` outputs remain inspectable

## Where to go next

- Runtime behavior and queues: [Operator runtime](./operator-runtime.md)
- Storage layout and promotion details: [Storage and promotion](./storage-and-promotion.md)
- Exact env vars: [Configuration](./reference/configuration.md)
