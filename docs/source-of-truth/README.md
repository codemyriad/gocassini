# Cassini source of truth

This directory is the canonical, manually maintained source of truth for Cassini's product, architecture, runtime model, and deployment story.

Agents should read this first when they need to:

- understand how the repo fits together
- regenerate audience-specific docs
- check which facts are repo-level contracts vs implementation detail

Humans should update this layer first. Audience-specific docs under `docs/generated/` are derivative.

## Read this in order

1. [Core flows and artifacts](./core-flows-and-artifacts.md)
2. [System architecture](./system-architecture.md)
3. [Operator](./operator.md)
4. [Control panel](./control-panel.md)
5. [Viewer](./viewer.md)
6. [Deployment](./deployment.md)
7. [Local development](./local-development.md)

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

## Supporting references outside this directory

These are still useful inputs for agents and human writers when a target docset needs more detail:

- [docs/architecture.md](../architecture.md)
- [docs/portable-meeting-format.md](../portable-meeting-format.md)
- [docs/audio-glossary.md](../audio-glossary.md)
- [deployment/README.md](../../deployment/README.md)
- component deep-dives under `cassini-go-recorder/docs/`, `cassini-transcriber/docs/`, and `cassini-viewer/docs/`
