# G7 — Unified product CLI & DX foundation

> One unified `cassini` product CLI and portable meeting/artifact model, replacing the old suite of sibling tools that every MVP goal stands on.

- **Project:** Orphaned / cross-cutting — no parent project · **Phase:** PHASE-1 · **Status:** ✅ largely delivered (rearchitecture Phase 1 substantially complete) · **Requirements:** none (no single business requirement — enabling architecture groundwork)

## What this goal is

G7 is the code-hygiene and architecture groundwork beneath the MVP: the rearchitecture of Cassini from a loose suite of sibling tools into one unified `cassini` product CLI with a portable, coherent meeting/artifact model. It establishes a single product boundary (`bin/cassini` with `record` / `build` / `publish` / `serve` / `doctor` / `inspect` / `dev` / `operator` subcommands) and a stable noun vocabulary — run, meeting, site — that the rest of the system can build on.

This goal has no parent project and no business requirement of its own; it predates ticketed tracking and therefore has no Linear D-tasks. Instead it holds the planning and research documents that drove the rearchitecture. It is cross-cutting: it underpins G1–G6 the way a "modular architecture" milestone underpins feature work, without itself shipping a user-facing feature.

## Why it matters

Every MVP goal — operator control plane, live capture, summaries, viewer, rerun reliability, deployment — sits on top of this unified surface. Without a single product CLI and a portable artifact model, each goal would re-fight the old sibling-tool layout and accumulate integration debt. Getting the product boundary right once unblocks all the downstream work cleanly.

## Definition of done / success signals

- The unified product boundary is real and stable enough that product work no longer fights the old subsystem layout.
- A single `cassini` entrypoint exposes the core verbs (`record`, `build`, `publish`, `serve`, `doctor`, `inspect`, `dev`, `operator`).
- The portable meeting/artifact model (run / meeting / site nouns) is consistent across subcommands and reused by downstream goals.
- Rearchitecture Phase 1 of the migration plan is substantially complete.

## Documents this goal holds

This goal predates ticketed (Linear D-task) tracking, so rather than tasks it holds the architecture vision and user-research documents that drove the rearchitecture:

| Document | Role |
| --- | --- |
| [dx-rearchitecture.md](./dx-rearchitecture.md) | Architecture vision — the three nouns (run / meeting / site), the unified product CLI, and the 4-phase migration plan. |
| [dx-execution-plan.md](./dx-execution-plan.md) | The 6-milestone execution roadmap operationalizing the vision — largely realized. |
| [usability-enhancements-plan.md](./usability-enhancements-plan.md) | North-star user-facing model — portable `.opus` files + archives and simplified verbs. |
| [user-review-notes.md](./user-review-notes.md) | Original user research that motivated the rearchitecture — now superseded. |
| [user-review-notes-current.md](./user-review-notes-current.md) | Review of the current `cassini` surface — the up-to-date user-research record. |

## Lineage & remaining work

The rearchitecture proceeded from user research ([user-review-notes.md](./user-review-notes.md), superseded by [user-review-notes-current.md](./user-review-notes-current.md)) into the architecture vision ([dx-rearchitecture.md](./dx-rearchitecture.md)) and a 6-milestone roadmap ([dx-execution-plan.md](./dx-execution-plan.md)). Rearchitecture Phase 1 is substantially complete: the unified `bin/cassini` with its record/build/publish/serve/doctor/inspect/dev/operator subcommands is in place and in active use by the MVP goals.

Remaining work is downhill and future-facing: the later rearchitecture phases (2–4) from [dx-rearchitecture.md](./dx-rearchitecture.md), and the north-star user-facing verbs and portable `.opus`/archive model from [usability-enhancements-plan.md](./usability-enhancements-plan.md). None of it blocks current MVP work — the product boundary is already stable enough that downstream goals no longer fight the old subsystem layout, which satisfies this goal's definition of done.
