# P1 — Cassini internal MVP

> A self-hostable Cassini that records Nextcloud Talk meetings on demand and publishes a polished, browsable meeting library — built as a webhook worker (shape B) around the existing `cassini` CLI/artifact pipeline.

- **Initiative:** [Cassini MVP](../../initiative-cassini-mvp.md) · **Phase:** PHASE-1 · **Status:** ◑ Near-complete — only D-249 (viewer UX restructure) and the D-246 deployment-bundle implementation remain

## What success looks like

A self-hosting operator can deploy Cassini and trigger a Nextcloud Talk recording through a lightweight control surface, and the existing `cassini` pipeline runs the meeting end-to-end — record → transcript → captions → summary — without manual artifact repair. The result is published to a stable URL where users browse a meeting library and open a polished, responsive, deep-linkable single-meeting page: audio playback, click-to-seek transcript, in-meeting search, and a rendered summary. When a run fails it is inspectable and rerunnable rather than abandoned, and publish stays correct. The whole deployment is documented well enough to stand up a serious self-hosted pilot.

This is **shape B**: a webhook worker wrapped around the proven `cassini` CLI and artifact model rather than a from-scratch service. The product surface is delivered through goals **G1–G6**, all resting on the foundational (orphaned, cross-cutting) goal **G7** — the unified `cassini` product CLI and portable meeting/artifact model that every MVP goal builds on.

## Scope

- Operator control plane: remote trigger + job-state tracking and a browser control panel (G1).
- Live end-to-end Nextcloud Talk capture as Talk's native recording backend (G2).
- Meeting summary contract, LLM generation, and viewer display (G3).
- Polished, responsive, deep-linkable meeting library + single-meeting viewer (G4).
- Failure inspection / rerun and publish correctness (G5).
- Self-host deployment bundle + pilot quickstart docs (G6).

## Out of scope

- Public marketplace distribution — ExApp packaging and runtime images live in [P2 — Nextcloud marketplace readiness](../P2-nextcloud-marketplace-readiness/).
- Multi-tenant / hosted SaaS operation; this targets a single self-hosted pilot.
- The G7 CLI rearchitecture itself as deliverable work — it is foundational and largely already delivered, informing (not gated by) P1.

## Goals

| Goal | Status | Focus |
| --- | --- | --- |
| [G1 — operator control plane](../G1-operator-control-plane/goal.md) | ✅ | Backend control surface to trigger recordings remotely + track job state (R3). |
| [G2 — live meeting capture](../G2-live-meeting-capture/goal.md) | ✅ | Record real Nextcloud Talk meetings end-to-end as the native recording backend (R0, R6). |
| [G3 — meeting summary](../G3-meeting-summary/goal.md) | ✅ | Summary contract/template, LLM generation, and viewer display (R5). |
| [G4 — viewer delivery surface](../G4-viewer-delivery-surface/goal.md) | ◑ | Polished, responsive, deep-linkable meeting library + single-meeting viewer (R4, R4.1–R4.3). |
| [G5 — failure & rerun reliability](../G5-failure-rerun-reliability/goal.md) | ✅ | Failures inspectable + rerunnable without manual artifact repair; publish correctness (R7). |
| [G6 — self-host deployment](../G6-self-host-deployment/goal.md) | ◑ | Deployable self-host bundle + pilot quickstart docs (R1, R8). |
| [G7 — product CLI foundation](../G7-product-cli-foundation/goal.md) | ✅ | Cross-cutting: unified `cassini` product CLI + portable meeting/artifact model underpinning all goals. |

## Done criteria

- [ ] An operator has a documented deploy path that stands up the full Cassini stack. _(G6 — bundle build pending)_
- [x] Recordings can be triggered remotely through a lightweight control surface, with job state tracked.
- [x] The existing `cassini` pipeline runs a meeting end-to-end (record → transcript → captions → summary).
- [x] Each completed meeting yields a full artifact: recording, transcript, captions, and summary.
- [ ] Users browse a meeting library and open a responsive, deep-linkable single-meeting page with audio, click-to-seek transcript, and search. _(G4 — D-249 pending)_
- [x] Failed runs are inspectable and rerunnable without manual artifact repair.
- [ ] Pilot quickstart docs are sufficient for a serious self-hosted pilot. _(G6 — docs pending)_
- [ ] Viewer runs on the stock DaisyUI/Tailwind shell with the split Meeting List / Meeting View restructure. _(D-248 done; D-249 split pending)_

> Remaining Phase-1 work: [D-249 viewer UX restructure](../D-249-viewer-ux-restructure) (G4) and the D-246 deployment-bundle implementation ([D-246](../D-246-deployment-bundle-1), G6).

## Shaping & reference docs

- [venture.md](./venture.md) — the venture / problem framing.
- [shaping.md](./shaping.md) — the shaped solution (shape B, requirements R0–R8).
- [slices.md](./slices.md) — the slicing into V0–V8 delivery slices.
- [tickets.md](./tickets.md) — the ticket breakdown into D-tasks.
