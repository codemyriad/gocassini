# Deployment

The deployment bundle packages three runtime services:

- **operator**
- **control panel**
- **viewer**

Those services are composed around one shared published-site storage boundary.

## What each service is for

### Operator

The operator is the only service that mutates runtime state.

It owns:

- job execution
- SQLite state
- work-root artifacts
- live recording orchestration
- build and publish execution
- promotion of successful publish output into the shared live site

### Control panel

The control panel is the browser UI for operating the operator.

It owns:

- job creation
- stop and rerun actions
- job and attempt inspection
- live operator status updates

### Viewer

The viewer is the read-only meeting site.

It owns:

- serving the published static meeting library
- browser playback and transcript review

It does not build or mutate the site.

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
  -> same shared published-site volume (read-only)
```

## Compose bundle shape

The deployment bundle is the repo-root Compose stack under `deployment/`.

Current services are:

- `cassini-operator`
- `cassini-control-panel`
- `cassini-viewer`

Default externally exposed ports from the checked-in `.env` are:

- operator API: `4000`
- control panel: `4173`
- viewer: `8765`

Current default operator base path:

- `/`

## Shared published-site volume

This is the most important deployment boundary.

The operator and viewer share the same storage, but with different permissions:

- operator: read-write
- viewer: read-only

Mounted parent path:

```text
/srv/cassini-site
```

Live site root inside that parent:

```text
/srv/cassini-site/published
```

That extra parent directory exists so the operator can perform staging and replacement next to the live root.

## Why the live root is `published/` under a parent volume

This layout exists because:

- standalone `cassini publish` requires an empty output directory
- operator-managed publish therefore targets attempt-local `.site` bundles first
- successful attempt sites are then promoted into the live root
- the operator needs room beside the live root for staging and backup handling
- the viewer only needs the final promoted `published/` directory

### Operator container

The operator image contains:

- `cassini`
- `cassini-operator`
- native transcription libraries
- `ffmpeg`
- the publish runner script
- a built viewer distribution used for initial site seeding

### Operator container defaults

Inside the container, the deployment sets or relies on these defaults:

- DB path: `/var/lib/cassini-operator/jobs.sqlite3`
- work root: `/var/lib/cassini-operator/jobs`
- cache root: `/var/lib/cassini-operator/cache`
- temp root: `/var/lib/cassini-operator/tmp`
- live site root: `/srv/cassini-site/published`
- `CASSINI_EXPORTER_RUNNER`: points at the packaged static export runner

### Operator-mounted storage

The operator mounts:

- operator state storage at `/var/lib/cassini-operator`
- shared published-site storage at `/srv/cassini-site`

### Operator entrypoint behavior

The operator container entrypoint does more than just `exec` the binary.

Before starting the operator it:

1. ensures state directories exist
2. ensures the published-site directory exists
3. migrates an older flat site layout into `published/` when necessary
4. seeds an initial empty live site if `published/index.html` does not exist

The seed site contains:

- `index.html`
- `assets/`
- empty `catalog.json`
- ready site-level `cassini.json`

This guarantees the viewer can start before any meeting has ever been published.

### Control panel container

The control panel image serves the built UI through Vite preview.

At container start it writes a browser runtime config file:

- `dist/cassini-config.js`

That file injects the public operator base path used by the browser.

### Operator proxying model in deployment

The browser talks to the control panel origin, not directly to the operator origin.

The Vite preview server proxies operator traffic upstream.

Current deployment wiring sets:

- `CASSINI_OPERATOR_BASE_PATH` to the public browser path
- `CASSINI_OPERATOR_URL` to `http://host.docker.internal:<operator-port>`

Important detail:

- the preview proxy currently targets the operator's published host port via `host.docker.internal`
- it does not proxy to the Compose service name directly

This is part of the current packaged runtime contract.

### Viewer container

The viewer image is plain nginx.

It serves:

- root: `/srv/cassini-site/published`
- port: `8765`
- read-only shared published-site mount

The viewer container does not contain operator logic and does not mutate site content.

### Persistent storage model

By default the Compose bundle uses named Docker volumes:

- `cassini_operator_state`
- `cassini_published_site`

### Operator state volume

Holds:

- SQLite DB
- work-root artifacts
- caches
- temp files

### Published-site volume

Holds:

- the current promoted viewer site under `published/`
- staging and replacement-adjacent filesystem operations performed by the operator

### Optional bind-mount overrides

The bundle also supports host-visible storage through environment overrides.

Current override variables are:

- `CASSINI_OPERATOR_STATE_STORAGE`
- `CASSINI_PUBLISHED_SITE_STORAGE`

When set, those paths replace the default named volumes.

### Compose configuration surface

The checked-in deployment `.env` currently defines the public bundle knobs:

- `CASSINI_OPERATOR_PORT`
- `CASSINI_CONTROL_PANEL_PORT`
- `CASSINI_VIEWER_PORT`
- `CASSINI_OPERATOR_BASE_PATH`
- `CASSINI_MAX_RECORD_WORKERS`
- `CASSINI_MAX_BUILD_WORKERS`

The operator service also passes through optional capability variables such as:

- `OPENROUTER_API_KEY`
- `OPENROUTER_BASE_URL`
- `LLM_BASE_URL`
- `LLM_MODEL`
- `SUMMARY_MODEL`
- `CASSINI_SUMMARY_DISABLED`
- `CASSINI_READABLE_STRICT_BATCHES`

These control readable-cleanup and summary-generation behavior inside build, but are not required for the base deployment to start.

### Startup sequence

A normal startup looks like this:

1. Compose starts the operator container
2. the operator entrypoint prepares and seeds the live site if needed
3. the operator process starts, applies DB migrations, and marks incomplete jobs `interrupted`
4. Compose starts the control panel container
5. Compose starts the viewer container
6. the control panel becomes available for job creation and inspection
7. the viewer serves the current live site, initially an empty catalog if nothing has been published yet

### End-to-end runtime flow in deployment

Once running, the deployed stack behaves like this:

1. a browser user starts a job from the control panel
2. the control panel sends operator API requests through its same-origin proxy path
3. the operator records, builds, and publishes through CLI subprocesses
4. successful publish writes a retained attempt-local `.site`
5. the operator promotes that retained site into `/srv/cassini-site/published`
6. the viewer immediately serves the new live content from the same shared volume

### Failure isolation

The deployment intentionally separates:

- attempt-local publish outputs
- the live published site

Consequences:

- a failed publish attempt does not corrupt the currently served site
- the viewer can keep serving the previous successful deployment
- the operator retains failed attempt-local `.site` bundles for inspection

### Public surfaces

The deployment exposes three surfaces:

- operator API
- control panel UI
- viewer UI

Only the control panel and viewer are intended as browser-facing user surfaces.

The operator API is primarily an operational backend surface.

### Current operational implications

A few current behaviors matter in practice:

- the live viewer site is a static export of the operator's canonical current meeting library
- each successful operator publish currently rebuilds that full library from ready `.meeting` bundles (transient build scratch; the canonical, user-facing meeting format is the portable `.opus`)
- the viewer service is fully read-only against published output
- the operator is the only service that should ever mutate shared published-site storage
