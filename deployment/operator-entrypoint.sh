#!/usr/bin/env bash
set -euo pipefail

export CASSINI_LIB_DIR="${CASSINI_LIB_DIR:-/opt/cassini/lib}"
export LD_LIBRARY_PATH="${CASSINI_LIB_DIR}${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

SITE_ROOT="${CASSINI_OPERATOR_SITE_ROOT:-/srv/cassini-site/published}"
SITE_PARENT="$(dirname "$SITE_ROOT")"
SITE_NAME="$(basename "$SITE_ROOT")"

mkdir -p \
  /var/lib/cassini-operator \
  /var/lib/cassini-operator/jobs \
  /var/lib/cassini-operator/cache \
  /var/lib/cassini-operator/tmp \
  "$SITE_ROOT"

# Seeded-site sentinel is catalog.json, not index.html (D-531): a lightweight
# publish writes catalog.json + meetings/ but no viewer shell, so gating on
# index.html would treat a real published site as unseeded and wipe it below.
if [[ ! -f "$SITE_ROOT/catalog.json" && -f "$SITE_PARENT/catalog.json" ]]; then
  shopt -s dotglob nullglob
  for path in "$SITE_PARENT"/*; do
    name="$(basename "$path")"
    case "$name" in
      "$SITE_NAME"|"$SITE_NAME.staging"|"$SITE_NAME.backup")
        continue
        ;;
    esac
    mv "$path" "$SITE_ROOT/"
  done
  shopt -u dotglob nullglob
fi

if [[ ! -f "$SITE_ROOT/catalog.json" ]]; then
  rm -rf "${SITE_ROOT:?}"/*
  cp /opt/cassini/cassini-viewer/dist/index.html "$SITE_ROOT/index.html"
  cp -R /opt/cassini/cassini-viewer/dist/assets "$SITE_ROOT/assets"
  cat > "$SITE_ROOT/catalog.json" <<'EOF'
{
  "version": "cassini.viewer.catalog.v1",
  "meetings": []
}
EOF
  cat > "$SITE_ROOT/cassini.json" <<EOF
{
  "kind": "site",
  "version": "cassini.site.v1",
  "created_at_utc": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "state": "ready",
  "stage": "ready",
  "source_path": "seed"
}
EOF
fi

exec /usr/local/bin/cassini-operator "$@"
