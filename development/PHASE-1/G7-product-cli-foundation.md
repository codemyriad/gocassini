# G7 — Unified product CLI & DX foundation

> One unified `cassini` product CLI and portable meeting/artifact model, replacing the old suite of sibling tools that every MVP goal stands on.

- **Project:** _Orphaned / cross-cutting — no parent project_ · **Phase:** PHASE-1 · **Requirements:** none (enabling architecture groundwork)
- **Status:** ✅ Rearchitecture phase 1 substantially complete
- **Last updated:** 2026-06-23

## What this goal is

G7 is the code-hygiene and architecture groundwork beneath the MVP: the rearchitecture of Cassini from a loose suite of sibling tools into one unified `cassini` product CLI with a portable, coherent meeting/artifact model. It establishes a single product boundary (`bin/cassini` with `record` / `build` / `publish` / `serve` / `doctor` / `inspect` / `dev` / `operator` subcommands) and a stable noun vocabulary the rest of the system builds on.

This goal has no parent project and no business requirement of its own; it predates ticketed tracking, so it has no Linear D-tasks. Instead it holds the planning and research documents that drove the rearchitecture. It is cross-cutting — it underpins G1–G6 the way a "modular architecture" milestone underpins feature work.

## Why it matters

Every MVP goal sits on top of this unified surface. Without a single product CLI and portable artifact model, each goal would re-fight the old sibling-tool layout and accumulate integration debt. Getting the product boundary right once unblocks all downstream work cleanly.

## Definition of done

- The unified product boundary is real and stable enough that product work no longer fights the old subsystem layout.
- A single `cassini` entrypoint exposes the core verbs (`record`, `build`, `publish`, `serve`, `doctor`, `inspect`, `dev`, `operator`).
- The portable meeting/artifact model (run / meeting / site nouns) is consistent across subcommands and reused downstream.

## Reference documents

This goal predates ticketed tracking, so it carries documents rather than D-tasks (in [`G7-product-cli-foundation/`](G7-product-cli-foundation/)):

| Document | Role |
| --- | --- |
| [dx-rearchitecture.md](G7-product-cli-foundation/dx-rearchitecture.md) | Architecture vision — three nouns (run / meeting / site), unified CLI, 4-phase migration plan. |
| [dx-execution-plan.md](G7-product-cli-foundation/dx-execution-plan.md) | 6-milestone execution roadmap operationalizing the vision — largely realized. |
| [usability-enhancements-plan.md](G7-product-cli-foundation/usability-enhancements-plan.md) | North-star user-facing model — portable `.opus` files + archives and simplified verbs. |
| [user-review-notes.md](G7-product-cli-foundation/user-review-notes.md) | Original user research that motivated the rearchitecture — superseded. |
| [user-review-notes-current.md](G7-product-cli-foundation/user-review-notes-current.md) | Review of the current `cassini` surface — up-to-date user research. |

## Work done

- Rearchitecture **phase 1** substantially complete: the unified `bin/cassini` with its record/build/publish/serve/doctor/inspect/dev/operator subcommands is in place and in active use by the MVP goals.
- The portable meeting/artifact model (run / meeting / site) is established and reused downstream.

## Work TODO

- Later rearchitecture phases (2–4) from [dx-rearchitecture.md](G7-product-cli-foundation/dx-rearchitecture.md).
- North-star user-facing verbs and the portable `.opus`/archive model from [usability-enhancements-plan.md](G7-product-cli-foundation/usability-enhancements-plan.md).

## Gaps to completion

The definition of done is met for phase 1 — the product boundary is stable enough that downstream goals no longer fight the old subsystem layout. Remaining work is **downhill and future-facing** (rearchitecture phases 2–4 and the usability verbs) and does not block current MVP work.
