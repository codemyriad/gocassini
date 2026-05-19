# Trying the ExApp image in a local Nextcloud

Three tiers, in increasing setup cost. Pick the lowest one that answers your question.

## Tier 1 — Image-only checks (no Nextcloud)

Verifies the image builds and the HTTP plane works (operator API, lifecycle, both SPAs).
This is what the CI workflow runs.

```bash
# Build the image locally
docker build -f deployment/Dockerfile.exapp -t cassini-exapp:local .

# Smoke test: image in dev mode (no APP_SECRET, middleware off)
IMAGE_REF=cassini-exapp:local ./harness/bin/ci-smoke-exapp.sh

# E2E: image with APP_SECRET set; asserts middleware refuses without auth,
# accepts with valid AppAPI headers, lifecycle works, state survives restart
IMAGE_REF=cassini-exapp:local ./harness/bin/ci-e2e-exapp.sh
```

You can also pull the published image instead of building:

```bash
docker pull ghcr.io/codemyriad/gocassini:latest
IMAGE_REF=ghcr.io/codemyriad/gocassini:latest ./harness/bin/ci-smoke-exapp.sh
```

## Tier 2 — Manual install against a real local Nextcloud

This is what an admin would do, scripted enough to repeat.

### Prerequisites

- Docker + docker compose
- A free port range (28080 by default for Nextcloud)
- The Cassini image, either built or pulled

### Steps

1. **Bring up Nextcloud + database.** The harness compose file already has a Nextcloud service; use its `default` profile (no Talk / signaling overhead needed for an ExApp install test):

   ```bash
   cd harness
   SPREED_PROFILE=default docker compose -p cassini-exapp-test up -d nextcloud db
   ./bin/bootstrap.sh   # waits for Nextcloud to be ready
   ```

2. **Install AppAPI and HaRP** inside the Nextcloud container:

   ```bash
   alias occ='docker compose -p cassini-exapp-test exec -T -u www-data nextcloud php occ'
   occ app:install app_api
   occ app:enable app_api
   ```

3. **Register a "manual-install" daemon** — this is the AppAPI daemon type for
   "the admin runs the container themselves, AppAPI just keeps track of where
   to reach it":

   ```bash
   # Replace HOST with the address Nextcloud uses to reach your container.
   # On Linux with docker-compose default bridge, this is the gateway IP:
   GATEWAY=$(docker network inspect cassini-exapp-test_default \
     -f '{{(index .IPAM.Config 0).Gateway}}')

   occ app_api:daemon:register \
       manual_install \
       "Local manual install" \
       manual-install \
       0 \
       "${GATEWAY}" \
       "${GATEWAY}"
   ```

4. **Run the Cassini container** with the env AppAPI would normally inject:

   ```bash
   APP_SECRET="$(head -c 24 /dev/urandom | base64 | tr -d /+= | head -c 32)"
   docker run -d --name cassini-exapp \
       --network cassini-exapp-test_default \
       -e APP_HOST=0.0.0.0 \
       -e APP_PORT=8080 \
       -e APP_ID=gocassini \
       -e APP_VERSION=0.1.0 \
       -e APP_SECRET="${APP_SECRET}" \
       -e AA_VERSION=5.0.0 \
       -e CASSINI_APPAPI_REQUIRED=true \
       -e CASSINI_OPERATOR_BASE_PATH=/operator \
       -v cassini-exapp-state:/var/lib/cassini-operator \
       -v cassini-exapp-site:/srv/cassini-site \
       --entrypoint /usr/local/bin/cassini-operator \
       cassini-exapp:local
   ```

   We override the entrypoint to skip `frpc` (no HaRP tunnel in this
   tier — we'll reach the container directly via the docker network).

5. **Register Cassini with AppAPI** as a manual-install app:

   ```bash
   CASSINI_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' cassini-exapp)
   occ app_api:app:register gocassini manual_install \
       --info-xml /var/www/html/custom_apps/info.xml \
       --json '{"appid":"gocassini","name":"Cassini","daemon_config_name":"manual_install","version":"0.1.0","secret":"'"${APP_SECRET}"'","host":"'"${CASSINI_IP}"'","port":8080,"protocol":"http","system_app":0}'
   # `--info-xml` expects a path inside the Nextcloud container; copy info.xml there first:
   docker compose -p cassini-exapp-test cp ../appinfo/info.xml nextcloud:/var/www/html/custom_apps/info.xml
   ```

6. **Open Nextcloud** at http://127.0.0.1:28080 (admin/admin), go to
   Settings → Apps → External Apps. Cassini should appear; click Enable.
   AppAPI calls `PUT /enabled` on the container, which returns 200, and the
   app activates.

7. **Verify routes:**
   - `http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/control-panel/` — Svelte admin UI (admin only)
   - `http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/viewer/` — viewer SPA (any logged-in user)
   - `http://127.0.0.1:28080/index.php/apps/app_api/proxy/gocassini/operator/jobs` — operator JSON API (admin only)

### Teardown

```bash
docker rm -f cassini-exapp
docker volume rm cassini-exapp-state cassini-exapp-site
docker compose -p cassini-exapp-test down --volumes
```

## Tier 3 — Production-shaped install via HaRP

A real production AppAPI install spawns the container itself via the deploy
daemon and routes traffic through HaRP. Reproducing this locally requires:

- AppAPI's HaRP server running alongside Nextcloud
- A Docker daemon registered with AppAPI's docker-install daemon type
- The container's `frpc` dialing the HaRP server on startup

This is not yet automated in the harness. The deferred Slice D (in
`planning/installable-nextcloud-app.md`) tracks turning Tier 2 into a CI E2E
plus extending it to a real HaRP-fronted install.

For now: Tier 2 verifies the install + enable contract using the same routes
and access levels a production install would use; Tier 1 verifies the
middleware itself.
