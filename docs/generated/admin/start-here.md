# Start here

The shortest useful mental model is:

> Cassini is an operator-managed pipeline that records meetings, builds durable artifacts, and publishes a static viewer site.

## The short version

Cassini has three logical stages:

1. **record** — join a meeting and capture source media
2. **build** — turn that source media into a reusable meeting artifact
3. **publish** — refresh the static viewer site from ready meetings

In deployment, three services matter most:

- **operator** — owns jobs, attempts, persistence, and stage execution
- **control panel** — browser UI for creating and watching jobs
- **viewer** — read-only static meeting site

## The first thing to do

Bring up the packaged stack and verify the three surfaces:

- control panel
- operator API
- viewer

That walkthrough is here:

- [Quick start](./quick-start.md)

## The main operational picture

```text
browser -> control panel -> operator API
operator -> SQLite + work root + shared published-site storage
viewer -> shared published-site storage (read only)
```

The operator is the only service that mutates runtime state.

## The storage picture that explains most behavior

The operator keeps:

- a canonical `current/` library of reusable `.run` and `.meeting` artifacts
- retained attempt-local outputs under `runs/`
- a separate live viewer site under `published/`

That split is what makes reruns, inspection, and safe site promotion possible.

## Four rules worth knowing early

1. The operator orchestrates the pipeline; it does not reimplement media processing.
2. The control panel talks only to the operator API.
3. The viewer reads only published static files.
4. Restarting the operator marks in-flight work `interrupted`; it does not automatically resume it.

## Where to go next

- Bring the stack up now: [Quick start](./quick-start.md)
- Understand the service boundaries: [System overview](./system-overview.md)
- Understand runtime behavior: [Operator runtime](./operator-runtime.md)
- Understand storage and live-site swaps: [Storage and promotion](./storage-and-promotion.md)
