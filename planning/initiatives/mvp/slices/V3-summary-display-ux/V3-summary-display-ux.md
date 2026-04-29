# V3 — Implement summary display UX in viewer using V0 demo data

## Summary

Implement the summary panel and display UX in the viewer using V0's committed demo data and summary template. The designer focuses purely on rendering and interaction — the template shape and demo content are already in the repo.

## Why this matters

This gives the viewer a polished summary presentation that backend generation (V4) can target without reopening design decisions.

## Problem statement

The viewer has no summary rendering. The template and demo data exist (V0), but nothing displays them yet.

## Scope

- Render the V0 summary template as a polished panel on the single meeting page
- Design the summary display UX: layout, typography, section rendering
- Handle missing-summary fallback gracefully
- Polish the overall viewer UX as needed for summary integration

## Out of scope

- Defining the summary template (done in V0)
- Preparing demo/seed data (done in V0)
- Real LLM summary generation
- Worker/trigger integration
- Building a new static server (existing one is verified working)

## Dependencies

- V0

## Acceptance criteria

- The single-meeting page renders the summary in a polished panel
- Missing-summary behavior is handled gracefully
- The rendering is faithful to the V0 template structure
- The designer can work entirely against V0 pulled demo data with `cassini serve` or `npm run dev`

## Demo / QA checklist

- Start the viewer against V0 demo data
- Open a meeting that contains a summary
- Confirm the summary renders in the intended UI
- Confirm fallback behavior when a meeting has no summary

## Likely code areas

- `cassini-viewer/src/App.svelte`
- `cassini-viewer/src/viewer/loadArtifact.ts`
- viewer-side summary/markdown rendering path

## Implementation notes

- The summary contract is owned by V0 — render it faithfully, do not redefine the structure
- The existing static serve path works — use `cassini serve` or `npm run dev` against V0 data

## Traceability

- **Slice:** V3
- **Affordances:** U8, N14
