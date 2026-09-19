### Added
- Recording checks in Operator → Publish pipeline guide administrators through
  storage, processing, Talk authentication and handoff, followed by a real Talk
  recording and playback confirmation. Existing recordings remain accessible.
- Persist the Talk internal secret securely on Cassini's volume, with deployment
  configuration taking precedence. Diagnostics expire and remain advisory;
  missing local credentials are reported before recording starts.
- Provide installation-specific host commands and copyable provider requests,
  plus first-ExApp, ARM64/amd64 and AIO persistence guidance.

### Fixed
- Keep recording-setup edits responsive during background status refreshes and
  prevent older responses from replacing newly saved configuration.
