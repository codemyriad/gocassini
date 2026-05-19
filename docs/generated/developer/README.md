# Cassini developer docs

This is a developer-facing entrypoint generated from the repo's source-of-truth docs.

## What Cassini is

Cassini is a file-driven meeting pipeline centered on the `cassini` CLI.

At the system level, the core flow is:

```text
Nextcloud Talk room
  -> .run bundle
  -> .meeting bundle
  -> .site bundle
  -> viewer site
```

For the product-facing path, that same pipeline can also end as one portable `.opus` file per meeting.

## The main mental model

Cassini has two layers:

- **product layer** — the `cassini` CLI for standalone use
- **operator layer** — a long-running service that orchestrates the same pipeline asynchronously

The key boundary is durable artifacts. Stages hand off through files, not hidden in-memory state.

## Core artifacts

- **`.run`** — captured source media from a meeting
- **`.meeting`** — built meeting artifact with media, transcript, manifest, and optional readable/summary outputs
- **`.site`** — static viewer site built from one or more ready meetings
- **portable `.opus`** — single-file packaging layered over the same capture/build story

## Repo map

| Path | Role |
|---|---|
| `bin/cassini` | main repo entrypoint for the CLI |
| `cassini-go-recorder/` | live Talk capture, build pipeline, portable packing, CLI implementation |
| `cassini-publisher/` | static-site export |
| `cassini-operator/` | orchestration, persistence, scheduling, SSE, reruns |
| `cassini-control-panel/` | browser UI for operating jobs |
| `cassini-viewer/` | read-only browser playback/transcript surface |
| `deployment/` | packaged Docker Compose topology |
| `harness/` | local dev stack, fixtures, smoke flows |

## Common local entry points

### CLI

```bash
./bin/cassini --help
./bin/cassini doctor
./bin/cassini record --help
./bin/cassini build --help
./bin/cassini publish --help
```

### Operator-backed stack

```bash
cd deployment
docker compose up --build
```

### Local harness flows

```bash
./bin/cassini dev stack up
./bin/cassini dev smoke
```

### Viewer dev flow

```bash
cd cassini-viewer
npm install
npm run build
npm run dev
```

## Boundaries that matter before editing code

- The operator **orchestrates** record/build/publish; it does not reimplement them.
- The control panel talks only to the operator API.
- The viewer reads only static published files.
- Publish is library-oriented: it exports from a set of ready meetings, not just one active job.
- Portable `.opus` output is a packaging form, not a separate recording pipeline.

## Read next

Start with the source-of-truth set:

1. [`docs/source-of-truth/core-flows-and-artifacts.md`](../../source-of-truth/core-flows-and-artifacts.md)
2. [`docs/source-of-truth/system-architecture.md`](../../source-of-truth/system-architecture.md)
3. [`docs/source-of-truth/operator.md`](../../source-of-truth/operator.md)

Then use deeper references as needed:

- [`docs/architecture.md`](../../architecture.md)
- [`docs/portable-meeting-format.md`](../../portable-meeting-format.md)
- [`cassini-go-recorder/docs/`](../../../cassini-go-recorder/docs/)
- [`cassini-viewer/docs/`](../../../cassini-viewer/docs/)
