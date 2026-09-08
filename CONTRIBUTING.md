# Contributing

Cassini is developed as a Nextcloud AppAPI ExApp. If your change affects the
ExApp's declared surface — routes, environment variables, or other
`appinfo/info.xml` metadata — update the manifest to match the code in the same
change. Release packaging, version/image-tag bumps, and App Store publishing are
maintainer tasks handled at release time (see [Releases](#releases)).

## Development

- Use `./bin/cassini` as the repo entry point.
- Use `docs/exapp-install.md` for production ExApp behavior.
- Use the harness under `harness/` and `sandbox/` only for local validation.

## Secrets

Do not commit private keys, certificates, CSRs, tokens, `.env` files, app-store
credentials, real meeting recordings, or deployment-specific configuration.

The checked-in harness credentials are deterministic local test values. Treat
them as public and unsuitable for any real deployment.

## Pull Requests

- Keep changes scoped to one behavioral or release-readiness concern.
- Include tests or a clear verification note when changing runtime behavior.
- Add a `changelog.d/` fragment for user-facing changes, install/release
  behavior, app-store metadata, security posture, or operator documentation.
  Do not edit `CHANGELOG.md` directly except during release preparation.

## PR conflict warnings

The PR conflict impact workflow compares each open PR against the other open PRs
targeting its base branch. It comments only when merging the candidate would make
a currently mergeable PR conflict. Pre-existing conflicts are omitted. The bot
updates one comment in place and deletes it when the warning clears.

The simulation assumes **merge commits**. Squash and rebase merges can produce
different results, especially with stacked PRs. Warnings are advisory and cover
Git merge conflicts, not build or test compatibility. PR updates, branch pushes,
and an hourly refresh keep the comments current.

## Releases

Maintainers cut releases with `scripts/prepare-release.sh` and the **Release**
workflow. See [`docs/release.md`](docs/release.md) for the version ladder, the
local release-prep flow, and the App Store publish path.
