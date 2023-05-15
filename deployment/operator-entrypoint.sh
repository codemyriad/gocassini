#!/usr/bin/env bash
set -euo pipefail

mkdir -p \
  /var/lib/cassini-operator \
  /var/lib/cassini-operator/jobs \
  /var/lib/cassini-operator/cache \
  /var/lib/cassini-operator/tmp \
  /srv/cassini-site

if [[ ! -f /srv/cassini-site/index.html ]]; then
  rm -rf /srv/cassini-site/*
  cp /opt/cassini/cassini-viewer/dist/index.html /srv/cassini-site/index.html
  cp -R /opt/cassini/cassini-viewer/dist/assets /srv/cassini-site/assets
  cat > /srv/cassini-site/catalog.json <<'EOF'
{
  "version": "cassini.viewer.catalog.v1",
  "meetings": []
}
EOF
  cat > /srv/cassini-site/cassini.json <<EOF
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
