#!/usr/bin/env bash
# Offline, no-Docker contract test for the two native Nextcloud prerequisites.
#
# In the ACCESS-CONTROLLED storage model, recordings live in a Team folder whose
# broad read mount is the virtual `everyone` group, so the substrate needs two
# PHP apps an ExApp cannot install for itself: `groupfolders` and
# `group_everyone`. A missing `everyone` group makes the provisioner return
# BEFORE the Team folder is created (PR #171), which is a silent no-op rather
# than a visible error — hence a contract test rather than a runtime check.
#
# What changed in D-616: those apps are prerequisites of a MODE, not of Cassini.
# The default model needs neither. So this file can no longer assert that
# bootstrap installs them unconditionally — it asserts that bootstrap installs
# them on the access-controlled path and is ABLE to skip them otherwise, which
# is the property the storage-mode dimension added.
#
# The bootstrap assertion here is the one real check rescued from
# test-exapp-access-control.sh, which was deleted with the
# CASSINI_NC_ACCESS_CONTROL flag it existed to pin.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BOOTSTRAP="$SCRIPT_DIR/bootstrap.sh"
SANDBOX_WIRE="$ROOT/sandbox/wire-cassini.sh"
MANIFEST="$ROOT/appinfo/info.xml"

fail() { echo "FAIL: $*" >&2; exit 1; }

# 1. The harness must still be able to provide both apps before the enabled edge
#    fires — that is what every access-controlled e2e assertion rests on.
for app in groupfolders group_everyone; do
  grep -qF "app:install $app" "$BOOTSTRAP" \
    || fail "bootstrap does not install required Nextcloud app $app"
  grep -qF "app:enable $app" "$BOOTSTRAP" \
    || fail "bootstrap does not enable required Nextcloud app $app"
done

# 1b. ...and it must be able NOT to. A stack asked for the default model, or for
#     no storage scaffold at all, must not be failed for their absence — that is
#     the state a production Nextcloud is in before anybody sets Cassini up, and
#     being able to stand it up is the point of the dimension.
#
#     Asserted on the predicates rather than on the surrounding shell, because
#     the shape of the conditional is not the contract; what is required is that
#     the required-app check consults them at all.
grep -q 'harness_storage_mode_is_acl' "$BOOTSTRAP" \
  || fail "bootstrap requires the prerequisite apps unconditionally; --storage-mode default must not be failed for their absence"
grep -q 'harness_skip_storage_scaffold' "$BOOTSTRAP" \
  || fail "bootstrap does not honour --debug-skip-storage-scaffold"
grep -q 'local -a required_apps=(spreed)' "$BOOTSTRAP" \
  || fail "the hard-required app list is no longer just spreed; the other two are one mode's prerequisites"

# 2. The sandbox is a dogfood instance people keep real recordings on, so its
#    enable is HARD — no `|| true`. bootstrap.sh only tries; a throwaway test
#    stack can survive a miss, a dogfood box cannot. Since the AIO rewrite
#    (D-515) the sandbox is wired by wire-cassini.sh, not deploy.sh.
#
#    The dogfood box runs the ACCESS-CONTROLLED model and says so explicitly
#    (see 2b). Switching it to the default model would move its archive and make
#    every recording readable by every account on the instance, so the mode is
#    declared rather than left to a fallback.
for app in groupfolders group_everyone; do
  # ${app} braced: bare "$app[" reads as an array subscript to shellcheck (SC1087),
  # and the bracket here opens a POSIX character class, not an index.
  grep -qE "^[[:space:]]*occ app:enable ${app}[[:space:]]*\$" "$SANDBOX_WIRE" \
    || fail "sandbox/wire-cassini.sh does not hard-enable required Nextcloud app $app"
done

# 3. The flag is gone; nothing may declare it again.
grep -qF 'CASSINI_NC_ACCESS_CONTROL' "$MANIFEST" \
  && fail "appinfo/info.xml still declares the retired CASSINI_NC_ACCESS_CONTROL variable"

INSTALL_DOC="$ROOT/docs/exapp-install.md"

# 4. Both apps are named inside the Prerequisites SECTION, not merely somewhere
#    in the file. An installer reads that section and stops; a mention buried in
#    a variable description is how this went undocumented in the first place
#    (D-585 outcome 4).
prereq_block="$(awk '/^## Prerequisites$/{f=1;next} /^## /{f=0} f' "$INSTALL_DOC")"
[[ -n "$prereq_block" ]] || fail "docs/exapp-install.md has no Prerequisites section"
for app in groupfolders group_everyone; do
  grep -qF "$app" <<<"$prereq_block" \
    || fail "docs/exapp-install.md Prerequisites does not name required app $app"
done

# 5. MANIFEST <-> DOCS PARITY. The deploy-options table was missing two of the
#    declared variables, and it was missing them because nothing checked.
mapfile -t manifest_vars < <(python3 - "$MANIFEST" <<'PY'
import sys, xml.etree.ElementTree as ET
root = ET.parse(sys.argv[1]).getroot()
for v in root.findall('./external-app/environment-variables/variable'):
    print(v.find('name').text)
PY
)
[[ ${#manifest_vars[@]} -gt 0 ]] || fail "appinfo/info.xml declares no environment variables"
mapfile -t doc_vars < <(python3 - "$INSTALL_DOC" <<'PY'
import re, sys
doc = open(sys.argv[1]).read()
# The deploy-options table: rows whose first cell is a backticked variable name.
block = doc.split('### App configuration (`--env`)', 1)[-1].split('### Updating deploy options', 1)[0]
for line in block.splitlines():
    m = re.match(r'\|\s*`([A-Z][A-Z0-9_]*)`\s*\|', line)
    if m:
        print(m.group(1))
PY
)
missing_from_docs="$(comm -23 <(printf '%s\n' "${manifest_vars[@]}" | sort) <(printf '%s\n' "${doc_vars[@]}" | sort))"
missing_from_manifest="$(comm -13 <(printf '%s\n' "${manifest_vars[@]}" | sort) <(printf '%s\n' "${doc_vars[@]}" | sort))"
[[ -z "$missing_from_docs" ]] \
  || fail "declared in appinfo/info.xml but absent from the deploy-options table: $(tr '\n' ' ' <<<"$missing_from_docs")"
[[ -z "$missing_from_manifest" ]] \
  || fail "documented in the deploy-options table but not declared in appinfo/info.xml: $(tr '\n' ' ' <<<"$missing_from_manifest")"

# 6. The substrate report is documented where an installer will look for it.
grep -qF 'recordings_access' "$INSTALL_DOC" \
  || fail "docs/exapp-install.md does not document the /status recordings_access block"

echo "PASS: prerequisites are provisioned and hard-enabled, and all ${#manifest_vars[@]} declared deploy variables are documented"
