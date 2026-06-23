# G6 — Self-host deployment & pilot docs

> Ship a deployable self-host bundle plus pilot quickstart docs, so a pilot operator can stand up Cassini on their own hardware with owned storage and a documented setup path.

- **Project:** [P1 — Cassini internal MVP](../P1-cassini-internal-mvp/spec.md) · **Phase:** PHASE-1 · **Status:** ◑ in progress (planning done, build pending) · **Requirements:** R1, R8

## What this goal is

G6 turns the working Cassini stack into something a pilot operator can actually run themselves. It packages the three runtime services — `cassini-operator`, `cassini-control-panel`, and `cassini-viewer` — into a single Docker Compose bundle with a `CASSINI_*` config contract, shared volumes for owned storage, and a same-origin proxy so the surfaces compose into one deployment. Alongside the bundle it delivers the pilot quickstart documentation: the setup path plus the hardware and model expectations an operator needs before standing it up.

This is the MVP "V6" release slice. It depends on the operator, control panel, viewer, and pipeline goals being functional, and it draws the line at making them deployable and documented for self-hosting rather than only runnable in the dev harness.

## Why it matters

R1 requires Cassini to be self-hostable with owned storage and a documented path, and R8 requires real setup docs with hardware and model expectations — without this slice the rest of the MVP cannot reach a pilot. It is one of the two principal remaining Phase-1 work items, alongside [D-249 viewer UX restructure](../G4-viewer-delivery-surface/goal.md).

## Definition of done / success signals

- A single Docker Compose bundle brings up operator + control panel + viewer together.
- A documented `CASSINI_*` config contract drives the bundle's configuration.
- Shared volumes give the operator owned, persistent storage for jobs and artifacts.
- A same-origin proxy presents the three surfaces as one deployment.
- Pilot quickstart docs cover the setup path plus hardware and model expectations (R8).
- A pilot operator can self-host on their own hardware following only the documented path (R1).

## Tasks

| Task | Status | What it delivered |
| --- | --- | --- |
| [D-246-deployment-bundle-1](../D-246-deployment-bundle-1) | ◑ | Shaping/planning complete; implementation I1/I2/I3 pending. Docker Compose bundle packaging `cassini-operator` + `cassini-control-panel` + `cassini-viewer` with a `CASSINI_*` config contract, shared volumes, and a same-origin proxy. |

## Lineage & remaining work

D-246 is the single task carrying this goal. Its shaping and planning are complete, but the three implementation increments (I1/I2/I3) are still pending — this is the build-phase work that remains to close G6 and ship the V6 release slice.

Cross-cutting overlap:

- **Distribution images / ExApp** — [P2 — Nextcloud marketplace readiness](../P2-nextcloud-marketplace-readiness/spec.md) handles ExApp packaging and parakeet runtime images. It is a distinct project from P1, but its image work overlaps the self-host bundle's runtime packaging and should be kept aligned.
- **QA audit** — [qa-2026-05-04](../qa-2026-05-04) findings QA-04 and QA-05 (operator runtime state + CI) touch this goal and should be reconciled as the bundle is built.

Remaining toward the goal: execute D-246 I1/I2/I3 (Compose bundle, config contract, proxy + shared volumes) and author the pilot quickstart docs covering setup path and hardware/model expectations.
