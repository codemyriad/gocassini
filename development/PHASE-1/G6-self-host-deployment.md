# G6 — Self-host deployment & pilot docs

> Ship a deployable self-host bundle plus pilot quickstart docs, so a pilot operator can stand up Cassini on their own hardware with owned storage and a documented setup path.

- **Project:** [P1 — Cassini internal MVP](P1-cassini-internal-mvp.md) · **Phase:** PHASE-1 · **Requirements:** R1, R8
- **Status:** ◑ In progress — planning done, build pending
- **Last updated:** 2026-06-23

## What this goal is

G6 turns the working Cassini stack into something a pilot operator can run themselves. It packages the three runtime services — `cassini-operator`, `cassini-control-panel`, and `cassini-viewer` — into a single Docker Compose bundle with a `CASSINI_*` config contract, shared volumes for owned storage, and a same-origin proxy. Alongside the bundle it delivers the pilot quickstart documentation: the setup path plus the hardware and model expectations an operator needs.

This is the MVP "V6" release slice. It depends on the operator, control panel, viewer, and pipeline goals being functional, and draws the line at making them deployable and documented for self-hosting.

## Why it matters

R1 requires Cassini to be self-hostable with owned storage and a documented path; R8 requires real setup docs with hardware/model expectations. Without this slice the rest of the MVP cannot reach a pilot. It is one of the two principal remaining Phase-1 work items, alongside [D-249](G4-viewer-delivery-surface.md).

## Definition of done

- A single Docker Compose bundle brings up operator + control panel + viewer together.
- A documented `CASSINI_*` config contract drives configuration.
- Shared volumes give the operator owned, persistent storage for jobs and artifacts.
- A same-origin proxy presents the three surfaces as one deployment.
- Pilot quickstart docs cover the setup path plus hardware and model expectations (R8).
- A pilot operator can self-host on their own hardware following only the documented path (R1).

## Work done

| Task | What it delivered |
| --- | --- |
| [D-246 — deployment-bundle-1](D-246-deployment-bundle-1) | **Shaping/planning only.** Selected shape B (three containerized services, host-published ports, shared volumes, same-origin proxy); two spikes resolving operator runtime dependencies and the `CASSINI_*` service-config contract; three implementation slices defined (I1/I2/I3). |

## Work TODO

| Task | Status | Outstanding |
| --- | --- | --- |
| [D-246 — deployment-bundle-1](D-246-deployment-bundle-1) | ◑ Build pending | Implement I1 (packaged operator + control-panel), I2 (viewer + shared-site handoff), I3 (final bundle contract + docs). |
| Pilot quickstart docs | ⏳ Not started | Setup path, hardware/model expectations, auth/reverse-proxy responsibility, storage ownership, out-of-scope items (R8). |

## Gaps to completion

- Execute D-246 I1/I2/I3: the Compose bundle, `CASSINI_*` config contract, same-origin proxy, and shared volumes.
- Author the pilot quickstart docs (R8).
- Reconcile [qa-2026-05-04](qa-2026-05-04) findings QA-04/QA-05 (operator runtime state placement, CI) as the bundle is built.

Cross-cutting overlap: [P2 — Nextcloud marketplace readiness](P2-nextcloud-marketplace-readiness.md) handles ExApp packaging and Parakeet runtime images — a distinct project whose image work overlaps this bundle's runtime packaging and should be kept aligned.
