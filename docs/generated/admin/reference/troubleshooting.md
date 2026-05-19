# Troubleshooting

## The optional local harness verification path is not working on macOS

### Meaning

This is a known current limitation of the local harness.

### Why it happens

The harness currently has networking issues on macOS.

### What to do

- do not assume the harness-based local end-to-end flow will work on macOS right now
- when you do use that optional harness path, prefer `./bin/cassini dev ...` entrypoints and `127.0.0.1` addresses, including in the browser

## The deployment bundle has port conflicts

### Symptom

`docker compose up` under `deployment/` fails to bind one of the default ports.

### What to do

Change the relevant values in `deployment/.env`:

- `CASSINI_OPERATOR_PORT`
- `CASSINI_CONTROL_PANEL_PORT`
- `CASSINI_VIEWER_PORT`

## The control panel opens, but job creation returns `503`

### Meaning

All recording slots are busy.

### Why it happens

Recording is admission-controlled by `CASSINI_MAX_RECORD_WORKERS`. There is no durable recording queue.

### What to do

- wait for the active recording to finish
- or increase `CASSINI_MAX_RECORD_WORKERS`

## A job becomes `interrupted` after restart

### Meaning

The operator restarted while work was queued or running.

### Current behavior

On startup, non-terminal work is marked `interrupted` rather than automatically resumed.

### What to do

- inspect the job and attempts
- rerun if the preserved canonical `.run` exists

## The viewer is empty

### Meaning

Usually one of these is true:

- no publish has succeeded yet
- the latest job failed before publish finished
- publish found no ready `.meeting` artifacts in the canonical current library

### What to remember

An empty viewer on first startup is normal because the deployment seeds an empty live site.

## Recording stopped, but the job kept running

### Meaning

This is often expected.

If the recorder finalized a usable `.run`, the job can continue into build and publish.

## The control panel cannot reach the operator after a base-path change

### Meaning

The browser path and proxy path no longer agree.

### What to check

- `CASSINI_OPERATOR_BASE_PATH`
- control-panel proxy configuration
- whether the base path still starts with `/`

## I expected summary or readable-transcript outputs, but they are missing

### Meaning

Those are optional capability layers.

### What to check

- whether the relevant LLM-related env vars were configured
- whether summary generation was disabled
- whether the base build still succeeded without those optional outputs

## I want to wipe local state and start fresh

From `deployment/`:

```bash
docker compose down -v
```

That removes the default named volumes for:

- operator state
- published site
