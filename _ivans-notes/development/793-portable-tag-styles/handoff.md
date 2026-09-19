# Implementation handoff — D-793

Planning complete. No implementation, tests, commits, deployments or Linear edits were performed for this task in the planning session. Start coding only when the user explicitly authorizes execution in the next session.

## Start here

Read the repository `AGENTS.md` and `CONTRIBUTING.md`, then these task files in order:

1. `brief.md` — request, ticket scope and branch context.
2. `framing.md` — accepted requirements and non-goals.
3. `plan.md` — concise agreed behavior.
4. `shaping.md` — exact wire, migration, legacy, mutation and rendering contracts.
5. `breadboarding.md` — user/API/data paths.
6. `slices.md` — ordered commit-sized work and gates.
7. `validation.md` — acceptance matrix, setup and commands.
8. `open_questions.md` — no blocking product questions.

`assessment.md` and `lazy-tag-reconciliation.md` are historical exploration. They contain superseded decisions. Do not implement authoritative central tags, global fan-out, per-tag revisions or lazy read-time synchronization.

## Baseline and dependencies

- Planning inspected `feat/tag-queue` at `0ac233f2` on 2026-09-18 with no tracked working-tree changes.
- Builds on the shared annotations model, SQLite durable snapshots, asynchronous archive workers, D-773 identity fixes and atomic selection tagging already on this branch.
- Public viewer D-775 code is present in the inspected branch. D-774 format work remains separate and is not a blocker.
- Current durable annotations schema is 5; migration plan targets 6 if no newer migration exists when execution starts.
- No new route, service account, global tag dictionary or external dependency is planned.
- Shaping skill packages named by the repository were not available in the planning session. These documents were authored directly from repository instructions and source inspection; no unavailable skill workflow is claimed.

Run read-only baseline checks before editing:

```sh
git status --short
git branch --show-current
git log -8 --oneline
git check-ignore -v _ivans-notes/development/793-portable-tag-styles/plan.md
```

If the branch moved, inspect relevant differences and adapt mechanical file/line names without discarding the agreed behavior. Preserve unrelated user changes. Reassess schema numbering and migration order before writing code.

## Local planning files and commits

The entire `_ivans-notes` tree is ignored by `/Users/ivan/.gitignore_global` in the inspected environment. Files exist locally and are available to a later session in this workspace, but do not travel automatically with a branch checkout. The final user handoff should call this out.

Repository instructions say to commit the agreed plan when execution is authorized. At that time, stage only the reviewed `.md` task documents explicitly with `git add -f`, inspect the staged diff and make a conventional documentation commit. Do not disable the ignore rule or stage the whole notes tree. If executing in a different checkout, first ensure these planning files were copied there or committed; do not reconstruct the design from the original Linear description alone.

Create `progress.md` at execution start with S1–S5 as ⬜, mark the active slice 🔄, and mark each committed slice ✅ with its commit ID and validation. Implement/validate/commit each slice in order and continue until done or blocked. `implementation.md`, `tutorial.md` and `followups.md` belong at completion and must report real results.

## Critical decisions to preserve

- Definitions are per recording, once per tag ID. Styles behave like names and propagate only to caller-accessible recordings.
- Aggregate vocabulary supplies picker metadata and initial definitions; it does not override explicit local appearance.
- Optional fields retain presence; `icon: ""` remains distinct from absence.
- Unknown stored strings survive unrelated mutations; newly authored tokens use the closed application palette.
- `tagStyles` is creation-only; existing tags use an ID-based `restyle` operation.
- New marks and combined rename/restyle each use their original one-snapshot mutation; bulk mark requests remain atomic.
- Existing JSON styles are read-only legacy fallback, materialized on actual authorized edits, not GETs or a migration sweep.
- Saved means the raw desired document reached and was verified in the archive. Untouched legacy-only appearance remains an explicit compatibility exception.
- Keep old persisted job payloads and receipts restorable. Do not change request identity on restart.
- No implementation permission is implied by this handoff alone.

## Suggested next-session instruction

> Implement D-793 using `_ivans-notes/development/793-portable-tag-styles/handoff.md`. The simplified per-recording style plan is approved. Commit the plan, then execute S1–S5 in order, validating and committing each slice. Preserve unrelated work and existing harness volumes. Do not restore the discarded central-authority or lazy-reconciliation designs.

## Stop conditions

Ask for input only if new evidence makes the accepted behavior impossible, requires changing access scope/portability semantics, or prevents safe migration of existing durable data. Routine helper naming, test organization and equivalent SQL details are implementation choices. If validation is blocked by environment dependencies, exhaust documented safe local alternatives and report precisely what remains unverified.
