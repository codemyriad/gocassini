# Cassini mental model

Cassini records Nextcloud Talk calls, builds portable meetings, and publishes
those meetings for playback, transcript search, annotations, and optional insights.
For a working installation, start with [Quick start](./quick-start.md).

## Artifacts and stages

```text
Talk room -> .run capture -> .meeting build workspace -> .opus meeting -> publish
```

- **`.run`** preserves the captured media and session metadata for reruns.
- **`.meeting`** is an intermediate build directory for inspecting audio,
  transcripts, captions, and optional summaries.
- **`.opus`** is the portable meeting: audio and embedded metadata in one file.
- **`.site`** is an optional static library export containing a catalog and meetings.

The operator persists four stages: **record → build → seal → publish**. Seal
packs and verifies the `.opus` before publication. The CLI's portable output
mode handles that packing within the record/build command.

The installed ExApp publishes recordings to Nextcloud Files. The standalone
Compose stack publishes to a shared site volume. A static export contains its
own viewer shell only when published with `--rebuild-viewer`; otherwise the
host must supply the viewer. See [Core pipeline](./core-pipeline.md) and
[Artifacts and filesystem](./reference/artifacts-and-filesystem.md).

## Runtime and browser surfaces

The **operator** runs CLI subprocesses, persists jobs and attempts in SQLite,
and manages publication, access, annotations, and insights.

The **Cassini app** is the unified Nextcloud interface. It uses `cassini-viewer`
for browsing meetings and provides insight generation and an administrator-only
**Operator** section for jobs and settings. AppAPI enforces API permissions.
The viewer can also run separately as a static or embedded meeting reader.

```text
Nextcloud Cassini app -> AppAPI proxy -> operator -> CLI subprocesses
                                       |        -> SQLite and work files
                                       +--------> Nextcloud Files
```

The standalone development bundle has two services: **operator** and **viewer**.
Run the app's Vite server separately when developing the Operator UI. The
[harness](./components/harness.md) supplies a local Nextcloud/Talk environment;
it is separate from that Compose bundle.

An installed viewer can read and edit annotations through the operator's APIs.
A static viewer reads the published files without those server-backed features.
Nextcloud setup actions can also use the administrator's browser session to
make changes directly in Nextcloud.

## Two ways to run the pipeline

### Operator-managed

Talk's recording controls or the operator API create jobs. The operator admits
recordings, schedules downstream stages, preserves attempts, and publishes the
result. Administrators watch progress in the app's Operator section.

### Standalone CLI

```bash
./bin/cassini record --call "$CALL_URL" --out ./runs/demo.run
./bin/cassini build ./runs/demo.run --out ./meetings/demo.meeting
./bin/cassini publish ./meetings --out ./site --rebuild-viewer
./bin/cassini serve ./site
```

This exposes the intermediate files for debugging. For a portable file directly,
use `./bin/cassini record --call "$CALL_URL" --out demo.opus`.

## Useful terms

- **Job** — one recording and its downstream processing.
- **Attempt** — one execution of that job; a rerun reuses its captured media.
- **`current/`** — the operator's latest reusable artifacts for each job.
- **Seal** — pack and verify the immutable `.opus` artifact for an attempt.
- **Publish** — deliver that artifact to Nextcloud or a static library.

Next: [Operator stack](./operator-stack.md), [local developer stack](./local-developer-stack.md),
or the [glossary](./reference/glossary.md).
