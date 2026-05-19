# Core pipeline

Cassini's main record/build/publish flow is easiest to understand after you have seen the system run once.

If you have not already seen the system run, start with:

- [Quick start](./quick-start.md)

## Pipeline overview

Cassini is built around durable stage boundaries.

```text
Nextcloud Talk room
  -> .run bundle
  -> .meeting bundle
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
- **build** turns that source media into a reusable meeting artifact
- **publish** turns one or more reusable meeting artifacts into a static browser site

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
7. optionally runs readable-text cleanup
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

- `transcript.readable.v1.json`
- `captions.vtt`
- `summary.md`

### Optional layers inside build

Two useful distinctions:

- speech-to-text is part of the normal build path
- readable cleanup and summary generation are capability layers that depend on configuration

So a successful build may produce a perfectly useful `.meeting` even when readable cleanup or summary generation is absent.

### Important distinction: raw `.meeting` vs published viewer artifact

The build stage itself does not have to produce every viewer convenience file.

For example:

- a raw `.meeting` bundle is the canonical built artifact
- publish/export tooling may later materialize viewer-facing files such as `transcript.display.v1.json`

That means a published meeting directory is not necessarily a byte-for-byte copy of the original `.meeting` bundle.

## 3. Publish

### Purpose

Publish turns one or more ready meetings into a static viewer site.

### Accepted inputs

- one ready `.meeting` bundle
- or a directory containing multiple ready `.meeting` bundles

### Output

A **`.site` bundle**.

Typical contents:

- `index.html`
- `assets/...`
- `catalog.json`
- `meetings/<meeting-id>/...`
- site-level `cassini.json`

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

Publish only uses successful ready `.meeting` artifacts as input.

That rule matters both:

- in standalone CLI mode
- and in operator mode, where publish reads the operator’s canonical `current/` meeting library

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

- the canonical built artifact for the multi-stage pipeline
- easy to inspect and publish
- the operator’s publish input format

### `.opus`

Think of portable `.opus` as:

- a one-file packaged meeting
- good for sharing or archiving
- readable by the viewer in portable mode

The key point is that `.opus` is a packaging output, not a separate capture architecture.

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
