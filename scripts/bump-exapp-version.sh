#!/usr/bin/env bash
# bump-exapp-version.sh — compatibility wrapper around release-version.sh.
#
# This script used to own the <version> + <image-tag> rewrite directly. That
# logic now lives in scripts/release-version.sh (`set <version>`), which also
# adds the bump/promote subcommands for the Nextcloud release ladder. The
# wrapper is kept because docs/exapp-install.md, appinfo/info.xml comments and
# publish-exapp-image.yml error messages still point maintainers here.
#
# Prefer the richer CLI for new work:
#   ./scripts/release-version.sh bump patch|minor|major
#   ./scripts/release-version.sh promote beta|rc.1|rc.2|stable
#
# Usage (unchanged):
#   ./scripts/bump-exapp-version.sh <new-version>   # e.g. 0.2.0

set -euo pipefail

NEW_VERSION="${1:?usage: $0 <new-version> (e.g. 0.2.0)}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$SCRIPT_DIR/release-version.sh" set "$NEW_VERSION"
