# G1 — Operator control plane

> A backend control surface that lets an operator trigger recordings remotely and track job state end-to-end — no SSH, no manual artifact stitching.

- **Project:** [P1 — Cassini internal MVP](P1-cassini-internal-mvp.md) · **Phase:** PHASE-1 · **Requirements:** R3
- **Status:** ✅ Complete
- **Last updated:** 2026-06-23

## What this goal is

G1 turns Cassini from a hands-on CLI workflow into an operable service: a long-running `cassini-operator` process that accepts recording jobs over an HTTP API, persists their state, drives them through staged worker pools (record → build → publish), and exposes that lifecycle. On top of the backend sits a browser control panel — a human surface to start/stop jobs and watch them progress live.

The goal is scoped to the *control plane* (API, job-state model, worker staging, operator UI), not the capture or publish internals it orchestrates; those are owned by sibling goals and plug into this foundation.

## Why it matters

R3 requires that an operator can trigger a recording without SSHing into a box or stitching artifacts together. This is the backbone that makes every other MVP workflow operable.

## Definition of done

- `cassini-operator` accepts jobs via `POST /jobs` and reports via `GET /jobs`.
- Job state is durably persisted (SQLite) and survives an operator restart.
- Jobs advance through staged record/build/publish worker pools with operator-driven publish refresh.
- An operator can start/stop/observe jobs from a browser control panel with live (SSE) updates.

## Work done

| Task | What it delivered |
| --- | --- |
| [D-233 — job scheduler setup](D-233-job-scheduler-setup) | Slice V1: the `cassini-operator` process, `POST`/`GET /jobs`, SQLite job state, staged record/build/publish worker pools, publish refresh, restart recovery. |
| [D-266 — operator control panel](D-266-operator-control-panel) | Browser control panel to start/stop/observe jobs with live SSE updates; same-origin proxy model. |

## Work TODO

None — all tasks associated with this goal are complete.

## Gaps to completion

The goal is met. Remaining items are deferred **D-266 enhancements**, not blockers, and most are owned downstream:

- Rerun controls in the UI — delivered by [G5](G5-failure-rerun-reliability.md) (D-280 control-panel rerun button).
- Deployment packaging of the control plane — owned by [G6](G6-self-host-deployment.md).
- Richer trigger fields, fine-grained sub-stage progress, and styling polish — open enhancements.
