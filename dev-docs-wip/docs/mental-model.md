# Cassini mental model

This page gives you the smallest useful model of the system.

If you have not already run the happy path, do that first:

- [Quick start](./quick-start.md)

## The easiest way to think about Cassini

Cassini is a **file-driven meeting pipeline**.

It does three things:

1. **record** a meeting
2. **build** a reusable meeting artifact from that recording
3. **publish** one or more built meetings into a static site

The important part is that each stage writes durable files to disk. Later stages consume those files rather than depending on hidden in-memory state.

## The core artifact flow

```text
Nextcloud Talk room
  -> .run bundle
  -> .meeting bundle
  -> .site bundle
  -> viewer
```

### What those artifacts mean

- **`.run`** — reusable output of the capture stage
- **`.meeting`** — reusable output of the build stage
- **`.site`** — static export for browser delivery

Cassini also supports a portable one-file output:

```text
... -> one .opus file
```

That `.opus` file is still built from the same underlying capture/build flow. It is not a second recording architecture.

See more:

- [Core pipeline](./core-pipeline.md)
- [Artifacts and filesystem](./reference/artifacts-and-filesystem.md)

## The main runtime pieces

In local end-to-end development, you usually run two stacks at once.

### 1. Harness

The harness gives you a local Nextcloud Talk environment to record against.

Think of it as the local lab for:

- starting Talk
- creating rooms
- running smoke tests and fixtures

### 2. Deployment bundle

The deployment bundle gives you the main Cassini runtime:

- **operator** — runs jobs and owns state
- **control panel** — browser UI for operating the operator
- **viewer** — browser UI for reading published results

## Browser and backend boundaries

```text
browser
  -> control panel
  -> viewer

control panel
  -> operator API

operator
  -> SQLite
  -> work root
  -> shared published-site storage
  -> cassini CLI subprocesses

viewer
  -> shared published-site storage (read-only)
```

This separation is deliberate.

- The **control panel** is for operating jobs.
- The **viewer** is for consuming published meetings.
- The **operator** is the only runtime service that mutates state.

## Two main ways to use Cassini

### Operator-managed flow

This is the product-shaped runtime:

- jobs are created through an HTTP API
- the operator persists jobs and attempts
- the control panel watches status updates
- the viewer serves published output

Use this when you care about end-to-end behavior.

### Standalone CLI flow

This is the transparent pipeline view:

```bash
./bin/cassini record --call "$CALL_URL" --out demo.run
./bin/cassini build demo.run --out demo.meeting
./bin/cassini publish ./meetings --out site
./bin/cassini serve ./site
```

Use this when you want to inspect stage boundaries directly.

## Three architectural rules that explain most of Cassini

### 1. Durable files are the contract between stages

This is why Cassini can:

- inspect failures after the fact
- rerun downstream work from preserved artifacts
- keep the viewer static and simple

### 2. The operator orchestrates; it does not reimplement the pipeline

The operator shells out to the Cassini CLI for record, build, and publish.

That keeps:

- one source of truth for artifact production
- better parity between CLI and operator mode
- cleaner separation between orchestration and media processing

### 3. The control panel and viewer solve different problems

The control panel is about:

- starting work
- stopping work
- rerunning work
- watching job state

The viewer is about:

- opening the published site
- playing audio
- reading transcripts and summaries

They are intentionally separate applications.

## A minimal glossary

- **Talk room** — the Nextcloud Talk meeting being recorded
- **record** — join the room and capture source media
- **build** — turn captured media into a structured meeting artifact
- **publish** — export one or more meetings into a static viewer site
- **job** — one logical unit of operator-managed work
- **attempt** — one execution pass for a job
- **`current/`** — the operator’s canonical library of latest reusable artifacts per job
- **portable `.opus`** — one-file packaged meeting output

For more terms, including audio/container terminology, see:

- [Glossary](./reference/glossary.md)
- repo reference: `docs/audio-glossary.md`

## Where to go next

- Want the local runtime topology: [Running the local developer stack](./local-developer-stack.md)
- Want the operator model: [Operator stack](./operator-stack.md)
- Want stage-by-stage details: [Core pipeline](./core-pipeline.md)
