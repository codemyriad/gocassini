#!/usr/bin/env bash
# test-release-tooling.sh — fast, local tests for the release scripts.
#
# Sources scripts/lib-release-version.sh and exercises the pure version
# functions plus the info.xml read/write mechanic against a throwaway fixture.
# No Docker, no network, no App Store credentials.
#
#   ./scripts/test-release-tooling.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib-release-version.sh
source "$SCRIPT_DIR/lib-release-version.sh"

PASS=0
FAIL=0

# ok <description> — record a passing assertion.
ok() { PASS=$((PASS + 1)); printf '  ok   %s\n' "$1"; }
# bad <description> — record a failing assertion.
bad() { FAIL=$((FAIL + 1)); printf '  FAIL %s\n' "$1" >&2; }

# eq <description> <expected> <actual>
eq() {
  if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1 (expected '$2', got '$3')"; fi
}

# accepts <fn> <arg> — assert the function succeeds (exit 0), silencing output.
accepts() {
  if "$1" "$2" >/dev/null 2>&1; then ok "$1 accepts $2"; else bad "$1 should accept $2"; fi
}

# rejects <fn> <arg> — assert the function fails (nonzero), silencing output.
rejects() {
  if "$1" "$2" >/dev/null 2>&1; then bad "$1 should reject $2"; else ok "$1 rejects $2"; fi
}

# transition <fn> <target> <from> <expected> — assert fn <target> <from> == expected.
transition() {
  local got
  if got="$("$1" "$2" "$3" 2>/dev/null)"; then
    eq "$1 $2 $3 -> $4" "$4" "$got"
  else
    bad "$1 $2 $3 should succeed"
  fi
}

# transition_fails <fn> <target> <from> — assert fn <target> <from> is rejected.
transition_fails() {
  if "$1" "$2" "$3" >/dev/null 2>&1; then
    bad "$1 $2 $3 should be rejected"
  else
    ok "$1 $2 $3 rejected"
  fi
}

echo "rv_validate — grammar"
accepts rv_validate 0.2.0
accepts rv_validate 0.2.0-alpha.1
accepts rv_validate 1.20.3-beta.2
accepts rv_validate 1.0.0-rc.2
rejects rv_validate 0.2            # not X.Y.Z
rejects rv_validate v0.2.0         # leading v
rejects rv_validate 0.2.0-alpha    # missing prerelease number
rejects rv_validate 0.2.0-gamma.1  # unknown prerelease type
rejects rv_validate 0.2.0-alpha.1+build   # build metadata
rejects rv_validate 0.2.0+meta            # build metadata

echo "rv_bump — patch/minor/major restart at alpha.1"
transition rv_bump patch 0.2.0 0.2.1-alpha.1
transition rv_bump minor 0.2.0 0.3.0-alpha.1
transition rv_bump major 0.2.0 1.0.0-alpha.1
# From a prerelease the suffix is dropped and the base re-targeted.
transition rv_bump minor 0.2.0-alpha.1 0.3.0-alpha.1
transition rv_bump major 1.4.9-rc.2 2.0.0-alpha.1
transition_fails rv_bump sideways 0.2.0

echo "rv_promote — ladder"
transition rv_promote beta   0.2.0-alpha.1 0.2.0-beta.1
transition rv_promote rc.1   0.2.0-beta.1  0.2.0-rc.1
transition rv_promote rc.2   0.2.0-rc.1    0.2.0-rc.2
transition rv_promote stable 0.2.0-rc.2    0.2.0
# Stable does not require rc.2 first.
transition rv_promote stable 0.2.0-rc.1    0.2.0
# Base version is never changed by a promotion (implicit in the outputs above).
transition_fails rv_promote beta   0.2.0        # stable has nothing to promote
transition_fails rv_promote rc.1   0.2.0-alpha.1 # must be beta first
transition_fails rv_promote stable 0.2.0-beta.1  # must reach rc first
transition_fails rv_promote rc.2   0.2.0-rc.2    # already at rc.2
transition_fails rv_promote nope   0.2.0-rc.1    # unknown target

echo "rv_compare — semver precedence"
eq "0.1.0 < 0.2.0"                 "-1" "$(rv_compare 0.1.0 0.2.0)"
eq "0.2.0 > 0.1.0"                 "1"  "$(rv_compare 0.2.0 0.1.0)"
eq "0.2.0 == 0.2.0"                "0"  "$(rv_compare 0.2.0 0.2.0)"
eq "0.2.0 > 0.2.0-rc.2 (stable wins)" "1"  "$(rv_compare 0.2.0 0.2.0-rc.2)"
eq "0.2.0-alpha.1 < 0.2.0-beta.1"  "-1" "$(rv_compare 0.2.0-alpha.1 0.2.0-beta.1)"
eq "0.2.0-beta.1 < 0.2.0-rc.1"     "-1" "$(rv_compare 0.2.0-beta.1 0.2.0-rc.1)"
eq "0.2.0-rc.1 < 0.2.0-rc.2"       "-1" "$(rv_compare 0.2.0-rc.1 0.2.0-rc.2)"
eq "1.0.0 > 0.9.9"                 "1"  "$(rv_compare 1.0.0 0.9.9)"
if rv_gt 0.3.0-alpha.1 0.2.0-alpha.1; then ok "rv_gt true for 0.3.0-alpha.1 > 0.2.0-alpha.1"; else bad "rv_gt should be true"; fi
if rv_gt 0.2.0-alpha.1 0.2.0; then bad "rv_gt should be false for prerelease < stable"; else ok "rv_gt false for 0.2.0-alpha.1 > 0.2.0"; fi

echo "rv_read_version / rv_write_version — info.xml IO"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
FIXTURE="$TMP/info.xml"
cat >"$FIXTURE" <<'EOF'
<?xml version="1.0"?>
<info>
  <version>0.2.0</version>
  <external-app>
    <docker-install>
      <image-tag>0.2.0</image-tag>
    </docker-install>
  </external-app>
</info>
EOF

eq "rv_read_version reads pinned version" "0.2.0" "$(rv_read_version "$FIXTURE")"
rv_write_version "$FIXTURE" "0.3.0-alpha.1"
eq "rv_write_version updates <version>"   "0.3.0-alpha.1" "$(rv_read_version "$FIXTURE")"
eq "rv_write_version updates <image-tag>" "0.3.0-alpha.1" "$(rv_read_image_tag "$FIXTURE")"

# A manifest whose two fields disagree must be fixed by hand, not bumped over.
DISAGREE="$TMP/disagree.xml"
cat >"$DISAGREE" <<'EOF'
<?xml version="1.0"?>
<info>
  <version>0.2.0</version>
  <external-app><docker-install><image-tag>0.1.0</image-tag></docker-install></external-app>
</info>
EOF
if rv_write_version "$DISAGREE" "0.3.0-alpha.1" >/dev/null 2>&1; then
  bad "rv_write_version should refuse a manifest with mismatched version/image-tag"
else
  ok "rv_write_version refuses mismatched version/image-tag"
fi

echo "fold-changelog.sh — fragment folding"
# Build a throwaway repo so fold-changelog's `git rev-parse --show-toplevel`
# resolves to our fixture rather than the real tree.
REPO="$(mktemp -d)"
git -C "$REPO" init -q
mkdir -p "$REPO/changelog.d"
cat >"$REPO/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

Fragments live in changelog.d/.

## [0.1.0] - 2026-07-01

- Initial release.
EOF
cat >"$REPO/changelog.d/README.md" <<'EOF'
# Changelog Fragments
Not a fragment; must be ignored by the folder.
EOF
cat >"$REPO/changelog.d/10.first.md" <<'EOF'
### Added
- Added a viewer search box.

### Fixed
- Fixed a crash on empty transcripts.
EOF
cat >"$REPO/changelog.d/20.second.md" <<'EOF'
### Added
- Added CUDA image aliases.

### Security
- Rotated the harness signing key.
EOF

fold() { ( cd "$REPO" && "$SCRIPT_DIR/fold-changelog.sh" "$@" ); }

# --check must validate without touching anything.
before="$(cat "$REPO/CHANGELOG.md")"
if fold --version 0.2.0-alpha.1 --date 2026-07-06 --check >/dev/null 2>&1; then
  ok "fold --check succeeds on valid fragments"
else
  bad "fold --check should succeed on valid fragments"
fi
eq "fold --check leaves CHANGELOG.md untouched" "$before" "$(cat "$REPO/CHANGELOG.md")"
if [[ -f "$REPO/changelog.d/10.first.md" && -f "$REPO/changelog.d/20.second.md" ]]; then
  ok "fold --check leaves fragments in place"
else
  bad "fold --check must not delete fragments"
fi

# --write splices the section and consumes fragments.
fold --version 0.2.0-alpha.1 --date 2026-07-06 --write >/dev/null
NEW="$REPO/CHANGELOG.md"
if grep -qF "## [0.2.0-alpha.1] - 2026-07-06" "$NEW"; then
  ok "fold --write inserts the version section"
else
  bad "fold --write should insert the version section"
fi
# Canonical heading order: Added, then Fixed (from frag 10), then Security
# (from frag 20) — merged across fragments in Keep a Changelog order.
order="$(grep -E '^### ' "$NEW" | head -3 | tr '\n' ',')"
eq "fold --write orders headings canonically" "### Added,### Fixed,### Security," "$order"
if grep -qF -- "- Added a viewer search box." "$NEW" && grep -qF -- "- Added CUDA image aliases." "$NEW"; then
  ok "fold --write merges + preserves bullet text across fragments"
else
  bad "fold --write should preserve bullet text from every fragment"
fi
# New section goes above the prior release, below Unreleased.
if awk '/^## \[Unreleased\]/{u=NR} /^## \[0.2.0-alpha.1\]/{n=NR} /^## \[0.1.0\]/{o=NR} END{exit !(u<n && n<o)}' "$NEW"; then
  ok "fold --write places section after Unreleased, before prior release"
else
  bad "fold --write mis-ordered the new section"
fi
if [[ ! -f "$REPO/changelog.d/10.first.md" && ! -f "$REPO/changelog.d/20.second.md" ]]; then
  ok "fold --write removes consumed fragments"
else
  bad "fold --write should remove consumed fragments"
fi
if [[ -f "$REPO/changelog.d/README.md" ]]; then
  ok "fold --write keeps README.md"
else
  bad "fold --write must not delete README.md"
fi

# Malformed fragments are rejected before any write.
cat >"$REPO/changelog.d/30.bad-heading.md" <<'EOF'
### Improved
- Not a Keep a Changelog heading.
EOF
if fold --version 0.2.1-alpha.1 --check >/dev/null 2>&1; then
  bad "fold should reject an unknown heading"
else
  ok "fold rejects unknown heading"
fi
rm -f "$REPO/changelog.d/30.bad-heading.md"

cat >"$REPO/changelog.d/31.prose.md" <<'EOF'
This line is prose with no heading above it.

### Added
- A real entry.
EOF
if fold --version 0.2.1-alpha.1 --check >/dev/null 2>&1; then
  bad "fold should reject prose outside a heading"
else
  ok "fold rejects prose outside a heading"
fi
rm -f "$REPO/changelog.d/31.prose.md"

rm -rf "$REPO"

echo "prepare-release.sh — end-to-end (local commit + tag)"
PR="$(mktemp -d)"
git -C "$PR" init -q
git -C "$PR" config user.email t@t
git -C "$PR" config user.name t
mkdir -p "$PR/scripts" "$PR/appinfo" "$PR/changelog.d"
cp "$SCRIPT_DIR"/{lib-release-version.sh,release-version.sh,fold-changelog.sh,extract-release-notes.sh,prepare-release.sh} "$PR/scripts/"
cat >"$PR/appinfo/info.xml" <<'EOF'
<?xml version="1.0"?>
<info>
  <version>0.2.0</version>
  <external-app><docker-install><image-tag>0.2.0</image-tag></docker-install></external-app>
</info>
EOF
cat >"$PR/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

Fragments live in changelog.d/.

## [0.2.0] - 2026-07-01

- Initial.
EOF
cat >"$PR/changelog.d/README.md" <<'EOF'
# Fragments
EOF
cat >"$PR/changelog.d/50.feature.md" <<'EOF'
### Added
- Added a release-prep command.
EOF
git -C "$PR" add -A
git -C "$PR" commit -q -m init
git -C "$PR" tag v0.2.0

prep() { ( cd "$PR" && "$PR/scripts/prepare-release.sh" "$@" ); }

# Dirty worktree is rejected.
echo "dirt" >>"$PR/appinfo/info.xml"
if prep --bump minor >/dev/null 2>&1; then bad "prepare-release should reject a dirty worktree"; else ok "prepare-release rejects a dirty worktree"; fi
git -C "$PR" checkout -q -- appinfo/info.xml

# Happy path: bump minor 0.2.0 -> 0.3.0-alpha.1.
if prep --bump minor >/dev/null 2>&1; then ok "prepare-release --bump minor succeeds"; else bad "prepare-release --bump minor should succeed"; fi
eq "info.xml bumped to next alpha" "0.3.0-alpha.1" "$(rv_read_version "$PR/appinfo/info.xml")"
if grep -qF "## [0.3.0-alpha.1]" "$PR/CHANGELOG.md"; then ok "CHANGELOG got the new section"; else bad "CHANGELOG should get the new section"; fi
if [[ ! -f "$PR/changelog.d/50.feature.md" ]]; then ok "fragment consumed"; else bad "fragment should be consumed"; fi
eq "commit message" "release: 0.3.0-alpha.1" "$(git -C "$PR" log -1 --pretty=%s)"
if git -C "$PR" rev-parse -q --verify refs/tags/v0.3.0-alpha.1 >/dev/null; then ok "tag v0.3.0-alpha.1 created"; else bad "tag should be created"; fi
if [[ -f "$PR/build/release/gocassini-0.3.0-alpha.1-notes.md" ]] && grep -qF -- "- Added a release-prep command." "$PR/build/release/gocassini-0.3.0-alpha.1-notes.md"; then ok "release notes file written with entries"; else bad "release notes file should be written"; fi
# Nothing pushed: no remote configured, and the working tree is clean again.
if [[ -z "$(git -C "$PR" remote)" ]]; then ok "prepare-release added no remote / did not push"; else bad "prepare-release must not push"; fi
if git -C "$PR" diff --quiet && git -C "$PR" diff --cached --quiet; then ok "worktree clean after prepare-release"; else bad "worktree should be clean after commit"; fi

# Re-running for a non-greater version is rejected (tag collision / ordering).
if prep --version 0.2.0 >/dev/null 2>&1; then bad "prepare-release should reject a non-greater version"; else ok "prepare-release rejects non-greater version"; fi

# --push pushes the release commit + tag to origin (0.3.0-alpha.1 -> beta.1).
BARE="$(mktemp -d)/origin.git"; git init -q --bare "$BARE"
git -C "$PR" remote add origin "$BARE"
git -C "$PR" push -q origin HEAD
( cd "$PR" && ./scripts/prepare-release.sh --promote beta --allow-empty-changelog --push ) >/dev/null 2>&1
if git --git-dir="$BARE" tag --list | grep -qx v0.3.0-beta.1; then
  ok "prepare-release --push pushes the release tag to origin"
else
  bad "prepare-release --push should push the release tag to origin"
fi
rm -rf "$BARE"

rm -rf "$PR"

echo "build/validate-appstore-tarball.sh — packaging"
# Build against the real repo's source files, writing only into a temp dir.
SRCV="$(rv_read_version "$(git rev-parse --show-toplevel)/appinfo/info.xml")"
BD="$(mktemp -d)"
TARBALL="$BD/gocassini.tar.gz"
if "$SCRIPT_DIR/build-appstore-tarball.sh" --version "$SRCV" --staging "$BD/appstore" --output "$TARBALL" >/dev/null 2>&1; then
  ok "build produces a tarball"
else
  bad "build should produce a tarball"
fi

# Package contains exactly the allowlisted files (directories filtered out).
files="$(tar -tzf "$TARBALL" | grep -v '/$' | LC_ALL=C sort | tr '\n' ',')"
expected="gocassini/CHANGELOG.md,gocassini/LICENSE,gocassini/appinfo/app.php,gocassini/appinfo/info.xml,gocassini/img/app.svg,"
eq "package contains only expected files" "$expected" "$files"
if tar -tzf "$TARBALL" | grep -q 'gocassini/README.md'; then bad "README.md must not be packaged"; else ok "README.md excluded from package"; fi

# Every entry sits under the gocassini/ root.
if tar -tzf "$TARBALL" | grep -qvE '^gocassini(/|$)'; then bad "archive has entries outside gocassini/"; else ok "archive root is gocassini/"; fi

# Validator passes on the freshly built (unsigned) tarball.
if "$SCRIPT_DIR/validate-appstore-tarball.sh" --version "$SRCV" --tarball "$TARBALL" >/dev/null 2>&1; then
  ok "validate passes on a good unsigned tarball"
else
  bad "validate should pass on a good unsigned tarball"
fi

# Version mismatch is rejected.
if "$SCRIPT_DIR/validate-appstore-tarball.sh" --version 9.9.9 --tarball "$TARBALL" >/dev/null 2>&1; then
  bad "validate should reject a version mismatch"
else
  ok "validate rejects a version mismatch"
fi

# Signed mode fails without signature.json, passes once it is present.
if "$SCRIPT_DIR/validate-appstore-tarball.sh" --version "$SRCV" --tarball "$TARBALL" --signed >/dev/null 2>&1; then
  bad "validate --signed should fail without signature.json"
else
  ok "validate --signed rejects an unsigned tarball"
fi
printf '{}' >"$BD/appstore/gocassini/appinfo/signature.json"
"$SCRIPT_DIR/build-appstore-tarball.sh" --version "$SRCV" --staging "$BD/appstore" --output "$TARBALL" --archive-only >/dev/null 2>&1
if "$SCRIPT_DIR/validate-appstore-tarball.sh" --version "$SRCV" --tarball "$TARBALL" --signed >/dev/null 2>&1; then
  ok "validate --signed passes with signature.json present"
else
  bad "validate --signed should pass once signature.json is present"
fi

# Forbidden signing material is caught.
cp -r "$BD/appstore/gocassini" "$BD/leak"
printf 'PRIVATE' >"$BD/leak/appinfo/app.key"
tar -czf "$BD/leak.tar.gz" -C "$BD" leak 2>/dev/null
# Re-root as gocassini/ so only the key (not the root) is what fails validation.
rm -rf "$BD/leakroot"; mkdir -p "$BD/leakroot"; cp -r "$BD/leak" "$BD/leakroot/gocassini"
tar -czf "$BD/leak.tar.gz" -C "$BD/leakroot" gocassini
if "$SCRIPT_DIR/validate-appstore-tarball.sh" --version "$SRCV" --tarball "$BD/leak.tar.gz" >/dev/null 2>&1; then
  bad "validate should reject an archive containing a .key file"
else
  ok "validate rejects forbidden signing material (.key)"
fi

# Wrong archive root is caught.
mkdir -p "$BD/wrong/notgocassini"; printf 'x' >"$BD/wrong/notgocassini/f"
tar -czf "$BD/wrong.tar.gz" -C "$BD/wrong" notgocassini
if "$SCRIPT_DIR/validate-appstore-tarball.sh" --version "$SRCV" --tarball "$BD/wrong.tar.gz" >/dev/null 2>&1; then
  bad "validate should reject a wrong archive root"
else
  ok "validate rejects a non-gocassini archive root"
fi

# Store-schema validation (pre-info.xslt -> info.xsd). Needs xsltproc + xmllint;
# skip cleanly where they are absent (CI installs them so this always runs there).
if command -v xsltproc >/dev/null 2>&1 && command -v xmllint >/dev/null 2>&1; then
  # The good tarball (built from the real manifest) must pass the store schema.
  if "$SCRIPT_DIR/validate-appstore-tarball.sh" --version "$SRCV" --tarball "$TARBALL" >/dev/null 2>&1; then
    ok "real info.xml passes store-schema validation (pre-info.xslt -> info.xsd)"
  else
    bad "real info.xml should pass store-schema validation"
  fi
  # An invalid <category> (a constrained enum) must be caught — and only the
  # store check catches it, since our other checks ignore <category>.
  mkdir -p "$BD/badcat"; cp -r "$BD/appstore/gocassini" "$BD/badcat/gocassini"
  sed -i 's#<category>[^<]*</category>#<category>not-a-real-category</category>#' \
    "$BD/badcat/gocassini/appinfo/info.xml"
  tar -czf "$BD/badcat.tar.gz" -C "$BD/badcat" gocassini
  if "$SCRIPT_DIR/validate-appstore-tarball.sh" --version "$SRCV" --tarball "$BD/badcat.tar.gz" >/dev/null 2>&1; then
    bad "validate should reject a store-schema-invalid <category>"
  else
    ok "validate rejects a store-schema-invalid manifest (bad <category>)"
  fi
else
  echo "  skip: xsltproc/xmllint not installed — store-schema tests run in CI"
fi

echo "build-appstore-tarball.sh — --sign-app with an injectable OCC"
# Stub occ: parse --path and drop a signature.json, so signing is exercised
# without a real Nextcloud. Proves the script wires OCC + key/cert + path and
# verifies the result.
STUB="$BD/occ-stub.sh"
cat >"$STUB" <<'STUBEOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == "integrity:sign-app" ]] || { echo "stub: unexpected occ subcommand: ${1:-}" >&2; exit 3; }
path=""; for a in "$@"; do case "$a" in --path=*) path="${a#--path=}";; esac; done
[[ -n "$path" && -d "$path/appinfo" ]] || { echo "stub: bad --path '$path'" >&2; exit 3; }
printf '{"hashes":{},"signature":"stub","certificate":"stub"}' > "$path/appinfo/signature.json"
STUBEOF
chmod +x "$STUB"

if OCC="$STUB" APP_PRIVATE_KEY="KEY" APP_PUBLIC_CRT="CRT" \
   "$SCRIPT_DIR/build-appstore-tarball.sh" --version "$SRCV" \
     --staging "$BD/signed" --output "$BD/signed.tar.gz" --sign-app >/dev/null 2>&1; then
  ok "--sign-app runs OCC and produces a signed tarball"
else
  bad "--sign-app should run OCC and produce a signed tarball"
fi
if tar -tzf "$BD/signed.tar.gz" 2>/dev/null | grep -qxF gocassini/appinfo/signature.json; then
  ok "signed tarball contains appinfo/signature.json"
else
  bad "signed tarball should contain appinfo/signature.json"
fi
# --sign-app must fail loudly when OCC is absent — never silently ship unsigned.
if APP_PRIVATE_KEY="KEY" APP_PUBLIC_CRT="CRT" \
   "$SCRIPT_DIR/build-appstore-tarball.sh" --version "$SRCV" \
     --staging "$BD/nosign" --output "$BD/nosign.tar.gz" --sign-app >/dev/null 2>&1; then
  bad "--sign-app without OCC should fail"
else
  ok "--sign-app without OCC fails loudly"
fi
# --sign-app + --archive-only is contradictory and rejected.
if "$SCRIPT_DIR/build-appstore-tarball.sh" --version "$SRCV" --sign-app --archive-only >/dev/null 2>&1; then
  bad "--sign-app --archive-only should be rejected"
else
  ok "--sign-app --archive-only rejected"
fi

rm -rf "$BD"

echo "fold-changelog.sh --preview / release-preview.sh — decide the next move"
PV="$(mktemp -d)"
git -C "$PV" init -q
git -C "$PV" config user.email t@t
git -C "$PV" config user.name t
mkdir -p "$PV/scripts" "$PV/appinfo" "$PV/changelog.d"
cp "$SCRIPT_DIR"/{lib-release-version.sh,fold-changelog.sh,release-preview.sh} "$PV/scripts/"
printf '# Changelog\n\n## [Unreleased]\n' > "$PV/CHANGELOG.md"
printf '# Fragments\n' > "$PV/changelog.d/README.md"
prev() { ( cd "$PV" && "$PV/scripts/release-preview.sh" ); }
foldprev() { ( cd "$PV" && "$PV/scripts/fold-changelog.sh" --preview ); }

# --preview with no fragments says so and does not error.
printf '<?xml version="1.0"?>\n<info><version>0.2.0</version><external-app><docker-install><image-tag>0.2.0</image-tag></docker-install></external-app></info>\n' > "$PV/appinfo/info.xml"
git -C "$PV" add -A; git -C "$PV" commit -qm init
if foldprev | grep -qF "no pending changelog"; then ok "fold --preview reports an empty changelog.d"; else bad "fold --preview should report empty"; fi

# --preview groups pending fragments with no version needed.
printf '### Added\n- Thing one.\n\n### Fixed\n- Thing two.\n' > "$PV/changelog.d/10.mix.md"
out="$(foldprev)"
if grep -qF "### Added" <<<"$out" && grep -qF -- "- Thing one." <<<"$out" && grep -qF "### Fixed" <<<"$out"; then
  ok "fold --preview groups pending fragments (no version)"
else
  bad "fold --preview should group pending fragments"
fi

# release-preview on a STABLE version shows patch/minor/major candidates.
sp="$(prev)"
if grep -qF "0.2.1-alpha.1" <<<"$sp" && grep -qF "0.3.0-alpha.1" <<<"$sp" && grep -qF "1.0.0-alpha.1" <<<"$sp"; then
  ok "release-preview shows patch/minor/major candidates on a stable version"
else
  bad "release-preview should show bump candidates on a stable version"
fi
if grep -qF -- "- Thing one." <<<"$sp"; then ok "release-preview shows the pending changes"; else bad "release-preview should show pending changes"; fi

# release-preview on a PRERELEASE shows the promote move + explicit-jump hint.
printf '<?xml version="1.0"?>\n<info><version>0.3.0-beta.1</version><external-app><docker-install><image-tag>0.3.0-beta.1</image-tag></docker-install></external-app></info>\n' > "$PV/appinfo/info.xml"
git -C "$PV" commit -qam bump
pp="$(prev)"
if grep -qF "promote rc.1" <<<"$pp" && grep -qF "0.3.0-rc.1" <<<"$pp" && grep -qF -- "--version 0.3.0" <<<"$pp"; then
  ok "release-preview shows promote + explicit-jump options on a prerelease"
else
  bad "release-preview should show promote/jump options on a prerelease"
fi
rm -rf "$PV"

echo "prepare-release.sh — guided (interactive) mode"
GD="$(mktemp -d)"
git -C "$GD" init -q
git -C "$GD" config user.email t@t
git -C "$GD" config user.name t
mkdir -p "$GD/scripts" "$GD/appinfo" "$GD/changelog.d"
cp "$SCRIPT_DIR"/{lib-release-version.sh,release-version.sh,fold-changelog.sh,extract-release-notes.sh,prepare-release.sh,release-preview.sh} "$GD/scripts/"
printf '<?xml version="1.0"?>\n<info><version>0.2.0</version><external-app><docker-install><image-tag>0.2.0</image-tag></docker-install></external-app></info>\n' > "$GD/appinfo/info.xml"
printf '# Changelog\n\n## [Unreleased]\n' > "$GD/CHANGELOG.md"
printf '# Fragments\n' > "$GD/changelog.d/README.md"
printf '### Added\n- guided feature.\n' > "$GD/changelog.d/10.f.md"
git -C "$GD" add -A; git -C "$GD" commit -qm init
# guided <answers...> — pipe answers into interactive prepare-release.
guided() { ( cd "$GD" && printf '%s\n' "$@" | "$GD/scripts/prepare-release.sh" >/dev/null 2>&1 ); }

guided q
if [[ "$(rv_read_version "$GD/appinfo/info.xml")" == "0.2.0" && -z "$(git -C "$GD" tag --list)" ]]; then
  ok "guided: 'q' aborts, nothing changed"
else
  bad "guided 'q' should change nothing"
fi

guided minor n
if [[ "$(rv_read_version "$GD/appinfo/info.xml")" == "0.2.0" ]]; then
  ok "guided: declining the confirm changes nothing"
else
  bad "guided decline should change nothing"
fi

guided minor y
if [[ "$(rv_read_version "$GD/appinfo/info.xml")" == "0.3.0-alpha.1" ]] \
   && git -C "$GD" rev-parse -q --verify refs/tags/v0.3.0-alpha.1 >/dev/null; then
  ok "guided: choosing minor + confirm cuts v0.3.0-alpha.1"
else
  bad "guided minor + confirm should cut the release"
fi
rm -rf "$GD"

echo
echo "passed: $PASS  failed: $FAIL"
[[ "$FAIL" -eq 0 ]]
