# D-793 implementation

Implemented 2026-09-18 on `feat/tag-styles`.

## Delivered

- `AnnotationTag` now stores optional `color` and `icon`; the shared mutation model supports ID-based partial `restyle` operations.
- Tag creation hints are carried through the recorder and operator batch paths into the saved annotation document.
- The operator indexes appearance per `(recording, tag)` and uses the existing indexed carrier lookup to schedule style updates only for recordings readable by the caller.
- The mutable installation-wide `tag-styles.json` store was removed. Tag vocabulary uses a deterministic per-recording representative, while list references and a loaded meeting use that recording's own appearance.
- The viewer accepts `restyle` jobs and renders document-local appearance in meeting and list views.

## Validation

| Command | Result |
|---|---|
| `go -C cassini-annotations test ./...` | passed |
| `go -C cassini-go-recorder test ./internal/cassini ./internal/portable` | passed |
| `go -C cassini-operator test ./internal/operator` | passed |
| `npm --prefix cassini-viewer test` | passed (890 tests) |
| `npm --prefix cassini-viewer run build` | passed |

The viewer build retains two pre-existing Svelte accessibility/unused-export warnings; no new build failure was introduced.
