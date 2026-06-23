# G5 — Failure inspection, rerun & reliability

> Pipeline failures are inspectable and rerunnable without manual artifact repair, and publishing stays correct.

- **Project:** [P1 — Cassini internal MVP](P1-cassini-internal-mvp.md) · **Phase:** PHASE-1 · **Requirements:** R7
- **Status:** ✅ Complete (two followups deferred)
- **Last updated:** 2026-06-23

## What this goal is

A recording job moves through staged record → build → publish work. When a stage fails, an operator must see *what* failed and retry it without hand-editing artifacts or re-running expensive upstream stages. This goal makes that loop first-class: every job carries a persisted attempt history, reruns are a single API/UI action, and post-processing can be retried from a stable canonical recording boundary rather than starting over from capture. It also closes the publish reliability gap — promoting a freshly built site into the live location is atomic and honest.

## Why it matters

Without this, any transient failure (or a fixable post-processing bug) means manually salvaging artifacts and re-recording — unusable for a pilot. Inspectable, idempotent reruns plus atomic publish make the pipeline operationally trustworthy (R7).

## Definition of done

- Failed jobs are inspectable: which attempt failed and why.
- Jobs can be rerun via API and from the control panel, producing a new attempt rather than mutating the old one.
- Post-processing (build/publish) can be rerun **without** re-recording, from a canonical "ready" boundary.
- The on-disk job layout is stable and predictable (`current/` + `runs/`).
- Publishing into the live site is atomic with rollback; a failed publish leaves the live site untouched, with lineage recorded.

## Work done

| Task | What it delivered |
| --- | --- |
| [D-243 — failure-inspection-and-rerun-flow](D-243-failure-inspection-and-rerun-flow) | Slice V5: two-level persistence (`jobs` + `job_attempts` history), `POST /jobs/:id/rerun`, and an inspection read surface — on top of the [G1](G1-operator-control-plane.md) control plane. |
| [D-280 — rerun-only-postprocessing-jobs](D-280-rerun-only-postprocessing-jobs) | Downstream-only rerun from the canonical ready `.run` boundary; stable `current/` + `runs/` filesystem split; control-panel rerun button. |
| [D-281 — publish-dest-conflict](D-281-publish-dest-conflict) | Atomic publish swap: attempt-local site bundles promoted into the live `site_root` with rollback; publish lineage metadata in `cassini.json`. |

## Work TODO

None — all tasks associated with this goal are complete.

## Gaps to completion

The reliability loop is delivered. Two **deferred followups** from D-280 remain open as future hardening, not blockers:

- Raw `recording.mkv` salvage (recover when the canonical `.run` boundary is unavailable).
- Artifact-retention policy.

Cross-cutting: [qa-2026-05-04](qa-2026-05-04) touches this surface (QA-02).
