### Added
- Operator → Storage retention configuration for container recordings, attempt history, current output archives and stage logs. Policies default to keep forever and use UTC calendar days, weeks or months.
- Startup/daily cleanup with restart recovery, video-only expiry that verifies preserved audio, and unavailable-source rerun protection. Only successful publication replaces the current local archive.

### Changed
- The old artifact-retention flag/environment variable is deprecated and ignored. Successful capture/intermediate duplicate cleanup remains unconditional; historical payloads now default to keep forever.
- Search backfill against the local work root requires stopping its operator process to avoid racing archive promotion or expiry. Existing AppAPI installations must receive the new ADMIN retention route through a versioned manifest update.
