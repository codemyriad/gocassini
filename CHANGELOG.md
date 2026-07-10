# Changelog

All notable user-facing changes to Cassini will be documented in this file.

This project follows semantic versioning for Nextcloud App Store releases.
The version in `appinfo/info.xml`, the ExApp Docker image tag, and the release
archive version must match.

## [Unreleased]

Unreleased changes are collected as fragments in `changelog.d/`. During
release preparation, maintainers fold those fragments into this file under the
new version and remove the consumed fragments.

## [0.2.0-alpha.2] - 2026-07-06

### Added
- The viewer's meeting list has a filter box for narrowing meetings by name or date.

### Fixed
- Talk recordings now show their conversation name and actual recording date in the viewer, instead of the raw job id as both name and date. When the name cannot be resolved, the meeting is shown as "Untitled meeting" with the recording date.
- Meetings whose date could not be read no longer scatter through the list; the list stays newest-first and undated entries sort last.
- Recordings publish to the viewer again. The static-site export crashed on every meeting — a regression that shipped with the D-462 meeting-name/date change (merged in #112) — so no recording reached the viewer. Publishing now completes normally.

## [0.2.0-alpha.1] - 2026-07-03

First public alpha.

### Added
- Added public project metadata: AGPLv3 license, changelog, security policy,
  and contribution notes.

### Security
- Marked deterministic harness credentials as development-only values and
  expanded ignore rules for local dependency and signing artifacts.

## [0.1.0] - 2026-07-01

- Initial Nextcloud AppAPI ExApp packaging for Cassini.
- Add AppAPI manifest metadata, Docker image install metadata, route access
  declarations, and documented production install flow.
- Include admin control panel, viewer, published meeting archive, and Talk
  recording backend routes in the ExApp surface.
