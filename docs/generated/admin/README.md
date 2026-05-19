# Cassini admin docs

This is an operations-facing entrypoint generated from the repo's source-of-truth docs.

## What the deployed stack contains

The packaged deployment centers on three services:

- **operator** — control plane for jobs, attempts, scheduling, reruns, and live-site promotion
- **control panel** — browser UI that talks only to the operator API
- **viewer** — read-only static site for consuming published meetings

## Topology in one view

```text
browser -> control panel -> operator API
operator -> cassini doctor / record / build / publish
operator -> SQLite + work root + shared published-site storage
viewer -> shared published-site storage (read only)
```

## Runtime model

A live job flows through three logical stages:

1. **record** — capture a reusable `.run`
2. **build** — produce a `.meeting`
3. **publish** — refresh the shared viewer site from ready meetings

The operator persists:

- logical job summaries
- attempt history
- stop metadata
- stage/state transitions

Important behavior:

- record admission is slot-based
- build is queue-backed with a worker pool
- publish is serialized to protect live-site promotion
- reruns start from preserved artifacts instead of re-recording

## Storage model

The operator uses a work root plus a shared site root.

### Canonical reusable artifacts

- `current/<job-id>.run`
- `current/<job-id>.meeting`

These are the stable inputs for downstream reruns and full-site publish.

### Attempt-local retained outputs

- `runs/<job-id>--attempt-XXX.run`
- `runs/<job-id>--attempt-XXX.meeting`
- `runs/<job-id>--attempt-XXX.site`
- `runs/<job-id>--attempt-XXX.logs/`

### Live published site

The operator promotes successful publish output into the shared live site root that the viewer serves.

## Quickstart

```bash
cd deployment
docker compose up --build
```

Default browser surfaces from the checked-in deployment env are:

- control panel: `http://127.0.0.1:4173/`
- operator API: `http://127.0.0.1:4000/`
- viewer: `http://127.0.0.1:8765/`

## Main knobs

Public deployment-facing settings include:

- `CASSINI_OPERATOR_PORT`
- `CASSINI_CONTROL_PANEL_PORT`
- `CASSINI_VIEWER_PORT`
- `CASSINI_OPERATOR_BASE_PATH`
- `CASSINI_MAX_RECORD_WORKERS`
- `CASSINI_MAX_BUILD_WORKERS`

Storage can use Docker volumes by default or host bind mounts through `.env` overrides.

## Operational consequences of the current design

- The operator is a control plane, not a second media-processing implementation.
- Failed stages can leave inspectable retained artifacts.
- Successful publish replaces the live site only after a complete export succeeds.
- The viewer never talks to the operator; it only reads static files.
- Current exported meeting identity is still job-id-centric in operator mode unless another presentation layer changes it.

## Read next

- [`docs/source-of-truth/deployment.md`](../../source-of-truth/deployment.md)
- [`docs/source-of-truth/operator.md`](../../source-of-truth/operator.md)
- [`deployment/README.md`](../../../deployment/README.md)
- [`cassini-operator/README.md`](../../../cassini-operator/README.md)
