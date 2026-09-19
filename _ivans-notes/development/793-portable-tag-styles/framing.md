# D-793 — Portable tag appearance

Status: framed for implementation, 2026-09-18. Planning only; no product code changed. Repository shaping skill packages were unavailable, so these documents use the repository workflow directly.

## Problem

Names and marks travel in a recording but colours/icons currently live in installation-wide `tag-styles.json`. A downloaded recording therefore loses its authored appearance. This branch already provides durable annotation snapshots and asynchronous archive sync; styles should use that same path.

The user explicitly chose simplicity: make styles behave like existing names. Each recording owns its definitions, and a tag-manager edit propagates only to recordings the caller can access. Earlier central-authority, global propagation and lazy reconciliation proposals are superseded.

## Desired outcome

An app-authored or offline-authored recording carries its tag appearance. After archive sync reports saved, the downloaded file and public viewer agree with the meeting view. A restyle behaves like a rename, including scope, partial progress, restart handling and archive delay.

```text
Caller edit -> scoped DB snapshots -> existing archive workers -> portable file
                    |
                    v
             current meeting UI
```

## Requirements

| ID | Requirement |
|----|-------------|
| R1 | Optional colour/icon live once per tag in `annotations.tags[]`; items reference tag IDs. |
| R2 | Preserve old recordings and unknown style tokens during unrelated rewrites. |
| R3 | Offline creation styles and ID-based restyle work without an operator. |
| R4 | New marks and atomic bulk marks capture styles in their original snapshot. |
| R5 | Rename/restyle/combined edits use caller-visible carrier jobs, one mutation per target. |
| R6 | Local definitions drive meeting and meeting-list appearance; aggregate vocabulary is picker metadata. |
| R7 | Preserve existing style data through a read-only compatibility bridge, without an archive-wide rewrite. |
| R8 | Keep exact snapshots, request receipts, revision checks, audio binding and archive verification intact. |
| R9 | Add no file reads or repair jobs on normal viewing; use DB carrier lookup and existing bounded workers. |
| R10 | Retain useful tag-manager attribution without a writable global style authority. |

## Non-goals

No central authoritative definitions, global style override, privileged global target selection, per-tag revision protocol, reconciliation on access, global name/search redesign, broad merge/delete redesign, archive-wide backfill, format-spec edits or in-place Opus optimization. Dormant and inaccessible recordings may retain different definitions indefinitely.

## Accepted decisions

- Parity begins after archive synchronization, not merely an accepted request or completed tag job.
- Existing offline tags are restyled by ID.
- The final instruction to mirror names supersedes the earlier privileged fan-out and lazy-sync discussion.
- Existing JSON styles become compatibility input only; do not silently discard them.

## Scope references

See `brief.md` for Linear links and `shaping.md` for the exact contract. The local agreed design intentionally supersedes D-793's installation-vocabulary-first rendering rule. Do not silently restore that rule from the ticket during implementation.
