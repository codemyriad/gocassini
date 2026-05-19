# Day-2 operations

The most common operational tasks and checks are collected here.

## Check the main surfaces

Operator API:

```bash
curl -s http://127.0.0.1:4000/jobs
```

Control-panel same-origin proxy path:

```bash
curl -s http://127.0.0.1:4173/jobs
```

Viewer catalog:

```bash
curl -s http://127.0.0.1:8765/catalog.json
```

## Understand common runtime states

### `503` on job creation

Meaning:

- all record slots are busy
- there is no durable record queue

What to do:

- wait for an active recording to finish
- or increase `CASSINI_MAX_RECORD_WORKERS`

### `interrupted` after restart

Meaning:

- the operator stopped while work was queued or running

What to do:

- inspect the job and attempts
- rerun if a canonical ready `.run` exists

### Recording stopped, but the job kept running

Meaning:

- this is often expected
- a stop request ends record work, not necessarily the whole pipeline
- if a usable `.run` exists, build and publish may still continue

## Rerun a terminal job

Use the control panel or API when:

- a downstream step failed
- build or publish behavior changed
- you need a fresh `.meeting` and `.site` from the preserved capture

Current reruns:

- require a canonical ready `.run`
- start at build
- do not rejoin the original room

## Understand an empty viewer

The viewer may be empty because:

- no publish has succeeded yet
- the latest job failed before publish finished
- publish found no ready meetings in the canonical current library

Also remember:

- the deployment seeds an empty site so the viewer can start before any meetings exist

## Change ports or worker counts

Primary deployment knobs in `deployment/.env`:

- `CASSINI_OPERATOR_PORT`
- `CASSINI_CONTROL_PANEL_PORT`
- `CASSINI_VIEWER_PORT`
- `CASSINI_MAX_RECORD_WORKERS`
- `CASSINI_MAX_BUILD_WORKERS`

## Inspect host-visible storage

If you want the operator state and published site on the host filesystem, set:

- `CASSINI_OPERATOR_STATE_STORAGE`
- `CASSINI_PUBLISHED_SITE_STORAGE`

Then restart the Compose bundle.

## Reset local state intentionally

From `deployment/`:

```bash
docker compose down -v
```

That removes the default named volumes for:

- operator state
- published site

Use it only when you want a clean slate.

## Keep the core boundaries in mind

When something looks strange, check which boundary is involved:

- operator issue
- control-panel proxy/base-path issue
- published-site storage/promotion issue
- viewer issue

That split usually narrows the problem quickly.

## Where to go next

- API details: [Operator API](./reference/api.md)
- Config knobs: [Configuration](./reference/configuration.md)
- Path and storage details: [Storage and filesystem reference](./reference/storage-and-filesystem.md)
- Common failures: [Troubleshooting](./reference/troubleshooting.md)
