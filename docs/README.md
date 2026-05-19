# Documentation workflow

This repo separates documentation into three layers:

1. **Source of truth** — manually maintained repo knowledge
2. **Structures** — audience/output sketches that shape what gets included and how it flows
3. **Generated outputs** — audience-specific docs derived from the first two layers

## Layout

```text
docs/
  README.md                  # this workflow guide
  docsets.yaml               # current docset registry
  source-of-truth/           # canonical manual docs
  structures/                # audience/output structures
  tasks/                     # reusable agent instructions
  generated/                 # derivative outputs
```

## Rules

- Update `docs/source-of-truth/` first.
- Treat `docs/generated/` as derivative output.
- Generated docs should read as standalone deliverables for their target audience.
- Generated docs must not reference `docs/source-of-truth/`.
- Generated docs should not open with audience or generation meta-framing such as “this documentation was written for...” or “this was generated from...”.
- Keep structure files lightweight and adaptable; they are guides, not rigid schemas.
- When source coverage is thin, prefer omission or an explicit gap note over invention.
- Use supporting references only to deepen or verify the source of truth, not to override it casually.

## Current source of truth

Start here:

- [docs/source-of-truth/README.md](./source-of-truth/README.md)

That directory holds the main Cassini narrative for:

- core flows and artifacts
- system architecture
- operator behavior
- control panel
- viewer
- deployment

## Current structures

- [repo-readme.md](./structures/repo-readme.md)
- [developer-docs.md](./structures/developer-docs.md)
- [admin-docs.md](./structures/admin-docs.md)
- [microsite-docs.md](./structures/microsite-docs.md)
- [_template.md](./structures/_template.md)

## Current outputs

- [generated/repo-readme/README.md](./generated/repo-readme/README.md)
- [generated/developer/README.md](./generated/developer/README.md)
- [generated/admin/README.md](./generated/admin/README.md)
- [generated/microsite/README.md](./generated/microsite/README.md)

## How to regenerate one docset

1. Pick a docset from [docsets.yaml](./docsets.yaml).
2. Read the matching structure file.
3. Read the docset's `primarySources` plus the source-of-truth entrypoint.
4. Update only that docset's declared outputs.
5. Keep the audience boundary tight.
6. Make the resulting docs read as deployable standalone docs.

Reusable task brief:

- [tasks/generate-one.md](./tasks/generate-one.md)

## How to regenerate all docsets

Use the same source-of-truth layer, but run through every registered structure and output target.

Reusable task brief:

- [tasks/generate-all.md](./tasks/generate-all.md)

## Adding a new audience or format

1. Copy [structures/_template.md](./structures/_template.md).
2. Sketch the new audience, outcome, flow, include/exclude rules, and candidate outputs.
3. Register it in [docsets.yaml](./docsets.yaml).
4. Generate its outputs into `docs/generated/<target>/`.
5. Keep the generated output self-contained for that audience.
