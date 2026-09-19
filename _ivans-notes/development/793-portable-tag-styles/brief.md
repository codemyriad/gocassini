# D-793: portable tag styles

## Request

Check Linear D-793 against the work in this branch. Understand the ticket and code, describe how it extends the branch's flow, and resolve open questions before planning and implementation.

Final planning instruction: "Please do so. Don't implement just make sure all plans are in place for a different session to impelment."

Planning baseline: `feat/tag-queue`, HEAD `0ac233f2`, 2026-09-18. No tracked worktree changes were present. No product changes, commits or external writes are authorized by this planning request.

## Scope and related work

- [D-793](https://linear.app/code-myriad/issue/D-793): carry optional tag colour and icon in recordings; offline authoring, browser rendering, mark writes and restyle fan-out. No archive-wide backfill or format-spec work.
- [D-774](https://linear.app/code-myriad/issue/D-774): separate format work; explicitly not a blocker for D-793.
- [D-775](https://linear.app/code-myriad/issue/D-775): public read-only viewer; its reader and palette-dealing behavior are present in this branch.
- [D-746](https://linear.app/code-myriad/issue/D-746): existing tag manager, palette and caller-scoped jobs. Its global JSON style design is superseded here; retain the other UI/access behaviors.
- [D-737](https://linear.app/code-myriad/issue/D-737): embedded annotations and operations. Its old disposable-index/file-first description is superseded by this branch's durable snapshots.
- [D-773](https://linear.app/code-myriad/issue/D-773): branch implementation preserves selected IDs across meetings; styles must follow that identity, including bulk tagging and merges. Do not redo the original bug investigation.
- [D-771](https://linear.app/code-myriad/issue/D-771) and [D-772](https://linear.app/code-myriad/issue/D-772): related search/filter work surfaced in D-746 relations. Preserve existing name/filter behavior; no additional search redesign is included.

D-793, D-746, D-737 and D-773 were reread during final planning. D-774/D-775 were read earlier in this task. No ticket was modified. The final user decision intentionally replaces D-793's installation-vocabulary-first precedence with recording-local appearance and a legacy-only bridge.

## Current direction

The user explicitly simplified the scope: whatever happens to tag names should happen to styles too. `plan.md`, `shaping.md` and `slices.md` are the current plan, superseding central authority, global propagation and lazy reconciliation proposals. Implementation has not started. Start the next session with `handoff.md`.
