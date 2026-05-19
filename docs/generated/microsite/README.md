# Cassini overview

## Cassini at a glance

Cassini records Nextcloud Talk meetings and turns them into durable meeting artifacts that can be reviewed later in a browser or packaged as one portable file.

The user-facing story is simple:

1. point Cassini at a meeting
2. record and process it
3. end up with one portable `.opus` file or a published viewer site

## How it works

Under the hood, Cassini is a file-driven pipeline:

```text
Talk room -> .run -> .meeting -> .site -> viewer
```

Those artifacts matter because they make the system inspectable and rerunnable. Each stage leaves a durable output that later stages can reuse.

## Product surfaces

### CLI

The `cassini` CLI is the main product entrypoint. It exposes the record, build, publish, inspect, serve, and doctor flows.

### Operator + control panel

For long-running operation, Cassini adds an operator service and a browser control panel. The operator manages jobs, attempts, reruns, and live-site publication.

### Viewer

The viewer is a separate read-only browser surface for consuming published meetings. It reads static files and does not depend on the operator API.

## Why the architecture is shaped this way

Cassini separates responsibilities deliberately:

- durable artifacts carry work between stages
- the operator orchestrates but does not reimplement media processing
- the control panel is for operating jobs
- the viewer is for consuming published meeting output

That keeps the control plane and the media pipeline loosely coupled.

## Deployment story

The packaged deployment combines:

- the operator
- the control panel
- the viewer
- shared persistent storage for operator state and the live published site

This allows one service to manage meeting jobs while another serves the read-only published result.

## Current scope

Today Cassini is strongest in the technical pipeline itself:

- Nextcloud Talk capture
- file-based build artifacts
- portable meeting packaging
- static-site export
- operator-backed orchestration

## Read next

- [`site-map.md`](./site-map.md)
- [`docs/generated/developer/README.md`](../developer/README.md)
- [`docs/generated/admin/README.md`](../admin/README.md)
