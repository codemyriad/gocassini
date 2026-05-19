# Storage and promotion

The operator's runtime layout explains how successful publish output becomes the live site.

## The three storage shapes to keep in mind

### 1. Canonical reusable artifacts

```text
<work-root>/current/
  <job-id>.run/
  <job-id>.meeting/
```

This is the stable library used for downstream reruns and publish.

### 2. Attempt-local retained artifacts

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

This is the preserved execution history.

### 3. Live published site

```text
<site-root>/
```

In the packaged deployment, the shared parent mount is `/srv/cassini-site` and the live site itself is `/srv/cassini-site/published`.

## Why both `current/` and `runs/` exist

The split exists so Cassini can do both of these well:

- preserve attempt history
- keep one canonical winning artifact set per job

That is what enables:

- downstream reruns from preserved capture
- publish from the canonical current meeting library
- inspection of older failures without losing the latest reusable artifacts

## Summary-row vs attempt-row paths

At the logical job summary level:

- `artifact_run_path` points at canonical `current/<job-id>.run`
- `artifact_meeting_path` points at canonical `current/<job-id>.meeting`
- `artifact_site_path` points at the live shared site root

At the attempt level:

- paths point at attempt-local retained outputs when those outputs were created for that attempt
- rerun attempts typically reuse the canonical `.run` and create fresh attempt-local `.meeting` and `.site` outputs

## How live-site promotion works

The operator never points `cassini publish --out` directly at the live site root.

Instead it:

1. publishes into a retained attempt-local `.site`
2. copies that site into a staging area next to the live root
3. writes live-site lineage into staged `cassini.json`
4. moves the current live site aside if needed
5. renames the staged site into place
6. removes the backup on success

This exists because standalone `cassini publish` requires an empty output directory.

## Lineage in the live site

The live site manifest can record:

- `published_by_job_id`
- `published_by_attempt_number`
- `published_at_utc`

That lets the active deployment answer which attempt produced the site now being served.

## Failure boundary

Promotion separates:

- retained attempt-local `.site` output
- the currently served live site

So:

- a failed publish does not corrupt the live site
- the previous successful site can remain available
- failed attempt-local `.site` output is still inspectable

## Typical roots in deployment

Operator state volume holds:

- SQLite DB
- work-root artifacts
- caches
- temp files

Published-site volume holds:

- live `published/` site
- adjacent staging space used during promotion

## Where to go next

- Runtime behavior: [Operator runtime](./operator-runtime.md)
- Day-to-day handling: [Day-2 operations](./day-2-operations.md)
- Exact path reference: [Storage and filesystem reference](./reference/storage-and-filesystem.md)
