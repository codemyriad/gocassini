# Artifacts and filesystem

This page describes Cassini’s main artifact types and the operator’s runtime layout.

## The four artifact shapes to know

| Artifact | Produced by | Purpose |
|---|---|---|
| `.run` bundle | record | durable capture output |
| `.meeting` bundle | build | transient build scratch (intermediate; packed into `.opus`) |
| `.site` bundle | publish | static viewer export |
| portable `.opus` | build/packaging flow; the operator's **seal** stage | the one canonical user-facing meeting format and only durable published contract |

The `.opus` portable file is the single user-facing deliverable. The `.meeting`
bundle and its manifests (`cassini.json` = `cassini.meeting.v1`, `manifest.json`
= `cassini.meeting-artifact.v1`) are internal build scratch, not a published
contract, and are scheduled for retirement once build/publish stop depending on
them.

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
- output: an intermediate bundle staged for packing into a portable `.opus`

The `.meeting` bundle is transient build scratch, not a published format. Its
`cassini.json` and `manifest.json` are internal staging manifests, not a
consumer contract, and are scheduled for retirement. Prefer the portable `.opus`
as the durable, user-facing meeting artifact.

## `.site` bundle

Typical contents (lightweight by default — D-531):

- `catalog.json`
- `meetings/<meeting-id>/...`
- site-level `cassini.json`
- `index.html` + `assets/...` — **only** when published with `--rebuild-viewer`;
  by default the viewer shell is served from the Docker image, not the site

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

- raw `.meeting` bundles are transient build scratch (an intermediate that gets packed into a portable `.opus`), not the canonical deliverable
- published meeting directories are exported viewer artifacts derived during publish

The canonical user-facing meeting artifact is the portable `.opus` file.

## Operator runtime layout

Assume these roots:

- `work-root = /var/lib/cassini-operator/jobs`
- `site-root = /srv/cassini-site/published`

### Canonical reusable artifacts

```text
<work-root>/current/
  <job-id>.run/
  <job-id>.meeting/
  <job-id>.opus
```

This is the operator’s canonical current library.

Think of it as “the latest successful reusable artifacts per logical job”.

`<job-id>.opus` is a **promotion** of the artifact an attempt sealed — a hard link
where the filesystem allows one — not an independent pack of the same meeting. It
is committed with a single atomic rename, so a reader sees either the whole
previous portable meeting or the whole new one, never neither.

### Attempt-local retained artifacts

```text
<work-root>/runs/
  <job-id>--attempt-001.run/
  <job-id>--attempt-001.logs/
    record.log
    build.log
    seal.log
    publish.log
  <job-id>--attempt-002.meeting/
  <job-id>--attempt-002.seal/
    <job-id>.opus
  <job-id>--attempt-002.site/
```

This is the preserved attempt history.

Think of it as “what happened in each execution pass”.

The `.seal` directory holds that attempt's portable meeting, and its shape is the
product of two constraints that pull in different directions:

- it has to be **attempt-scoped**, so a rerun cannot overwrite the artifact a
  queued publish is about to deliver — that race is exactly what D-583 removed;
- its **file name has to stay the job id**, because the static-site exporter
  derives a meeting's catalog id from the input file's stem. A file named
  `<job-id>--attempt-002.opus` would publish a rerun as a second, separate
  meeting rather than updating the first.

A directory per attempt satisfies both. The file inside it is immutable: it is
written once, digested, and never rewritten.

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

> **The site root is only used by the `local` publish sink.** Under
> `--sink nextcloud-files` (the default for an installed ExApp) recordings are
> written into the `cassini` service account's Files and nothing is written
> here, so the operator does not serve this directory at all — archive requests
> go to Nextcloud or 404. The per-attempt `runs/<job>--attempt-NNN.site`
> directory is staging either way, and is removed once the sink accepts the
> meeting. See `docs/reference/configuration.md` for `--sink`.

Which path in Nextcloud Files depends on the storage mode, and the two roots are
deliberately distinct so that neither can shadow the other:

```text
  default mode            CassiniNoACL/Recordings/   the service account's own
                                                     private directory
  access-controlled mode  Cassini/Recordings/        inside the `Cassini` Team
                                                     folder, under advanced ACLs

  either root:  meetings/<job-id>.opus
                catalog.json
```

The shape inside is identical, so nothing downstream of the root string changes
with the mode. Only one root holds the archive at a time; switching modes copies
it across and then empties the other. `/status` reports the active one as
`recordings_access.root`. See
[Installing Cassini as a Nextcloud ExApp](../exapp-install.md#where-recordings-live).

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
- `artifact_opus_path` points at canonical `current/<job-id>.opus`
- `artifact_opus_sha256` is the SHA-256 of the artifact the current attempt sealed
- `artifact_site_path` points at the live shared site root

At the attempt level:

- `.run`, `.meeting`, and `.site` paths point at attempt-local retained outputs when those outputs were created for that attempt
- `artifact_opus_path` points at that attempt's own immutable
  `runs/<job-id>--attempt-NNN.seal/<job-id>.opus`, and `artifact_opus_sha256` is
  its digest
- rerun attempts typically reuse the canonical `.run` and create fresh attempt-local `.meeting`, `.seal` and `.site` outputs

The split is the same one every stage uses, and it is what lets a publish deliver
a specific attempt's artifact rather than whatever is currently canonical:

```text
  job row      current/<job-id>.opus                        what is canonical now
  attempt row  runs/<job-id>--attempt-NNN.seal/<job-id>.opus what this attempt sealed
```

## Retention

Attempt-local payloads under `runs/` are pruned by an explicit policy,
`--artifact-retention` / `CASSINI_ARTIFACT_RETENTION`:

| Policy | Prunes |
|--------|--------|
| `all` | nothing |
| `superseded` | the `.run`, `.meeting`, `.site` and `.seal` of attempts a rerun has replaced |
| `sealed` **(default)** | `superseded`, plus a succeeded attempt's `.run`, `.meeting` and `.site` |

One removal happens outside this policy and `all` does not disable it: a
successfully delivered attempt's `.site` is removed as soon as the sink accepts
it (D-550). That is an access boundary rather than housekeeping — the attempt
site is a full copy of the recording on the app's own volume, outside the
Nextcloud access model — so retention is not a way to keep one.

Never pruned, under any policy: everything in `current/`, every attempt `.logs`
directory, the retained `.seal` of a succeeded attempt, and the live site. Every
removal is additionally guarded on the artifact that replaces it existing, so a
record that failed before promotion keeps its attempt `.run` and a failed job
keeps everything — nothing here removes the last copy of anything.

Attempt rows keep the paths of artifacts that were pruned. The row is the record
of what that attempt produced; the retention policy governs whether the bytes are
still there. So an `artifact_site_path` on a succeeded attempt under the `sealed`
policy names a directory that no longer exists, by design — the operator log line
`artifact retention removed id=… policy=… <path> (…)` is what says why.

## Live site lineage

When the operator promotes a successful site into the live root, the live site manifest can record lineage such as:

- `published_by_job_id`
- `published_by_attempt_number`
- `published_at_utc`

That makes it possible to answer: which attempt produced the currently served site?

## See also

- [Core pipeline](../core-pipeline.md)
- [Operator stack](../operator-stack.md)
