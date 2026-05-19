# Cassini developer docs

## Start here

- New to Cassini: [Start here](./start-here.md)
- Want to see it working first: [Quick start](./quick-start.md)
- Want the system picture: [Mental model](./mental-model.md)

## Suggested reading order

1. [Start here](./start-here.md)
2. [Quick start](./quick-start.md)
3. [Mental model](./mental-model.md)
4. [Running the local developer stack](./local-developer-stack.md)
5. [Operator stack](./operator-stack.md)
6. [Core pipeline](./core-pipeline.md)
7. [Component pages](./components/README.md)
8. [Reference](./reference/README.md)

## Fast paths

### I want to get Cassini running end to end

Read:

1. [Quick start](./quick-start.md)
2. [Mental model](./mental-model.md)
3. [Operator stack](./operator-stack.md)

### I want to work on the runtime or orchestration layer

Read:

1. [Mental model](./mental-model.md)
2. [Running the local developer stack](./local-developer-stack.md)
3. [Operator stack](./operator-stack.md)
4. [Operator API reference](./reference/api.md)

### I want to work on the media pipeline

Read:

1. [Mental model](./mental-model.md)
2. [Core pipeline](./core-pipeline.md)
3. [Artifacts and filesystem](./reference/artifacts-and-filesystem.md)
4. [Glossary](./reference/glossary.md)

### I want to work on the browser apps

Read:

1. [Quick start](./quick-start.md)
2. [Control panel](./components/control-panel.md)
3. [Viewer](./components/viewer.md)

## What Cassini is, in one paragraph

Cassini records a Nextcloud Talk meeting, turns that recording into reusable meeting artifacts, and publishes those artifacts into a static browser-readable site. In local development, you usually run two stacks together:

- the **harness**, which gives you a local Nextcloud Talk environment
- the **deployment bundle**, which gives you the operator, control panel, and viewer

For the harness path, prefer `./bin/cassini dev ...` entrypoints over raw harness `docker compose`, use `127.0.0.1` rather than `localhost` even in the browser, and note that the local harness currently does not work on macOS because of networking issues.

The easiest way to understand the project is to run the happy path first, then read downward into how the pieces fit together.
