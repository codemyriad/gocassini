# shellcheck shell=bash
#
# Shared helpers for the Cassini release version ladder.
#
# The release version lives in appinfo/info.xml as <version>, and the AppAPI
# Docker <image-tag> plus the optional native capture companion version must
# stay equal to it (CI's validate-manifest job rejects a manifest where the
# first two disagree, and a release tag vX.Y.Z whose X.Y.Z doesn't match).
# Every release mutation here rewrites all present copies in lockstep.
#
# Version grammar is stricter than the App Store info.xsd `semver` type on
# purpose: the ladder only recognizes stable X.Y.Z and alpha/beta/rc
# prereleases, so anything else (arbitrary suffixes, build metadata) is a bug
# we want to catch, not ship.
#
# Deliberately sed-based (same idiom as scripts/bump-exapp-version.sh and
# harness/bin/lib-exapp-image.sh): xmllint is not guaranteed to exist in dev
# shells.
#
# Meant to be sourced; defines functions only, no side effects.

# X.Y.Z with an optional -(alpha|beta|rc).N prerelease. Capture groups:
#   1=major 2=minor 3=patch 5=prerelease-type 6=prerelease-number
RV_VERSION_RE='^([0-9]+)\.([0-9]+)\.([0-9]+)(-(alpha|beta|rc)\.([0-9]+))?$'

# rv_validate <version>
# Return 0 when <version> is a well-formed release version, else print why to
# stderr and return 1.
rv_validate() {
  local v="$1"
  if [[ "$v" == *+* ]]; then
    echo "error: build metadata (+...) is not allowed in release versions: '$v'" >&2
    return 1
  fi
  if [[ ! "$v" =~ $RV_VERSION_RE ]]; then
    echo "error: version must be X.Y.Z or X.Y.Z-(alpha|beta|rc).N, got: '$v'" >&2
    return 1
  fi
}

# rv_parse <version>
# Echo "MAJOR MINOR PATCH PRETYPE PRENUM". PRETYPE/PRENUM are empty for a
# stable version. Validates first.
rv_parse() {
  local v="$1"
  rv_validate "$v" || return 1
  [[ "$v" =~ $RV_VERSION_RE ]]  # repopulate BASH_REMATCH
  printf '%s %s %s %s %s\n' \
    "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}" \
    "${BASH_REMATCH[5]}" "${BASH_REMATCH[6]}"
}

# rv_bump <patch|minor|major> <version>
# Echo the next version. Every bump restarts the prerelease train at -alpha.1,
# whether the current version is stable or a prerelease, so the base can be
# re-targeted mid-cycle:
#   patch:  A.B.C[-pre] -> A.B.(C+1)-alpha.1
#   minor:  A.B.C[-pre] -> A.(B+1).0-alpha.1
#   major:  A.B.C[-pre] -> (A+1).0.0-alpha.1
rv_bump() {
  local level="$1" v="$2" parsed major minor patch
  parsed="$(rv_parse "$v")" || return 1
  read -r major minor patch _ _ <<<"$parsed"
  case "$level" in
    patch) patch=$((patch + 1)) ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    major) major=$((major + 1)); minor=0; patch=0 ;;
    *) echo "error: bump level must be patch|minor|major, got: '$level'" >&2; return 1 ;;
  esac
  printf '%s.%s.%s-alpha.1\n' "$major" "$minor" "$patch"
}

# rv_promote <beta|rc.1|rc.2|stable> <version>
# Echo the next version along the Nextcloud prerelease ladder. The base X.Y.Z
# never changes:
#   promote beta   : alpha.N -> beta.1
#   promote rc.1   : beta.N  -> rc.1
#   promote rc.2   : rc.1    -> rc.2
#   promote stable : rc.N    -> X.Y.Z   (drops the suffix)
# Any other source state is rejected as an ambiguous promotion. Stable does not
# require rc.2 first — rc.1 -> X.Y.Z is allowed when a cycle needs only one
# candidate.
rv_promote() {
  local target="$1" v="$2" parsed major minor patch pretype prenum base
  parsed="$(rv_parse "$v")" || return 1
  read -r major minor patch pretype prenum <<<"$parsed"
  base="${major}.${minor}.${patch}"
  case "$target" in
    beta)
      [[ "$pretype" == alpha ]] || { echo "error: 'promote beta' requires an alpha.N version, got: '$v'" >&2; return 1; }
      printf '%s-beta.1\n' "$base" ;;
    rc.1)
      [[ "$pretype" == beta ]] || { echo "error: 'promote rc.1' requires a beta.N version, got: '$v'" >&2; return 1; }
      printf '%s-rc.1\n' "$base" ;;
    rc.2)
      [[ "$pretype" == rc && "$prenum" == 1 ]] || { echo "error: 'promote rc.2' requires an rc.1 version, got: '$v'" >&2; return 1; }
      printf '%s-rc.2\n' "$base" ;;
    stable)
      [[ "$pretype" == rc ]] || { echo "error: 'promote stable' requires an rc.N version, got: '$v'" >&2; return 1; }
      printf '%s\n' "$base" ;;
    *)
      echo "error: promote target must be beta|rc.1|rc.2|stable, got: '$target'" >&2; return 1 ;;
  esac
}

# rv_pretype_rank <alpha|beta|rc>
# Echo the precedence rank of a prerelease type (alpha < beta < rc).
rv_pretype_rank() {
  case "$1" in
    alpha) echo 0 ;;
    beta)  echo 1 ;;
    rc)    echo 2 ;;
    *)     echo 9 ;;
  esac
}

# rv_compare <a> <b>
# Echo -1 if a<b, 0 if equal, 1 if a>b, by semver precedence. A stable version
# outranks the same base prerelease (1.0.0 > 1.0.0-rc.1); among prereleases
# alpha<beta<rc, then the numeric counter.
rv_compare() {
  local a="$1" b="$2" pa pb
  local amaj amin apat atype anum bmaj bmin bpat btype bnum ra rb
  pa="$(rv_parse "$a")" || return 1
  pb="$(rv_parse "$b")" || return 1
  read -r amaj amin apat atype anum <<<"$pa"
  read -r bmaj bmin bpat btype bnum <<<"$pb"
  if (( amaj != bmaj )); then (( amaj > bmaj )) && echo 1 || echo -1; return; fi
  if (( amin != bmin )); then (( amin > bmin )) && echo 1 || echo -1; return; fi
  if (( apat != bpat )); then (( apat > bpat )) && echo 1 || echo -1; return; fi
  # Base X.Y.Z equal — a missing prerelease type ranks highest (it is the
  # final release of that base).
  if [[ -z "$atype" && -z "$btype" ]]; then echo 0; return; fi
  if [[ -z "$atype" ]]; then echo 1; return; fi
  if [[ -z "$btype" ]]; then echo -1; return; fi
  ra="$(rv_pretype_rank "$atype")"; rb="$(rv_pretype_rank "$btype")"
  if (( ra != rb )); then (( ra > rb )) && echo 1 || echo -1; return; fi
  if (( anum != bnum )); then (( anum > bnum )) && echo 1 || echo -1; return; fi
  echo 0
}

# rv_gt <a> <b>: return 0 (true) when a is strictly greater than b.
rv_gt() {
  [[ "$(rv_compare "$1" "$2")" == 1 ]]
}

# rv_read_version <info.xml>
# Echo the <version> pinned in the manifest, or fail with a message.
rv_read_version() {
  local info_xml="$1" v
  v="$(sed -n 's|.*<version>\(.*\)</version>.*|\1|p' "$info_xml" | head -n1)"
  if [[ -z "$v" ]]; then
    echo "error: could not extract <version> from $info_xml" >&2
    return 1
  fi
  printf '%s\n' "$v"
}

# rv_read_image_tag <info.xml>
# Echo the <image-tag> pinned in the manifest, or fail with a message.
rv_read_image_tag() {
  local info_xml="$1" tag
  tag="$(sed -n 's|.*<image-tag>\(.*\)</image-tag>.*|\1|p' "$info_xml" | head -n1)"
  if [[ -z "$tag" ]]; then
    echo "error: could not extract <image-tag> from $info_xml" >&2
    return 1
  fi
  printf '%s\n' "$tag"
}

# rv_write_version <info.xml> <new-version>
# Rewrite <version> and <image-tag> in lockstep and verify each landed exactly
# once. Fails if the manifest's two fields already disagree — that is a broken
# state to fix by hand, not to bump over.
rv_write_version() {
  local info_xml="$1" new="$2" cur_version cur_tag marker count
  rv_validate "$new" || return 1
  cur_version="$(rv_read_version "$info_xml")" || return 1
  cur_tag="$(rv_read_image_tag "$info_xml")" || return 1
  if [[ "$cur_version" != "$cur_tag" ]]; then
    echo "error: <version> ($cur_version) and <image-tag> ($cur_tag) disagree in $info_xml; fix the manifest before bumping" >&2
    return 1
  fi
  sed -i \
    -e "s|<version>${cur_version}</version>|<version>${new}</version>|" \
    -e "s|<image-tag>${cur_tag}</image-tag>|<image-tag>${new}</image-tag>|" \
    "$info_xml"
  for marker in "<version>${new}</version>" "<image-tag>${new}</image-tag>"; do
    count="$(grep -cF "$marker" "$info_xml")"
    if [[ "$count" -ne 1 ]]; then
      echo "error: expected exactly one '$marker' in $info_xml after rewrite, found $count" >&2
      return 1
    fi
  done
}

# rv_write_plain_version <info.xml> <new-version>
# Rewrite the single <version> in a native-app manifest (no Docker image tag).
rv_write_plain_version() {
  local info_xml="$1" new="$2" current count
  rv_validate "$new" || return 1
  current="$(rv_read_version "$info_xml")" || return 1
  sed -i "s|<version>${current}</version>|<version>${new}</version>|" "$info_xml"
  count="$(grep -cF "<version>${new}</version>" "$info_xml")"
  if [[ "$count" -ne 1 ]]; then
    echo "error: expected exactly one <version>${new}</version> in $info_xml, found $count" >&2
    return 1
  fi
}

# rv_write_release_versions <repo-root> <current> <new>
# Keep the ExApp and its capture-delivery companion on one compatibility
# version. Releasing either side alone would leave admins guessing which pair
# can safely be installed.
rv_write_release_versions() {
  local root="$1" current="$2" new="$3"
  local primary="$root/appinfo/info.xml"
  local companion="$root/cassini_capture/appinfo/info.xml"
  if [[ -f "$companion" ]]; then
    local companion_current
    companion_current="$(rv_read_version "$companion")" || return 1
    if [[ "$companion_current" != "$current" ]]; then
      echo "error: cassini_capture is $companion_current while gocassini is $current; reconcile before releasing" >&2
      return 1
    fi
  fi
  rv_write_version "$primary" "$new" || return 1
  if [[ -f "$companion" ]]; then
    rv_write_plain_version "$companion" "$new" || return 1
  fi
}

# rv_info_xml
# Echo the path to appinfo/info.xml at the repo root.
rv_info_xml() {
  printf '%s/appinfo/info.xml\n' "$(git rev-parse --show-toplevel)"
}
