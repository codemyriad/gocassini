# Artifacts and filesystem

Cassini’s main artifact types and the operator’s runtime layout are summarized here.

## The four artifact shapes to know

| Artifact | Produced by | Purpose |
|---|---|---|
| `.run` bundle | record | durable capture output |
| `.meeting` bundle | build | durable built meeting artifact |
| `.site` bundle | publish | static viewer export |
| portable `.opus` | build/packaging flow | one-file meeting package |

## Common bundle manifest idea

Cassini bundles use `cassini.json` as the top-level bundle manifest.

Across bundle types, these fields answer the same questions:

- `kind` — what kind of artifact is this?
- `state` — is it preparing, ready, or failed?
- `stage` — where did processing stop?

Common state values:

- `preparing`
- `ready`
- `failed`

That shared model is what makes stage boundaries inspectable and rerunnable.

## `.run` bundle

Typical contents:

- `cassini.json`
- `recording.mkv`
- optional `session/` directory

Conceptually:

- input: Talk room
- output: reusable captured media

Use `.run` when you need to rerun or inspect the build stage.

## `.meeting` bundle

Typical contents:

- `cassini.json`
- `meeting.webm`
- `transcript.words.v1.json`
- `manifest.json`
- optional `transcript.readable.v1.json`
- optional `captions.vtt`
- optional `summary.md`

Conceptually:

- input: `.run` or raw `.mkv`
- output: reusable built meeting artifact

Use `.meeting` when you need a canonical publish input.

## `.site` bundle

Typical contents:

- `index.html`
- `assets/...`
- `catalog.json`
- `meetings/<meeting-id>/...`
- site-level `cassini.json`

Conceptually:

- input: one or more ready `.meeting` bundles
- output: static browser-deliverable meeting library

Use `.site` when you need something the viewer can serve directly.

## Portable `.opus`

A portable Cassini `.opus` file is:

- an ordinary Ogg Opus audio file
- with Cassini metadata embedded inside it
- loadable by the viewer in portable mode

Conceptually:

- input: the same capture/build pipeline
- output: one file rather than an explicit bundle directory

## Published meeting directories vs raw `.meeting` bundles

A published meeting directory under `meetings/<id>/` is viewer-ready export output.

It may include extra viewer-facing files that were materialized during publish.

So:

- raw `.meeting` bundles are the canonical built artifacts
- published meeting directories are exported viewer artifacts derived from those bundles

## Operator runtime layout

Assume these roots:

- `work-root = /var/lib/cassini-operator/jobs`
- `site-root = /srv/cassini-site/published`

### Canonical reusable artifacts

```text
<work-root>/current/
  <job-id>.run/
  <job-id>.meeting/
```

This is the operator’s canonical current library.

Think of it as “the latest successful reusable artifacts per logical job”.

### Attempt-local retained artifacts

```text
<work-root>/runs/
  <job-id>--attempt-001.run/
  <job-id>--attempt-001.logs/
    record.log
    build.log
    publish.log
  <job-id>--attempt-002.meeting/
  <job-id>--attempt-002.site/
```

This is the preserved attempt history.

Think of it as “what happened in each execution pass”.

### Live published site

```text
<site-root>/
```

In deployment packaging, the shared parent mount is:

```text
/srv/cassini-site
```

and the live site itself is:

```text
/srv/cassini-site/published
```

## Why both `current/` and `runs/` exist

The split exists so Cassini can do both of these well:

- preserve attempt history
- keep one canonical current artifact set per logical job

That is what enables:

- downstream reruns from preserved capture
- publish from the canonical current meeting library
- inspection of old failures without losing the winning artifact set

## Job-summary vs attempt-row artifact semantics

At the logical job summary level:

- `artifact_run_path` points at canonical `current/<job-id>.run`
- `artifact_meeting_path` points at canonical `current/<job-id>.meeting`
- `artifact_site_path` points at the live shared site root

At the attempt level:

- `.run`, `.meeting`, and `.site` paths point at attempt-local retained outputs when those outputs were created for that attempt
- rerun attempts typically reuse the canonical `.run` and create fresh attempt-local `.meeting` and `.site` outputs

## Live site lineage

When the operator promotes a successful site into the live root, the live site manifest can record lineage such as:

- `published_by_job_id`
- `published_by_attempt_number`
- `published_at_utc`

That makes it possible to answer: which attempt produced the currently served site?

## See also

- [Core pipeline](../core-pipeline.md)
- [Operator stack](../operator-stack.md)
