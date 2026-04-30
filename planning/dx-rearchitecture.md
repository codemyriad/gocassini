# Cassini DX Rearchitecture

Date: 2026-03-10

## Thesis

Cassini should stop presenting itself as a suite of sibling tools.

That shape is correct for the implementation, but wrong for the user.
Users do not want:

- a recorder package
- a transcriber package
- a publisher package
- a viewer package
- a player package
- a lab package
- a diagnostics package

They want one product that records meetings and turns them into something usable.

So the rearchitecture is this:

- one product: `cassini`
- one supported CLI
- one coherent artifact model
- one obvious first-run experience
- one place for environment checks
- one developer namespace for the harness and low-level tools

Everything else becomes internal.

This is not a documentation pass. It is a boundary rewrite.

## The Current Shape Is Wrong

The current repository teaches the implementation topology instead of the product model.

That creates predictable DX failures:

- too many entry points
- too many nouns for outputs
- too much dependence on internal structure knowledge
- too much exposure to language/runtime boundaries
- too much user-visible coupling to test harness details

The biggest issue is not that the repo is polyglot.
The biggest issue is that the polyglot structure leaks into the product surface.

Polyglot internals are acceptable.
Polyglot user journeys are not.

## New Product Definition

Cassini becomes a single product with three primary user outcomes:

1. Record a meeting
2. Build a meeting package
3. Publish a meeting library

Everything else is either support or development.

### Primary user objects

The product needs only three top-level nouns:

- `run`: a captured meeting with recorder diagnostics
- `meeting`: a browser-ready meeting package
- `site`: a static library of meetings

This replaces the current mix of:

- `.mkv`
- session artifact directories
- `.csr`
- manifest directories
- viewer bundle
- static export
- readable transcript outputs

Users should not have to infer relationships between neighboring files.

## New Output Model

The current output model is too implicit.
The primary output should always be a directory with a manifest at the root.

### `run` bundle

Output of `cassini record`.

```text
weekly-sync.run/
  cassini.json
  recording.mkv
  session/
    session.json
    events.ndjson
    streams/
```

Rules:

- this is the only supported primary output of recording
- the final MKV lives inside the bundle
- diagnostics live inside the bundle
- no hidden sibling directories
- no implied path conventions

### `meeting` bundle

Output of `cassini build`.

```text
weekly-sync.meeting/
  cassini.json
  meeting.webm
  transcript.words.v1.json
  transcript.readable.v1.json
  captions.vtt
  timeline.map.v1.json
  manifest.json
```

### `site` bundle

Output of `cassini publish`.

```text
cassini-site/
  cassini.json
  index.html
  catalog.json
  assets/
  meetings/
    weekly-sync/
      ...
```

Every bundle root contains a `cassini.json` file:

```json
{
  "kind": "run",
  "version": "cassini.run.v1"
}
```

That gives Cassini a stable way to identify what a path represents.

## New CLI Model

The user-facing CLI is one command:

```bash
cassini
```

Its top-level subcommands are:

```text
cassini doctor
cassini demo
cassini record
cassini build
cassini publish
cassini inspect
cassini serve
```

Developer-only commands live under:

```text
cassini dev ...
```

### What this replaces

These should disappear as documented product entry points:

- `cassini-recorder/bin/record-talk.sh`
- `cassini-transcriber/bin/process-meeting.sh`
- `cassini-publisher/bin/process-meeting.sh`
- `cassini-publisher/bin/export-static-meetings.sh`
- `cassini-diagnostics/bin/*`
- `cassini-player/bin/*`
- `cassini-lab/bin/*`

Some of them may still exist internally for a transition period.
They should stop being first-class.

## The New User Journey

### 1. Safe first run

First-time experience must not require:

- a real Nextcloud room
- Docker Compose
- Python knowledge
- Node knowledge
- reading multiple READMEs

The first command should be:

```bash
cassini demo
```

That command should:

- verify minimal prerequisites
- unpack or reuse a bundled demo meeting
- export a demo site
- print one local URL

Optional:

```bash
cassini demo --open
```

This is the product handshake.

### 2. Real recording flow

```bash
cassini record --call "$CALL_URL" --out ./runs/weekly-sync.run
cassini build ./runs/weekly-sync.run --out ./meetings/weekly-sync.meeting
cassini publish ./meetings --out ./site
```

### 3. One-command flow

For the common case:

```bash
cassini pipeline --call "$CALL_URL" --out ./site
```

This can be added after the primary nouns are stable.
Do not start with only a mega-command. The staged model is easier to trust.

## `doctor` Must Become Mandatory Architecture

Today, environment failures surface late and indirectly.
That is unacceptable for a product that spans Docker, ffmpeg, models, caches, GPU, and network services.

`cassini doctor` becomes a first-class command, not an afterthought.

Examples:

```bash
cassini doctor
cassini doctor record
cassini doctor build --device cpu
cassini doctor dev stack
```

It should check:

- available disk space in the configured cache and temp locations
- writability of cache roots
- Docker availability when required
- ffmpeg / ffprobe presence
- whether the viewer assets are bundled and usable
- whether the local transcriber backend is actually runnable
- GPU visibility when `--device cuda` is requested
- whether a supplied path is resolvable from the current working directory

Most importantly, it must fail before expensive work begins.

## Language Strategy

The repo may remain polyglot internally, but the product may not expose that.

### User-visible language policy

Users should not run:

- `go run`
- `python3 cassini-transcriber/...`
- `npm run ...`
- shell wrappers that hop between directories

Users run `cassini`.

### Internal language policy

Use each language for a clear role:

- Go: primary CLI, orchestration, recorder, artifact inspection, path handling, release packaging
- Python: transcriber worker implementation only
- Node/Svelte: viewer application only

This means:

- Go owns the UX
- Python and Node are engines, not products

## Packaging Strategy

The current repo makes source layout feel like installation layout.
That must stop.

### Supported installation surface

Cassini should ship as:

1. one standalone `cassini` binary
2. one optional official transcriber container image

The viewer static assets should be embedded into the `cassini` binary at release time.

That means:

- `cassini publish` does not require local Node
- `cassini serve` does not require local Node
- viewer build tooling is only needed by viewer developers

For local transcription, the supported modes should be reduced to:

- `http`
- `docker`
- `none`

The raw Python mode should remain a developer mode, not a primary user mode.

Reason:

- `docker` is a coherent distribution boundary
- raw local Python is too fragile as a default UX

## Repo Layout

The current top-level package sprawl should be replaced by a product-oriented layout.

Proposed shape:

```text
/cmd/cassini
/internal/record
/internal/build
/internal/publish
/internal/inspect
/internal/doctor
/internal/dev
/transcriber-worker
/viewer
/harness
/docs
```

Interpretation:

- `cmd/cassini`: the only user CLI
- `internal/*`: product logic
- `transcriber-worker`: Python worker implementation
- `viewer`: browser app source
- `harness`: local stack, fixtures, soak tests, synthetic media

What disappears from the top level:

- user-visible sibling products
- the legacy harness directory as an implied public API
- wrapper directories presented as products

`harness/` is explicitly not part of the product boundary.

## Documentation Model

The docs should mirror the new product boundary.

### Root README

The root README should answer only four things:

1. What is Cassini?
2. What are the three core commands?
3. What does the output look like?
4. How do I try it safely right now?

Nothing else belongs above the fold.

### User docs

```text
docs/user/getting-started.md
docs/user/record.md
docs/user/build.md
docs/user/publish.md
docs/user/inspect.md
docs/user/troubleshooting.md
```

### Developer docs

```text
docs/dev/architecture.md
docs/dev/harness.md
docs/dev/viewer.md
docs/dev/transcriber-worker.md
```

The key rule:

- user docs never point to `harness/bin`
- user docs never ask users to invoke package-internal scripts

## Path and Process Rules

Several current problems come from wrapper behavior, not core behavior.

New rules:

### 1. No user-facing command may depend on `cd`

All paths resolve from the invocation working directory.

### 2. `--help` always exits `0`

No exceptions.

### 3. User-facing commands must print progress in product terms

Not:

- raw `ffmpeg` command lines
- raw Python tracebacks
- Node stack traces

Instead:

```text
[1/5] Validating environment
[2/5] Extracting speaker tracks
[3/5] Transcribing audio
[4/5] Building meeting package
[5/5] Writing outputs
```

When a worker fails, Cassini should show:

- a short product-level error
- the likely cause
- the path to the detailed log

### 4. No primary docs should default to `/tmp`

Default outputs should be current-directory based:

```bash
./runs/
./meetings/
./site/
```

Temporary directories are for internal use only.

### 5. All commands should emit one canonical output path at the end

Examples:

```text
run -> /path/to/weekly-sync.run
meeting -> /path/to/weekly-sync.meeting
site -> /path/to/site
```

## Developer Namespace

The current local stack and synthetic fixture flows are useful, but they confuse the main product story.

They should move under:

```text
cassini dev stack up
cassini dev stack down
cassini dev room create
cassini dev fixture prepare
cassini dev fixture stream
cassini dev smoke
```

This does two things:

1. preserves the harness
2. stops the harness from defining the product

## What Must Be Removed, Not Wrapped

To avoid layering more complexity on top, the following must be deleted as product concepts:

### 1. The "suite of sibling tools" framing

Keep the internals, remove the framing.

### 2. Bare MKV plus adjacent hidden session directory as the primary recording output

Replace with a `run` bundle.

### 3. Multiple first-class shell wrappers per subsystem

Replace with one CLI.

### 4. Source-language-native invocations in user docs

No more `go run`, `python3 ...`, `npm run ...` in user onboarding.

### 5. The legacy harness directory as a user-facing surface

Rename it to `harness/` and move it out of the product story.

## Migration Plan

### Phase 1: Introduce the new boundary

- add `cmd/cassini`
- implement `doctor`, `demo`, `record`, `build`, `publish`, `inspect`, `serve`
- define `run`, `meeting`, and `site` manifests
- embed viewer assets in the release binary

### Phase 2: Repoint old commands

Existing wrappers become thin deprecated shims to `cassini`.

Examples:

- `cassini-recorder/bin/record-talk.sh` -> `cassini record`
- `cassini-publisher/bin/process-meeting.sh` -> `cassini build`
- `cassini-diagnostics/bin/inspect-artifact.sh` -> `cassini inspect`

But:

- they are no longer documented
- they print deprecation warnings

### Phase 3: Move internal implementation

- keep the harness under `harness/`
- collapse top-level product directories
- keep Python worker and viewer source as dedicated internal engines

### Phase 4: Remove old public surfaces

- remove deprecated wrappers
- remove obsolete docs
- remove multi-product language from the README

## Success Criteria

The rearchitecture is successful when a new user can do this:

```bash
cassini demo
cassini doctor
cassini record --call "$CALL_URL" --out ./runs/test.run
cassini build ./runs/test.run --out ./meetings/test.meeting
cassini publish ./meetings --out ./site
```

and can answer all of these without reading three READMEs:

- What is Cassini?
- What does it output?
- Where are the diagnostics?
- How do I inspect a failure?
- Which command is for product use and which is for internal development?

## Architectural Recommendation

Do not try to make the current suite feel simpler by adding more wrapper scripts or more top-level docs.

That would preserve the wrong boundary.

The correct move is:

- make `cassini` the only product
- make bundles the only primary outputs
- make `doctor` the gatekeeper
- hide the polyglot internals behind one Go-owned UX
- quarantine the harness under `cassini dev`

That is the simplest shape that can plausibly make this project feel predictable and loved.
