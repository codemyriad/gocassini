#!/usr/bin/env bash
# Fast fixture tests for canonical app identity, image, and route extraction.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck disable=SC1091 # SCRIPT_DIR is resolved dynamically above.
source "$SCRIPT_DIR/lib-exapp-manifest.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

command -v python3 >/dev/null 2>&1 || fail "python3 is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

cat >"$TMP_DIR/info.xml" <<'XML'
<?xml version="1.0"?>
<info>
  <id>fixture-app</id>
  <version>1.2.3-test.1</version>
  <external-app>
    <docker-install>
      <registry>registry.example.test</registry>
      <image>team/fixture</image>
      <image-tag>1.2.3-test.1</image-tag>
    </docker-install>
    <routes>
      <route><url>^first\/?$</url><verb>GET</verb><access_level>PUBLIC</access_level></route>
      <route><url>^quoted\/&quot;value&quot;$</url><verb>GET,HEAD</verb><access_level>USER</access_level></route>
      <route><url>^last\/[^\/]+$</url><verb>POST</verb><access_level>ADMIN</access_level></route>
    </routes>
  </external-app>
</info>
XML

[[ "$(exapp_app_id "$TMP_DIR/info.xml")" == "fixture-app" ]] || fail "wrong app id"
[[ "$(exapp_app_version "$TMP_DIR/info.xml")" == "1.2.3-test.1" ]] || fail "wrong app version"
[[ "$(exapp_image_ref "$TMP_DIR/info.xml")" == "registry.example.test/team/fixture:1.2.3-test.1" ]] \
  || fail "wrong image ref"

routes="$(exapp_routes_json "$TMP_DIR/info.xml")"
jq -e 'length == 3' <<<"$routes" >/dev/null || fail "expected three routes"
jq -e '.[0] == {url:"^first\\/?$",verb:"GET",access_level:0}' <<<"$routes" >/dev/null \
  || fail "PUBLIC route or ordering changed"
jq -e '.[1] == {url:"^quoted\\/\"value\"$",verb:"GET,HEAD",access_level:1}' <<<"$routes" >/dev/null \
  || fail "route escaping changed"
jq -e '.[2] == {url:"^last\\/[^\\/]+$",verb:"POST",access_level:2}' <<<"$routes" >/dev/null \
  || fail "ADMIN route or ordering changed"

# Missing scalar fields and malformed route contracts fail loudly.
sed '/<version>/d' "$TMP_DIR/info.xml" >"$TMP_DIR/no-version.xml"
if exapp_app_version "$TMP_DIR/no-version.xml" >/dev/null 2>&1; then
  fail "missing version was accepted"
fi
sed 's/<access_level>USER<\/access_level>/<access_level>MEMBER<\/access_level>/' \
  "$TMP_DIR/info.xml" >"$TMP_DIR/bad-access.xml"
if exapp_routes_json "$TMP_DIR/bad-access.xml" >/dev/null 2>&1; then
  fail "unknown access level was accepted"
fi
sed 's#<url>\^first\\/?\$</url>#<url></url>#' "$TMP_DIR/info.xml" >"$TMP_DIR/empty-url.xml"
if exapp_routes_json "$TMP_DIR/empty-url.xml" >/dev/null 2>&1; then
  fail "empty route URL was accepted"
fi

# A manifest route addition/removal changes generated JSON mechanically.
python3 - "$TMP_DIR/info.xml" "$TMP_DIR/added.xml" <<'PY'
import sys
from xml.etree import ElementTree as ET
source, target = sys.argv[1:]
tree = ET.parse(source)
routes = tree.getroot().find("./external-app/routes")
route = ET.SubElement(routes, "route")
ET.SubElement(route, "url").text = "^added$"
ET.SubElement(route, "verb").text = "DELETE"
ET.SubElement(route, "access_level").text = "ADMIN"
tree.write(target, encoding="unicode")
PY
added="$(exapp_routes_json "$TMP_DIR/added.xml")"
jq -e 'length == 4 and .[3].url == "^added$"' <<<"$added" >/dev/null \
  || fail "added manifest route did not appear in generated JSON"
python3 - "$TMP_DIR/info.xml" "$TMP_DIR/removed.xml" <<'PY'
import sys
from xml.etree import ElementTree as ET
source, target = sys.argv[1:]
tree = ET.parse(source)
routes = tree.getroot().find("./external-app/routes")
routes.remove(routes.findall("route")[1])
tree.write(target, encoding="unicode")
PY
removed="$(exapp_routes_json "$TMP_DIR/removed.xml")"
jq -e 'length == 2 and all(.[]; .url != "^quoted\\/\"value\"$")' <<<"$removed" >/dev/null \
  || fail "removed manifest route remained in generated JSON"

# The real manifest converts every route, in XML order, with the expected
# access-level numeric representation consumed by AppAPI --json-info.
real_json="$(exapp_routes_json "$ROOT/appinfo/info.xml")"
python3 - "$ROOT/appinfo/info.xml" "$TMP_DIR/expected-routes.json" <<'PY'
import json
import sys
from xml.etree import ElementTree as ET
source, target = sys.argv[1:]
levels = {"PUBLIC": 0, "USER": 1, "ADMIN": 2}
root = ET.parse(source).getroot()
result = []
for route in root.findall("./external-app/routes/route"):
    result.append({
        "url": route.findtext("url").strip(),
        "verb": route.findtext("verb").strip(),
        "access_level": levels[route.findtext("access_level").strip()],
    })
with open(target, "w", encoding="utf-8") as output:
    json.dump(result, output, separators=(",", ":"))
PY
jq -e --argjson actual "$real_json" '$actual == .' "$TMP_DIR/expected-routes.json" >/dev/null \
  || fail "real manifest route JSON is not in exact XML order/parity"

echo "PASS: ExApp identity and routes derive from info.xml with fail-loud parity"
