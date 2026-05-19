# Task brief: generate one docset

Use this when updating a single audience/output target.

## Inputs

- `docs/docsets.yaml`
- one docset id from `docsets.yaml`
- `docs/source-of-truth/README.md` and the relevant linked source docs
- the chosen structure file
- any listed supporting references that help verify or deepen the source

## Process

1. Read `docs/docsets.yaml` and identify the chosen docset.
2. Read the matching structure file under `docs/structures/`.
3. Read `docs/source-of-truth/README.md`.
4. Read the docset's `primarySources` from `docs/docsets.yaml`.
5. Use supporting references only when they help confirm or sharpen the source material.
6. Update only the output files declared for that docset.

## Rules

- Source of truth wins.
- Keep the audience boundary tight.
- Do not invent facts, commands, or capabilities.
- Reorganize and condense freely, but do not silently change meaning.
- Preserve paths, command names, and important constraints.
- If the source is missing something important, note the gap instead of guessing.
- Do not mention `docs/source-of-truth/` inside generated docs.
- Do not open generated docs with audience declarations or generation meta.
- Make the output read like standalone documentation for the target audience.

## Deliverable

A refreshed audience-specific doc or doc collection under `docs/generated/`.
