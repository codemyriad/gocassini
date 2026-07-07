# Contributing

Cassini is developed as a Nextcloud AppAPI ExApp. Contributions should keep
runtime behavior, release packaging, and app-store metadata aligned.

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

## Releases

Maintainers cut releases with `scripts/prepare-release.sh` and the **Release**
workflow. See [`docs/release.md`](docs/release.md) for the version ladder, the
local release-prep flow, and the App Store publish path.
