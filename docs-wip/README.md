# Cassini reference documentation

This directory is the current reference for how Cassini records meetings, builds artifacts, publishes viewer sites, and runs as an operator-managed deployment.

It is written to support three audiences at once:

- agents that need an accurate mental model of the system
- developers working on the codebase
- admins/operators running the deployed stack

## Read this in order

1. [Core flows and artifacts](./core-flows-and-artifacts.md)
2. [System architecture](./system-architecture.md)
3. [Operator](./operator.md)
4. [Control panel](./control-panel.md)
5. [Viewer](./viewer.md)
6. [Deployment](./deployment.md)

## Cassini in one page

Cassini is a file-driven meeting pipeline with three core stages:

1. **Record** — join a Nextcloud Talk room and capture reusable source media
2. **Build** — turn that source media into a meeting artifact or a portable `.opus` file
3. **Publish** — turn one or more built meetings into a static viewer site

Cassini supports two main operating styles:

- **Explicit bundle flow**: `.run` -> `.meeting` -> `.site`
- **Portable single-file flow**: one `.opus` file per meeting

The long-running operator deployment runs the same logical pipeline, but adds:

- persisted jobs and attempts
- recording-slot admission control
- queued build workers
- serialized publish promotion into a shared live site
- a browser control panel
- a separate read-only viewer service

## Core architectural rules

- Durable files are the primary contract between stages.
- The operator orchestrates the pipeline but does not reimplement record/build/publish internals.
- The control panel talks only to the operator API.
- The viewer reads only published static files.
