# Changelog Fragments

Cassini uses changelog fragments to keep agent-heavy pull requests from
conflicting on `CHANGELOG.md`.

Add one fragment for every pull request that changes user-visible behavior,
installation or release behavior, app-store metadata, security posture, or
operator documentation. Skip fragments for pure refactors, tests-only changes,
typo fixes, and internal planning notes.

## File Names

Use a short, stable name:

```text
changelog.d/<issue-or-pr>.<slug>.md
```

Examples:

```text
changelog.d/109.public-release.md
changelog.d/d-398.signing-certificate.md
```

## Format

Use Keep a Changelog-style headings. Include only headings that have entries.

```markdown
### Added
- Added a public security policy.

### Changed
- Clarified the ExApp installation flow.

### Fixed
- Fixed AppAPI route metadata for published recordings.

### Security
- Marked local harness credentials as development-only values.
```

Good fragments are short and user/operator-facing. Do not list every file
changed, implementation step, test helper, or agent action.

## Release Process

During release preparation, maintainers move the relevant fragments into
`CHANGELOG.md` under the new version, edit them into a coherent release note,
and delete the consumed fragment files.
