# Core flows and artifacts

Cassini is built around durable stage boundaries. Each major step writes files to disk, and later steps consume those files instead of depending on transient in-memory state.

That file-first design is true in both:

- the standalone `cassini` CLI flow
- the long-running operator-managed flow

## The three core stages

### 1. Record

### Purpose

Record joins a Nextcloud Talk meeting and captures reusable source media.

### Main entry points

- `cassini record`
- the operator record stage, which shells out to `cassini record`

### Inputs

- a Nextcloud Talk room URL
- optional recorder display name
- optional duration cap
- optional stop-when-room-empty behavior
- optional room-empty grace period

### Primary outputs

- a **`.run` bundle** in explicit bundle mode
- or, in portable mode, a hidden workspace that eventually becomes one **`.opus`** file

### What a `.run` bundle is

A `.run` bundle is a directory that packages the result of capture.

Typical contents:

- `cassini.json`
- `recording.mkv`
- optional `session/` directory containing recorder session artifacts

### `.run` manifest semantics

`cassini.json` in a run bundle records:

- `kind = run`
- bundle version
- created time
- bundle `state`
- bundle `stage`
- `source_mode` (`talk` or `simulate`)
- recorder name
- recording file path and format
- optional session artifact directory

### Important record-stage behavior

- Live recording writes `recording.mkv`.
- Simulate mode writes `recording.csr` and is debug-only.
- `cassini build` currently requires MKV input, so simulated `.run` bundles are not normal downstream inputs.
- Finalizing a live `.run` may also move a single captured session directory into `session/` inside the bundle.
- In operator mode, a stop request targets the live `cassini record` subprocess with `SIGTERM` and may still produce a usable `.run`.

---

### 2. Build

### Purpose

Build turns captured media into a reviewable meeting artifact.

### Main entry points

- `cassini build`
- the operator build stage, which shells out to `cassini build`

### Accepted inputs

- a ready `.run` bundle
- a raw `.mkv` recording

### Primary outputs

- a **`.meeting` bundle** in explicit bundle mode
- or a final **portable `.opus`** file in single-file mode

### What build actually does

The build pipeline currently:

1. runs build-target doctor checks in the standalone CLI flow
2. resolves the source recording
3. probes the MKV stream layout
4. mixes all speaker tracks into `meeting.webm`
5. computes decoded-audio integrity hashes
6. runs speech-to-text
7. writes the canonical word-timed transcript
8. optionally runs readable-text cleanup and writes captions
9. optionally generates `summary.md`
10. writes `manifest.json`
11. finalizes a `.meeting` bundle or packs everything into `.opus`

Readable cleanup and summary generation are optional capability layers:

- speech-to-text is part of the normal build pipeline
- readable cleanup depends on the configured LLM settings
- summary generation depends on the configured summary LLM settings and can be disabled independently

### What a `.meeting` bundle contains

The core build pipeline always writes:

- `cassini.json`
- `meeting.webm`
- `transcript.words.v1.json`
- `manifest.json`

It may also write:

- `transcript.readable.v1.json`
- `captions.vtt`
- `summary.md`

Additional files may appear later in other flows or tools, for example:

- `transcript.display.v1.json`
- `chapters.vtt`
- `timeline.map.v1.json`

### Important current distinction: build output vs published viewer artifact

The build stage itself does **not** currently generate `transcript.display.v1.json`.

That file is typically materialized later by viewer/export tooling during publish or other post-processing workflows.

So:

- a raw `.meeting` bundle is the canonical built artifact
- a published `meetings/<id>/` directory may contain extra viewer-facing convenience files derived from that bundle

### `.meeting` bundle manifest semantics

`cassini.json` in a meeting bundle records:

- `kind = meeting`
- bundle version
- created time
- bundle `state`
- bundle `stage`
- `source_kind` (`run` or `mkv`)
- `source_path`
- discovered files inside the bundle
- optional pointer to the artifact-level `manifest.json`

### Artifact `manifest.json` semantics

`manifest.json` inside the meeting bundle describes the built meeting artifact itself.

It records:

- source basename
- source duration
- inferred local recorded time
- generated time
- output filenames
- speaker count
- segment count
- word count
- provenance for speech-to-text, readable cleanup, and summary generation

---

### 3. Publish

### Purpose

Publish turns one or more ready meetings into a static viewer site.

### Main entry points

- `cassini publish`
- the operator publish stage, which shells out to `cassini publish`

### Accepted inputs

- a single ready `.meeting` bundle
- a directory containing one or more ready `.meeting` bundles

### Primary output

- a **`.site` bundle**

### What publish actually does

Publish currently:

1. prepares an empty site output directory
2. stages only ready `.meeting` bundles into a temporary input directory
3. skips partial or failed `.meeting` bundles
4. fails if no ready meetings remain after staging
5. invokes the static exporter
6. copies the built viewer shell
7. copies meeting artifacts into `meetings/`
8. materializes `transcript.display.v1.json` in published meeting directories when needed
9. writes `catalog.json`
10. writes site-level `cassini.json`

### Important publish constraints

- Standalone `cassini publish` expects `--out` to point at an **empty** directory.
- Meeting IDs in the published catalog are derived from the source bundle directory names.
- Default catalog titles/date labels are also derived from those published meeting IDs unless another layer rewrites them.
- In operator mode, canonical meeting bundle names are job ids, so the exported viewer catalog is currently job-id-centric unless a later presentation layer changes that.
- If two source bundles collapse to the same meeting ID, publish fails.

### What a `.site` bundle contains

Typical contents:

- `cassini.json`
- `index.html`
- `catalog.json`
- `assets/...`
- `meetings/<meeting-id>/...`

The published `meetings/<meeting-id>/` directories are viewer-ready export artifacts, not necessarily byte-for-byte copies of the original `.meeting` bundle.

### `.site` manifest semantics

`cassini.json` in a site bundle records:

- `kind = site`
- bundle version
- created time
- bundle `state`
- bundle `stage`
- source path summary
- optional `catalog.json` path
- meeting count
- optional live-deployment lineage fields when the operator promotes the site

---

## Operating modes

### Explicit bundle flow

This is the transparent, stage-by-stage flow.

```text
cassini record --out demo.run
  -> demo.run/

cassini build demo.run --out demo.meeting
  -> demo.meeting/

cassini publish ./meetings --out site
  -> site/
```

Use this flow when you need:

- visible intermediate artifacts
- debugging of each stage boundary
- explicit publish inputs
- operator-style observability

### Portable single-file flow

This is the normal end-user flow.

Examples:

```text
cassini record --call <url> --out "Weekly Sync.opus"
```

```text
cassini build /path/to/meeting.mkv --out "Imported Meeting.opus"
```

### How portable mode works internally

Portable mode still uses the same logical stages, but hides the intermediate workspace under a sibling directory:

```text
.cassini-work/<name>.portable-work/
```

That workspace currently contains:

- `capture.run/`
- `meeting.meeting/`

On success, the workspace is deleted.
On failure, it is retained so the same command can be rerun against the same `--out` path.

### Portable resumability

If a portable command fails after recording or after building, rerunning the same command with the same `--out` path can reuse:

- an already-finished capture bundle
- an already-finished meeting bundle
- or a fully-ready `.opus` file

### What a portable `.opus` file is

A Cassini portable meeting is:

- an ordinary Ogg Opus audio file
- with playable audio as the primary payload
- with Cassini metadata embedded in OpusTags
- with a compressed embedded JSON manifest split across `CASSINI_PAYLOAD_000...`

The embedded payload can include:

- meeting identity and timestamps
- audio integrity metadata
- speakers
- canonical transcript items
- readable transcript data
- display transcript data
- summary metadata and summary attachment content
- processing provenance

### Portable integrity model

Portable files carry integrity fields derived from decoded PCM, including:

- sample rate
- channel count
- sample count
- duration
- PCM SHA-256

Consumers should trust embedded transcript metadata only when those integrity values still match the actual audio.

---

## Common bundle state model

The main Cassini bundle types use the same readiness idea.

Common `state` values:

- `preparing`
- `ready`
- `failed`

Common `stage` values differ by bundle type, but always answer the same question:

- where did this artifact stop?

That is what makes partial outputs inspectable and rerunnable.

---

## Mode comparison

| Mode | User-visible output | Best for | Intermediates visible |
|---|---|---|---|
| Explicit bundle flow | `.run`, `.meeting`, `.site` directories | debugging, publishing, operator-style workflows | yes |
| Portable flow | one `.opus` file | end-user archive, sharing, reopen-in-viewer | hidden under `.cassini-work` |
