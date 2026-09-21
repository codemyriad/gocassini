### Changed
- Require Nextcloud 33.0.9–35 for new installations; retire Nextcloud 32 support in this release.
- Require successful installed-app compatibility evidence for every advertised Nextcloud major before publishing a release, with exact tested versions and artifacts attached to the release.
- Cache compatibility Go tools, report scenario phase timings, share GPU smoke transcription across assertions, and separate registry maintenance from release qualification.

### Added
- Scheduled upstream compatibility checks and real-browser coverage of the embedded Cassini transcript and recording playback.
