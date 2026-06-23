# G1 — Operator control plane

> A backend control surface that lets an operator trigger recordings remotely and track job state end-to-end — no SSH, no manual artifact stitching.

- **Project:** [P1 — Cassini internal MVP](../P1-cassini-internal-mvp/spec.md) · **Phase:** PHASE-1 · **Status:** ✅ completed · **Requirements:** R3

## What this goal is

G1 turns Cassini from a hands-on CLI workflow into an operable service. It introduces a long-running `cassini-operator` process that accepts recording jobs over an HTTP API, persists their state, drives them through staged worker pools (record → build → publish), and exposes that lifecycle so an operator can start, observe, and reason about jobs from one place. On top of that backend it adds a browser control panel: a human-facing surface to start/stop jobs and watch them progress live.

The goal is deliberately scoped to the *control plane* — the API, the job-state model, the worker staging, and the operator UI — rather than the capture or publish internals it orchestrates. Those are owned by sibling goals and plug into the foundation built here.

## Why it matters

R3 requires that an operator can trigger a recording without SSHing into a box or manually stitching artifacts together. This goal is the backbone that makes every other MVP workflow operable: live capture, failure inspection, and rerun all build on the job lifecycle and control surface established here.

## Definition of done / success signals

- A standalone `cassini-operator` process accepts jobs via `POST /jobs` and reports them via `GET /jobs`.
- Job state is durably persisted (SQLite) and survives an operator restart (restart recovery).
- Jobs advance through staged record/build/publish worker pools, with publish refresh handled by the operator.
- An operator can start, stop, and observe jobs from a browser control panel with live (SSE) updates — no SSH or manual artifact handling.

## Tasks

| Task | Status | What it delivered |
| --- | --- | --- |
| [D-233 — job scheduler setup](../D-233-job-scheduler-setup) | ✅ | Slice V1: the `cassini-operator` process, `POST`/`GET /jobs`, SQLite job state, staged record/build/publish worker pools, publish refresh, and restart recovery. |
| [D-266 — operator control panel](../D-266-operator-control-panel) | ✅ | The browser control panel to start/stop/observe jobs with live SSE updates. |

## Lineage & remaining work

**Lineage.** D-233 laid the operator backbone (process, API, job state, worker staging); D-266 added the human-facing control panel on top of it. This control plane is the foundation that sibling goals build on — [D-234 live capture (G2)](../G2-live-meeting-capture/goal.md) drives real recordings through the operator, and [D-243 failure inspection & rerun (G5)](../G5-failure-rerun-reliability/goal.md) extends the job-state model with attempt history and rerun.

**Remaining work.** The goal itself is met. Deferred D-266 followups remain as enhancements rather than blockers: rerun controls in the UI, richer trigger fields, fine-grained progress, deployment packaging, Nextcloud integration, and styling polish. Several of these are picked up downstream — rerun by G5, deployment packaging by [G6 self-host deployment](../G6-self-host-deployment/goal.md), and NC integration by [G2](../G2-live-meeting-capture/goal.md).
