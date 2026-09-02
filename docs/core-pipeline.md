# Core pipeline

This page explains the main record/build/publish flow in more depth.

If you have not already seen the system run, start with:

- [Quick start](./quick-start.md)

## Pipeline overview

Cassini is built around durable stage boundaries.

```text
Nextcloud Talk room
  -> .run bundle
  -> .meeting bundle
  -> .opus portable meeting
  -> .site bundle
```

There is also a portable one-file output mode:

```text
Nextcloud Talk room
  -> capture/build pipeline
  -> one .opus file
```

The simplest way to understand the pipeline is:

- **record** captures reusable source media
- **build** turns that source media into a portable `.opus` meeting, staging a transient `.meeting` bundle internally
- **publish** turns one or more built meetings into a static browser site

In the operator those boundaries are four persisted stages, because the pack
that produces the `.opus` is a precondition for publishing rather than a side
effect of building — see [Operator stack](./operator-stack.md).

## 1. Record

### Purpose

Record joins a Nextcloud Talk room and captures reusable source media.

### Inputs

- Talk room URL
- optional recorder display name
- optional duration cap
- optional room-empty stop behavior
- optional room-empty grace period

### Output

The normal explicit output is a **`.run` bundle**.

Typical contents:

- `cassini.json`
- `recording.mkv`
- optional `session/` artifacts

### What to remember

A `.run` bundle is the durable output of capture.

It is useful because:

- build can be rerun from it
- failures can be inspected after recording finishes
- operator reruns do not need to rejoin the original room when a canonical `.run` already exists

### Record-stage behavior worth knowing

- live recording writes `recording.mkv`
- operator stop requests target the live record subprocess
- a stopped recording may still finalize into a usable `.run`

## 2. Build

### Purpose

Build turns captured media into a reviewable meeting artifact.

### Accepted inputs

- a ready `.run` bundle
- a raw `.mkv` recording

### Primary outputs

- a **`.meeting` bundle** in explicit bundle mode
- a final **portable `.opus`** file in single-file mode

### What build actually does

In the current pipeline, build:

1. resolves the source recording
2. probes the MKV stream layout
3. mixes speaker tracks into `meeting.webm`
4. computes decoded-audio integrity hashes
5. runs speech-to-text
6. writes the canonical word-timed transcript
7. writes captions
8. optionally generates `summary.md`
9. writes `manifest.json`
10. finalizes either a `.meeting` bundle or a portable `.opus`

### What a `.meeting` bundle contains

Core outputs normally include:

- `cassini.json`
- `meeting.webm`
- `transcript.words.v1.json`
- `manifest.json`

Optional outputs may include:

- `captions.vtt`
- `summary.md`

### Optional layers inside build

Two useful distinctions:

- speech-to-text is part of the normal build path
- summary generation is a capability layer that depends on configuration

So a successful build may produce a perfectly useful `.meeting` even when summary generation is absent.

### Important distinction: raw `.meeting` vs published viewer artifact

The build stage itself does not have to produce every viewer convenience file.

For example:

- a raw `.meeting` bundle is transient build scratch (an intermediate that gets packed into a portable `.opus`), not the canonical deliverable
- publish/export tooling may later materialize viewer-facing files such as `transcript.display.v1.json`

That means a published meeting directory is not necessarily a byte-for-byte copy of the transient `.meeting` bundle. The canonical, user-facing meeting artifact is the portable `.opus` file.

## 3. Publish

### Purpose

Publish turns one or more ready meetings into a static viewer site.

### Accepted inputs

- one portable `.opus` file
- one ready `.meeting` bundle
- or a directory containing either or both

A `.opus` input is **verified and copied through byte for byte** — its embedded
manifest is decoded and its audio integrity re-checked, and then the file itself
is what lands in the site. A `.meeting` input is packed into a temporary `.opus`
first, so publishing a bundle costs a pack that publishing a portable meeting
does not.

> The operator no longer uses the `.meeting` input. Every meeting it publishes is
> sealed into a portable `.opus` first (see [Operator stack](./operator-stack.md)),
> so the operator's publish is always the copy-through path. The `.meeting` input
> remains for standalone CLI use and is scheduled for retirement (see the D-425
> retirement inventory).

### Output

A **`.site` bundle**.

Typical contents (lightweight by default — D-531):

- `catalog.json`
- `meetings/<meeting-id>/...`
- site-level `cassini.json`
- `index.html` + `assets/...` — **only** with `--rebuild-viewer`; otherwise the
  viewer shell is served from the image at runtime

### What publish actually does

Publish currently:

1. prepares an empty output directory
2. stages only ready `.meeting` bundles
3. skips partial or failed meetings
4. fails if no ready meetings remain
5. invokes the static exporter
6. copies the viewer shell
7. copies or materializes per-meeting viewer artifacts
8. writes `catalog.json`
9. writes site-level `cassini.json`

### Important publish rule

Publish only uses artifacts it can verify: a ready `.meeting` bundle, or a
`.opus` whose embedded integrity check passes.

That rule matters both:

- in standalone CLI mode
- and in operator mode, where publish is handed exactly one sealed `.opus` — the
  artifact that attempt sealed, identified by its digest

### Cost, stated honestly

For a job with one new meeting, and `A` meetings already in the archive:

| Work | Cost |
|------|------|
| packing the meeting | once, at seal time |
| writing its audio into the site | one file copy |
| every other meeting's audio | **untouched** |
| `catalog.json` | read, re-marshalled and rewritten in full — `O(A)` |

So the **archived-media** work per publish is O(1) in the size of the archive,
which is the D-459 win. The total is not O(1): the flat catalog is `O(A)` per
publish, and `O(J²)` metadata work across `J` arriving jobs. At production scale
(tens of meetings, a few KB of JSON) that is negligible next to one audio pack,
which is why the flat catalog has not been replaced — but "incremental media
publish" is the accurate description, not "O(1) publish".

## Explicit bundle flow vs portable single-file flow

### Explicit bundle flow

This is the clearest developer-facing flow when you want to see each stage boundary.

```bash
./bin/cassini record --call "$CALL_URL" --out demo.run
./bin/cassini build demo.run --out demo.meeting
./bin/cassini publish ./meetings --out site
```

Use it when you want:

- visible intermediates
- easier debugging
- operator-like artifact inspection

### Portable single-file flow

This is the shorter end-user story.

Examples:

```bash
./bin/cassini record --call "$CALL_URL" --out "Weekly Sync.opus"
./bin/cassini build /path/to/meeting.mkv --out "Imported Meeting.opus"
```

Portable mode still uses the same logical stages internally. It just hides the intermediate workspace.

## `.meeting` vs `.opus`

### `.meeting`

Think of `.meeting` as:

- transient build scratch: an intermediate bundle the build stage stages before packing into a portable `.opus`
- easy to inspect while debugging the pipeline
- not a user-facing deliverable, and scheduled for retirement (its `cassini.json`/`manifest.json` are internal staging manifests, not a published contract)

### `.opus`

Think of portable `.opus` as:

- the one canonical, user-facing meeting format and only durable published contract
- a one-file packaged meeting, good for sharing or archiving
- readable by the viewer in portable mode
- in the operator, **sealed**: produced by a stage the job must pass, verified,
  digested, and immutable once written

The key point is that `.opus` is the durable deliverable, not a separate capture architecture; the `.meeting` bundle is just the intermediate it is packed from.

## Common state model across bundles

Cassini bundle manifests share the same readiness idea.

Common state values:

- `preparing`
- `ready`
- `failed`

That is what lets later stages answer questions like:

- did this stage finish?
- did it fail partway through?
- is it a valid input for the next stage?

## Why this pipeline shape is useful

This file-first pipeline makes Cassini easier to work on because it supports:

- inspectable failures
- reproducible downstream reruns
- static-site publishing
- a clean separation between orchestration and media processing

## Where to go next

- Want the runtime around this pipeline: [Operator stack](./operator-stack.md)
- Want exact artifact contents and paths: [Artifacts and filesystem](./reference/artifacts-and-filesystem.md)
- Want terminology help: [Glossary](./reference/glossary.md)
- Want deeper audio/media background: [Audio & media glossary](./audio-glossary.md)
