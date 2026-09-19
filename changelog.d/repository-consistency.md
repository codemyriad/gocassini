### Fixed
- Corrected installation and development guides for CPU transcription, the unified Cassini app, recording access, and optional summary/insight data sharing.
- Fixed the app’s development proxy so the Operator section, settings and storage controls can reach a root-mounted operator API.
- Fixed readiness requests and proxy path boundaries, rejected fallback/login responses in Operator detection, and stopped storage setup when its refreshed plan no longer includes the selected mode.
- Replaced the homepage’s unsupported recording command with the documented CLI syntax.

### Removed
- Removed an accidentally tracked harness executable; harness scripts continue to run its Go source.
