# Troubleshooting

This page covers common first-pass developer issues.

## `./bin/cassini` fails immediately

### Symptom

You see an error like:

- `go: command not found`
- a Go build failure before Cassini actually starts

### Why it happens

In this repo, `./bin/cassini` builds a temporary Cassini binary before running it.

### What to do

- make sure Go is installed and available on `PATH`
- rerun the command from the repo root

## The harness does not start cleanly

### Symptom

`./bin/cassini dev stack up` fails or the local Talk UI never becomes available.

### What to check

- Docker is running
- port `28080` is free
- the local stack had enough time to bootstrap

### Useful checks

Open:

- `http://127.0.0.1:28080/`

Or run:

```bash
./bin/cassini dev stack status
```

## The deployment bundle has port conflicts

### Symptom

`docker compose up` under `deployment/` fails to bind one of the default ports.

### Default ports

- operator: `4000`
- control panel: `4173`
- viewer: `8765`

### What to do

Change the values in `deployment/.env`:

- `CASSINI_OPERATOR_PORT`
- `CASSINI_CONTROL_PANEL_PORT`
- `CASSINI_VIEWER_PORT`

See:

- [Configuration reference](./configuration.md)

## The control panel opens, but starting a job fails with `503`

### Meaning

The operator has no free recording slots.

### Why it happens

Recording is admission-controlled by `CASSINI_MAX_RECORD_WORKERS`.
There is no durable recording queue.

### What to do

- wait for the current recording job to finish
- or increase `CASSINI_MAX_RECORD_WORKERS`

See:

- [Operator stack](../operator-stack.md)

## A job becomes `interrupted` after restart

### Meaning

The operator restarted while work was queued or running.

### Current behavior

On startup, non-terminal work is marked `interrupted` rather than automatically resumed.

### What to do

- inspect the job and attempts in the control panel
- use rerun when the preserved canonical `.run` exists

See:

- [Operator stack](../operator-stack.md)
- [Operator API reference](./api.md)

## The viewer is empty

### Meaning

Usually one of these is true:

- no publish has succeeded yet
- the latest job failed before publish finished
- publish found no ready `.meeting` artifacts to export

### What to do

- confirm the job reached `done / succeeded`
- inspect job and attempt state in the control panel
- refresh the viewer after publish success

Remember:

- the deployment bundle seeds an empty site so the viewer can start before any meetings exist
- an empty viewer on first startup is normal

## The job stopped recording, but kept running

### Meaning

This is often expected.

Stopping the record subprocess is not the same as cancelling all downstream work.
If the recorder finalized a usable `.run`, the job may continue into build and publish.

## I expected a summary or cleaned-up transcript, but it is missing

### Meaning

Readable cleanup and summary generation are optional capability layers.

### What to check

- whether the relevant LLM-related env vars were configured
- whether summary generation was explicitly disabled
- whether the base build still succeeded without those optional outputs

See:

- [Configuration reference](./configuration.md)
- [Core pipeline](../core-pipeline.md)

## The control panel cannot reach the operator after I changed the base path

### Meaning

The browser path and the proxy path no longer agree.

### What to check

- `CASSINI_OPERATOR_BASE_PATH` in deployment or local dev config
- the control panel proxy target configuration
- whether the base path still starts with `/`

See:

- [Configuration reference](./configuration.md)
- [Control panel](../components/control-panel.md)

## I want to wipe local state and start fresh

### Deployment bundle

From `deployment/`:

```bash
docker compose down -v
```

That removes the default named volumes for:

- operator state
- published site

### Harness

For a normal stop:

```bash
./bin/cassini dev stack down
```

For deeper harness cleanup, use the harness-specific scripts and notes in:

- `harness/README.md`
