# shellcheck shell=bash
# Canonical readers for appinfo/info.xml used by harness tests.
# Meant to be sourced; defines functions only and has no side effects.

_exapp_manifest_query() {
  local info_xml="$1"
  local query="$2"
  python3 - "$info_xml" "$query" <<'PY'
import json
import sys
import xml.etree.ElementTree as ET

path, query = sys.argv[1:]
try:
    root = ET.parse(path).getroot()
except (OSError, ET.ParseError) as exc:
    raise SystemExit(f"error: could not parse {path}: {exc}")

scalar_paths = {
    "app-id": "./id",
    "app-version": "./version",
    "image-registry": "./external-app/docker-install/registry",
    "image-name": "./external-app/docker-install/image",
    "image-tag": "./external-app/docker-install/image-tag",
}

if query in scalar_paths:
    nodes = root.findall(scalar_paths[query])
    values = [(node.text or "").strip() for node in nodes]
    if len(values) != 1 or not values[0]:
        raise SystemExit(
            f"error: expected one non-empty {query} in {path}, got {len(values)}"
        )
    print(values[0])
    raise SystemExit(0)

if query == "routes-json":
    access_levels = {"PUBLIC": 0, "USER": 1, "ADMIN": 2}
    routes = []
    for index, route in enumerate(root.findall("./external-app/routes/route"), 1):
        values = {}
        for field in ("url", "verb", "access_level"):
            nodes = route.findall(field)
            text = (nodes[0].text or "").strip() if len(nodes) == 1 else ""
            if len(nodes) != 1 or not text:
                raise SystemExit(
                    f"error: route {index} must contain one non-empty <{field}> in {path}"
                )
            values[field] = text
        access = values["access_level"]
        if access not in access_levels:
            raise SystemExit(
                f"error: route {index} has unknown access_level {access!r} in {path}"
            )
        routes.append(
            {
                "url": values["url"],
                "verb": values["verb"],
                "access_level": access_levels[access],
            }
        )
    if not routes:
        raise SystemExit(f"error: no ExApp routes found in {path}")
    print(json.dumps(routes, separators=(",", ":"), ensure_ascii=False))
    raise SystemExit(0)

raise SystemExit(f"error: unsupported manifest query: {query}")
PY
}

exapp_app_id() {
  _exapp_manifest_query "$1" app-id
}

exapp_app_version() {
  _exapp_manifest_query "$1" app-version
}

exapp_image_registry() {
  _exapp_manifest_query "$1" image-registry
}

exapp_image_name() {
  _exapp_manifest_query "$1" image-name
}

exapp_image_tag() {
  _exapp_manifest_query "$1" image-tag
}

exapp_image_ref() {
  local info_xml="$1"
  printf '%s/%s:%s\n' \
    "$(exapp_image_registry "$info_xml")" \
    "$(exapp_image_name "$info_xml")" \
    "$(exapp_image_tag "$info_xml")"
}

exapp_routes_json() {
  _exapp_manifest_query "$1" routes-json
}
