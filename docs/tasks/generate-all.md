# Task brief: generate all docsets

Use this when the source of truth has changed and every derivative docset should be refreshed.

## Inputs

- `docs/docsets.yaml`
- all structure files referenced there
- `docs/source-of-truth/README.md` and the relevant linked source docs
- supporting references listed in `docsets.yaml` when needed

## Process

1. Read `docs/docsets.yaml`.
2. Read the source-of-truth entrypoint once.
3. For each registered docset:
   - read its structure file
   - read its `primarySources`
   - update its declared outputs
   - keep tone and scope specific to that audience
4. Check that the same core facts remain consistent across all outputs.

## Rules

- Update outputs from the same source pass so they do not drift.
- Source of truth wins over older generated wording.
- Do not force the same section order onto every audience.
- Prefer omission over invention.
- If a target needs a different file split, keep the content aligned but adapt the packaging.

## Deliverable

A fully refreshed `docs/generated/` tree for every registered docset.
