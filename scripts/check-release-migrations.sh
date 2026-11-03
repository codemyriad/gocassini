#!/usr/bin/env bash
# Refuse a change to a release branch that adds or edits an operator migration.
#
# Why this exists (D-901): operator migrations are numbered contiguously, and a
# number means one migration everywhere. A release/X.Y branch stops at the
# number main had when it was cut, so a migration from a later main has no
# number it can take there: keeping main's leaves a gap, which the operator
# refuses to load, and taking the next free one gives that number to a
# different migration than main's, so an installation on the patch cannot
# upgrade to the next minor. Schema changes therefore ship in the next minor,
# never in a patch (docs/release.md#branches).
#
# The backport workflow cherry-picks whatever a labelled PR contains, so this
# runs on every change to a release branch rather than relying on whoever adds
# the label to remember.
#
# Usage: check-release-migrations.sh <base-ref> [<head-ref>]
set -euo pipefail

MIGRATIONS_DIR="cassini-operator/internal/operator/migrations"

base="${1:?usage: $0 <base-ref> [<head-ref>]}"
head="${2:-HEAD}"

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# The README documents how migrations work; editing it changes no schema.
mapfile -t changed < <(git diff --name-only "$base...$head" -- "$MIGRATIONS_DIR" | grep -v "^$MIGRATIONS_DIR/README.md$" || true)

if [[ "${#changed[@]}" -eq 0 ]]; then
  echo "no operator migrations changed"
  exit 0
fi

msg="release branches take no operator migrations: a schema change ships in the next minor (docs/release.md#branches)"
if [[ "${GITHUB_ACTIONS:-}" == true ]]; then echo "::error::$msg" >&2; else echo "error: $msg" >&2; fi
printf '  %s\n' "${changed[@]}" >&2
exit 1
