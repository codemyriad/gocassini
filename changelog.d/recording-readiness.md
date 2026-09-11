### Added
- Cassini Setup now checks recording readiness, supports persistent Talk internal
  secret configuration, provides handoff commands and follows a test recording
  started through Talk through publication and administrator playback confirmation.
- A non-recording `cassini talk-check` probes recording authentication and HPB
  without joining a room or subscribing to media. Live results expire and are
  rechecked after restart; test history remains available.

### Fixed
- Known missing recording credentials are refused before recording starts with
  an actionable setup message. Ordinary users see coarse setup guidance while
  retaining access to existing recordings.
- Installation guidance now describes CPU transcription, explicit storage
  selection, AIO's integrated HaRP setup and recording handoff persistence.
