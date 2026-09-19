### Fixed
- Corrected installation and development guides for CPU transcription, the unified Cassini app, recording access, and optional summary/insight data sharing.
- Fixed the app’s development proxy so the Operator section, settings and storage controls can reach a root-mounted operator API.
- Fixed readiness requests and proxy path boundaries, rejected fallback/login responses in Operator detection, and stopped storage setup when its refreshed plan no longer includes the selected mode.
- Replaced the homepage’s unsupported recording command with the documented CLI syntax.

- Fixed concurrent atomic file writes by giving each writer its own temporary file.
- Aligned architecture and API docs with the unified app and two-service Compose bundle, and corrected standalone publish examples.

### Changed
- Simplified Go helpers and workflow context handling; shortened repetitive implementation comments.
- Moved the removed Python transcriber's architecture notes into `docs/history/`.

### Removed
- Removed an accidentally tracked harness executable; harness scripts continue to run its Go source.
- Removed obsolete diagnostic reproduction tests; the production setup paths retain regression coverage.
