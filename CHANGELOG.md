# Changelog

All notable user-facing changes to Cassini will be documented in this file.

This project follows semantic versioning for Nextcloud App Store releases.
The version in `appinfo/info.xml`, the ExApp Docker image tag, and the release
archive version must match.

## [Unreleased]

Unreleased changes are collected as fragments in `changelog.d/`. During
release preparation, maintainers fold those fragments into this file under the
new version and remove the consumed fragments.

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
