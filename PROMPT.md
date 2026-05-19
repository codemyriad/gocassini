# Repo guidance for agents

## Documentation first

When you need repo-level documentation context:

1. Read [`docs/README.md`](docs/README.md).
2. Treat [`docs/source-of-truth/`](docs/source-of-truth/) as the canonical documentation layer.
3. Treat [`docs/generated/`](docs/generated/) as derivative output.
4. Use [`docs/docsets.yaml`](docs/docsets.yaml) plus the matching file in [`docs/structures/`](docs/structures/) when generating or refreshing audience-specific docs.

## Working rules

- Update source-of-truth docs before derivative docs.
- Keep generated docs aligned with their audience structure.
- Prefer current product surfaces over legacy repo history unless legacy context is explicitly relevant.
- When the source of truth is thin, note the gap instead of inventing detail.
