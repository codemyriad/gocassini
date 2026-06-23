# G5 — Failure inspection, rerun & reliability

> Pipeline failures are inspectable and rerunnable without manual artifact repair, and publishing stays correct.

- **Project:** [Cassini internal MVP](../P1-cassini-internal-mvp/spec.md) · **Phase:** PHASE-1 · **Status:** ✅ Complete · **Requirements:** R7

## What this goal is

A recording job moves through staged record → build → publish work. When any stage fails, an operator must be able to see *what* failed and retry it without hand-editing artifacts on disk or re-running expensive upstream stages. This goal makes that loop first-class: every job carries a persisted attempt history, reruns are a single API/UI action, and post-processing can be retried from a stable, canonical recording boundary rather than starting over from capture.

It also closes the reliability gap on the publish stage — promoting a freshly built site into the live location must be atomic and honest, so a failed or partial publish never corrupts the served site and its lineage stays traceable.

## Why it matters

Without this, any transient failure (or a fixable post-processing bug) means manually salvaging artifacts and re-recording — slow, error-prone, and unusable for a pilot. Inspectable, idempotent reruns plus atomic publish are what make the pipeline operationally trustworthy.

## Definition of done / success signals

- Failed jobs are inspectable: an operator can read what failed and from which attempt.
- Jobs can be rerun via API and from the control panel, producing a new attempt rather than mutating the old one.
- Post-processing (build/publish) can be rerun **without** re-recording, starting from a canonical "ready" recording boundary.
- The on-disk job layout is stable and predictable (`current/` + `runs/` split).
- Publishing into the live site is atomic with rollback; a failed publish leaves the live site untouched and lineage is recorded.

## Tasks

| Task | Status | What it delivered |
| --- | --- | --- |
| [D-243 failure-inspection-and-rerun-flow](../D-243-failure-inspection-and-rerun-flow) | ✅ | Was slice V5. Two-level persistence (`jobs` + `job_attempts` history), `POST /jobs/:id/rerun`, and an inspection read surface — failures become viewable and retryable on top of the G1 control plane. |
| [D-280 rerun-only-postprocessing-jobs](../D-280-rerun-only-postprocessing-jobs) | ✅ | Downstream-only rerun from the canonical ready `.run` boundary, a stable `current/` + `runs/` filesystem split, and a control-panel rerun button — retry build/publish without re-recording. |
| [D-281 publish-dest-conflict](../D-281-publish-dest-conflict) | ✅ | Atomic publish swap: attempt-local site bundles promoted into the live `site_root` with rollback, plus publish lineage metadata in `cassini.json`. |

## Lineage & remaining work

The three tasks layer cleanly:

1. **D-243** establishes the foundation — attempt history and a rerun API — built on the [G1 operator control plane](../G1-operator-control-plane/goal.md).
2. **D-280** makes reruns cheap: by defining a canonical recording boundary and a stable `current/` + `runs/` layout, post-processing can be retried without re-capturing the meeting.
3. **D-281** makes the final stage honest: publish becomes an atomic, rollback-safe swap into the live site with recorded lineage.

All three tasks are complete; this goal is delivered. Deferred followups carried out of D-280 remain open: **raw `recording.mkv` salvage** and an **artifact-retention policy**.

Cross-cutting: the [qa-2026-05-04](../qa-2026-05-04/) repo-alignment audit touches this goal's reliability surface (alongside G4 and G6).
