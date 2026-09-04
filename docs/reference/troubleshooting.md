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
- the job failed at `seal`, so there was never a portable meeting to publish
- publish found no ready `.meeting` artifacts to export
- publish refused to run because the sealed `.opus` was missing or no longer
  matched the digest recorded for it

### What to do

- confirm the job reached `done / succeeded`
- inspect job and attempt state in the control panel
- refresh the viewer after publish success

### Sealing failures

A job cannot publish until it has sealed a portable `.opus`, so a failed seal is
a failed job (`done / failed`) with the pack error in `error`. The attempt's seal
log is the detail:

```text
<work-root>/runs/<job-id>--attempt-NNN.logs/seal.log
```

A rerun is the retry. It rebuilds from `current/<job-id>.run`, so a failed seal
loses nothing but time.

If the seal succeeded but the publish refused to run, the operator log carries
one of these, and each one names the job and the artifact:

```text
job <id> has no sealed portable meeting to publish
job <id> is missing its sealed portable meeting <path>
job <id> has a sealed portable meeting with no recorded digest
sealed portable meeting <path> changed since it was sealed: sha256 <got>, want <want>
```

The last one means the file on disk is no longer the artifact the job sealed —
truncated, replaced, or corrupted. It is a refusal, not a fallback: publishing
something the pipeline did not verify is exactly what sealing exists to prevent.
A rerun re-seals and re-publishes.

If the delivery got as far as the sink and was refused, the message names the
site-relative asset instead:

```text
published asset meetings/<id>.opus does not match the sealed artifact: sha256 <got>, want <want>
```

Nothing was published in that case — the asset is checked before the catalog can
name it, and the previous live site is untouched.

Remember:

- the deployment bundle seeds an empty site so the viewer can start before any meetings exist
- an empty viewer on first startup is normal

## The job stopped recording, but kept running

### Meaning

This is often expected.

Stopping the record subprocess is not the same as cancelling all downstream work.
If the recorder finalized a usable `.run`, the job may continue into build and publish.

## I expected a summary, but it is missing

### Meaning

Summary generation is an optional capability layer. Transcripts are never
rewritten by a model, so a missing summary is the only LLM-shaped output that
can go missing.

### What to check

- whether the relevant LLM-related env vars were configured
- whether summary generation was explicitly disabled
- whether the base build still succeeded without that optional output

## My vocabulary terms are not showing up in the transcript

### Meaning

The vocabulary biases the decoder; it does not rewrite finished text. A term is
only written where the audio already supports it, and some models cannot be
biased at all.

### What to check

- `provenance.speechToText.hints` in the build manifest. `applied: false` names
  the reason, most often a model bundle with no `bpe.vocab` or the `fast`
  quality tier, which uses a CTC model that cannot be biased
- whether `CASSINI_STT_HINTS_DISABLED` is set
- whether the speaker genuinely said the term. Biasing raises a spelling's
  score, it does not insert words

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
