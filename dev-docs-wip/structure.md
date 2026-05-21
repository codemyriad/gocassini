# Cassini developer docs structure

This directory is a work-in-progress pass at developer-facing documentation for Cassini.

## Audience

These docs assume the reader is:

- technically capable
- new to Cassini
- not expected to know WebRTC internals or audio-processing terminology up front

## Writing principles

1. **Quick success first**
   - The first path through the docs should get a developer to a visible end-to-end result quickly.
2. **Conceptual depth later**
   - The deeper pages should explain why the system is shaped the way it is.
3. **Reference last**
   - Exact APIs, file layouts, and configuration belong in lookup pages, not in the first-run walkthrough.
4. **Cross-link downwards**
   - Early pages can say “see more” and link to the deeper pages below them.
5. **No prior Cassini vocabulary required**
   - Terms like `.meeting`, `.opus`, `publish`, `attempt`, and `current/` should be introduced before they are used heavily.

## Reading order

Suggested reading order for a new developer:

1. `docs/README.md`
2. `docs/start-here.md`
3. `docs/quick-start.md`
4. `docs/mental-model.md`
5. `docs/local-developer-stack.md`
6. `docs/operator-stack.md`
7. `docs/core-pipeline.md`
8. `docs/components/*`
9. `docs/reference/*`

## Page map

### Top-level onboarding and explanation

- `docs/README.md`
  - entry point and reading paths
- `docs/start-here.md`
  - short orientation for new contributors
- `docs/quick-start.md`
  - happy-path end-to-end walkthrough
- `docs/mental-model.md`
  - system overview, components, and artifact flow
- `docs/local-developer-stack.md`
  - how the local harness and deployment bundle fit together
- `docs/operator-stack.md`
  - operator runtime model, jobs, attempts, workers, promotion
- `docs/core-pipeline.md`
  - record/build/publish flow and `.meeting` vs `.opus`

### Component pages

- `docs/components/README.md`
  - component index
- `docs/components/control-panel.md`
  - operator UI behavior and boundaries
- `docs/components/viewer.md`
  - static meeting-reading UI behavior and inputs
- `docs/components/harness.md`
  - local Talk lab and test harness

### Reference pages

- `docs/reference/README.md`
  - reference index
- `docs/reference/api.md`
  - operator HTTP API and SSE surface
- `docs/reference/configuration.md`
  - deployment, operator, and UI configuration knobs
- `docs/reference/artifacts-and-filesystem.md`
  - artifact contracts and runtime layout
- `docs/reference/glossary.md`
  - Cassini and media terms used in the docs
- `docs/reference/troubleshooting.md`
  - common local-dev and runtime issues

## Doc style by layer

### Task docs

Use for the first-run path:

- `docs/start-here.md`
- `docs/quick-start.md`
- parts of `docs/local-developer-stack.md`

These should answer: **what should I do next?**

### Explanation docs

Use for understanding how Cassini works:

- `docs/mental-model.md`
- `docs/operator-stack.md`
- `docs/core-pipeline.md`
- component pages

These should answer: **how does this system fit together?**

### Reference docs

Use for exact details:

- `docs/reference/*`

These should answer: **what is the exact contract or configuration?**
