# P1 — Cassini internal MVP

> A self-hostable Cassini that records Nextcloud Talk meetings on demand and publishes a polished, browsable meeting library — built as a webhook worker (shape B) around the existing `cassini` CLI/artifact pipeline.

- **Initiative:** [Cassini MVP](../initiative-cassini-mvp.md) · **Phase:** PHASE-1
- **Status:** ◑ Near-complete — only G4 (D-249 viewer restructure) and G6 (D-246 deployment bundle) remain
- **Last updated:** 2026-06-23

## What success looks like

A self-hosting operator can deploy Cassini and trigger a Nextcloud Talk recording through a lightweight control surface, and the existing `cassini` pipeline runs the meeting end-to-end — record → transcript → captions → summary — without manual artifact repair. The result is published to a stable URL where users browse a meeting library and open a polished, responsive, deep-linkable single-meeting page: audio playback, click-to-seek transcript, in-meeting search, and a rendered summary. When a run fails it is inspectable and rerunnable; publish stays correct. The deployment is documented well enough to stand up a serious self-hosted pilot.

This is **shape B**: a webhook worker wrapped around the proven `cassini` CLI and artifact model. The product surface is delivered through goals **G1–G6**, all resting on the foundational orphaned goal **G7** (unified `cassini` product CLI + portable artifact model).

## Scope

- Operator control plane: remote trigger + job-state tracking and a browser control panel (G1).
- Live end-to-end Nextcloud Talk capture as Talk's native recording backend (G2).
- Meeting summary contract, LLM generation, and viewer display (G3).
- Polished, responsive, deep-linkable meeting library + single-meeting viewer (G4).
- Failure inspection / rerun and publish correctness (G5).
- Self-host deployment bundle + pilot quickstart docs (G6).

## Out of scope

- Public marketplace distribution — ExApp packaging and runtime images live in [P2 — Nextcloud marketplace readiness](P2-nextcloud-marketplace-readiness.md).
- Multi-tenant / hosted SaaS operation; this targets a single self-hosted pilot.
- The G7 CLI rearchitecture itself as deliverable work — foundational and largely already delivered; it informs (not gates) P1.

## Goals done

| Goal | Focus |
| --- | --- |
| [G1 — operator control plane](G1-operator-control-plane.md) ✅ | Remote trigger + job-state tracking + browser control panel (R3). |
| [G2 — live meeting capture](G2-live-meeting-capture.md) ✅ | Record real Nextcloud Talk meetings end-to-end as native backend (R0, R6). |
| [G3 — meeting summary](G3-meeting-summary.md) ✅ | Summary contract/template, LLM generation, viewer display (R5). |
| [G5 — failure & rerun reliability](G5-failure-rerun-reliability.md) ✅ | Inspectable + rerunnable failures; publish correctness (R7). |
| [G7 — product CLI foundation](G7-product-cli-foundation.md) ✅ | Cross-cutting unified `cassini` CLI + portable artifact model underpinning all goals. |

## Goals TODO

| Goal | Status | Outstanding |
| --- | --- | --- |
| [G4 — viewer delivery surface](G4-viewer-delivery-surface.md) | ◑ In progress | D-249 viewer UX restructure (Meeting List / Meeting View split, URL navigation, mobile) — shaped, not implemented. |
| [G6 — self-host deployment](G6-self-host-deployment.md) | ◑ In progress | D-246 bundle implementation (I1/I2/I3) + pilot quickstart docs — planning done, build pending. |

## Gaps to completion

The end-to-end product path works (trigger → capture → pipeline → summary → publish → inspect/rerun). What stands between current state and "MVP done":

1. **G4 / D-249** — split the viewer into Meeting List + Meeting View with URL-as-source-of-truth navigation and mobile back-to-list.
2. **G6 / D-246** — implement the Docker Compose self-host bundle (I1/I2/I3) and write the pilot quickstart docs (deploy path, hardware/model expectations, auth/storage responsibility).

Done-criteria still open (see shaping): documented deploy path; responsive deep-linkable viewer shell; pilot docs.

## Shaping & reference docs

In [`P1-cassini-internal-mvp/`](P1-cassini-internal-mvp/):

- [venture.md](P1-cassini-internal-mvp/venture.md) — venture / problem framing.
- [shaping.md](P1-cassini-internal-mvp/shaping.md) — the shaped solution (shape B, requirements R0–R8).
- [slices.md](P1-cassini-internal-mvp/slices.md) — slicing into V0–V8 delivery slices.
- [tickets.md](P1-cassini-internal-mvp/tickets.md) — ticket breakdown into D-tasks.
