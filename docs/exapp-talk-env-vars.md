# Talk recording — environment variables

## The core mismatch

`deployment/` can pass any env var directly to containers. AppAPI-installed ExApps cannot: AppAPI only forwards variables declared in `appinfo/info.xml` `<environment-variables>`.

D-395 fixed the missing required variable for production Talk recording by declaring it in `appinfo/info.xml`:

```text
CASSINI_TALK_SIGNALING_INTERNAL_SECRET
```

Without that variable, the default `hpb-internal` recorder path fails before it can record private/group/1:1 Talk calls.

AppAPI only forwards env vars declared in `info.xml`, so declaring it there is a prerequisite for the admin-supplied value to reach the ExApp container.

## Installed ExApp env vars

| Variable | Required? | Source | Purpose |
|---|---:|---|---|
| `CASSINI_TALK_RECORDING_SECRET` | Yes for Talk record button | Admin deploy option declared in `appinfo/info.xml` | HMAC shared secret for Talk's recording-backend protocol; must match `spreed.recording_servers.secret`. |
| `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` | Yes for HPB-internal/default Talk recording | Admin deploy option declared in `appinfo/info.xml` | Internal client secret for standalone Nextcloud Talk signaling / HPB; must match signaling config `[clients] internalsecret`. |
| `CASSINI_TALK_BACKEND_URL` | Optional, sometimes required | Admin deploy option declared in `appinfo/info.xml` | Override base URL the operator uses for callbacks/upload/OCS calls back to Nextcloud. Use when Talk advertises a URL unreachable from the ExApp container. |
| `OPENROUTER_API_KEY` | Optional | Admin deploy option | Enables transcript cleanup / summaries through OpenRouter or compatible endpoint. Privacy: when set, the full local transcript is sent to that third party; transcription itself is always local. |
| `LLM_BASE_URL` | Optional | Admin deploy option | OpenAI-compatible base URL. |
| `LLM_MODEL` | Optional | Admin deploy option | Model identifier for cleanup/summaries. |
| `CASSINI_OPERATOR_API_TOKEN` | Optional | Admin deploy option | Bearer token for direct non-AppAPI operator API calls; proxied AppAPI requests are authenticated by AppAPI. |

## AppAPI-injected runtime vars

Admins normally do not set these by hand for a real installed ExApp:

| Variable | Purpose |
|---|---|
| `APP_HOST` / `APP_PORT` | Container bind address/port. |
| `APP_ID` | ExApp id (`gocassini`). |
| `APP_VERSION` | App version from manifest. |
| `APP_SECRET` | Shared secret between AppAPI and ExApp middleware. |
| `AA_VERSION` | AppAPI version. |
| `NEXTCLOUD_URL` | Base URL the ExApp uses to call back to Nextcloud/AppAPI. Must be reachable from the ExApp container. |
| `APP_PERSISTENT_STORAGE` | Path to the AppAPI persistent volume. Cassini redirects DB/work/site defaults under it. |
| `COMPUTE_DEVICE` | AppAPI daemon compute device (`cpu`, `cuda`, `rocm`). |
| `HP_FRP_ADDRESS` / `HP_FRP_PORT` / `HP_SHARED_KEY` | HaRP tunnel parameters when using a tunneled HaRP daemon. |

## `deployment/` suite vars

The proven standalone suite uses `deployment/.env.example` and `deployment/compose.yml`.

Important parity vars:

| Variable | Purpose |
|---|---|
| `CASSINI_TALK_RECORDING_SECRET` | Same HMAC secret as installed ExApp. |
| `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` | Same HPB internal secret; already passed in `deployment/compose.yml`. |
| `CASSINI_TALK_BACKEND_URL` | Container-reachable Nextcloud base URL for callbacks/upload. |
| `CASSINI_OPERATOR_BASE_PATH` | `/` for standalone suite; `/operator` for ExApp image. |
| `CASSINI_OPERATOR_STATE_STORAGE` | Optional bind/named volume override for DB/work root. |
| `CASSINI_PUBLISHED_SITE_STORAGE` | Optional bind/named volume override for published site. |

## Harness vars

| Variable | Typical value | Purpose |
|---|---|---|
| `SPREED_PROFILE` | `full` | Legacy compatibility profile. Prefer `CASSINI_HARNESS_SERVICE_MODE` / `cassini dev stack --services` for new flows. |
| `CASSINI_HARNESS_SERVICE_MODE` | `legacy-default`, `core`, `appapi`, `full`, `full-remote` | Resolved service topology for `cassini dev stack`. |
| `CASSINI_HARNESS_CASSINI_MODE` | `none`, `installed-exapp` | Whether stack setup installs/registers Cassini as an ExApp. Default is `none`. |
| `CASSINI_HARNESS_RECORDING_BACKEND` | `legacy`, `direct-operator`, `installed-exapp`, `none` | Which Talk recording backend bootstrap writes. |
| `CASSINI_HARNESS_EXAPP_IMAGE_MODE` | `build`, `reuse-local`, `pull` | Installed-ExApp image strategy. `--build` maps to `build`. |
| `CASSINI_HARNESS_PATCH_MODE` | `auto`, `none`, `force` | Scenario-associated patch behavior, mapping to `--patch=auto|none|force`. |
| `CASSINI_TALK_RECORDING_URL` | `http://reverse-proxy/index.php/apps/app_api/proxy/gocassini` in installed-ExApp harness | Optional override for where Talk sends recording-backend requests. |
| `CASSINI_TALK_RECORDING_SECRET` | dev fallback or generated | Secret written into Talk `recording_servers`; must be passed into installed ExApp. |
| `SIGNALING_INTERNAL_SECRET` | dev fallback from harness common | Secret in signaling config; must match `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`. |
| `CASSINI_HARNESS_VM` | `true` | Makes `cassini dev stack up/down` use `harness/vm`. |
| `CASSINI_HARNESS_HOST` | VM IP, e.g. `192.168.252.29` | Legacy browser-facing host/IP for VM/LAN harness URLs. |
| `CASSINI_HARNESS_PUBLIC_MODE` | `local-http`, `lan-http`, `remote-https` | Explicit browser/public mode. Remote public env vars require `remote-https`. |
| `CASSINI_HARNESS_PUBLIC_URL` | `https://<16a-fqdn>` | Browser-facing HTTPS origin when `CASSINI_HARNESS_PUBLIC_MODE=remote-https`. Enables Nextcloud HTTPS overwrite config and public call URLs. |
| `CASSINI_HARNESS_PUBLIC_HOST` | `<16a-fqdn>` | Bare public hostname for trusted domains, Docker-network split DNS, and signaling backend allow-list entries. Derived from `CASSINI_HARNESS_PUBLIC_URL` in remote mode when omitted. |
| `CASSINI_HARNESS_MEDIA_HOST` | `<16a-tailscale-ip>` | Host/IP advertised to browser WebRTC via Janus and TURN. Required for remote full media. |
| `CASSINI_HARNESS_SIGNALING_PUBLIC_URL` | `https://<16a-fqdn>:8443` | Optional override for the browser-facing standalone signaling URL; otherwise remote mode derives this from the public host. |
| `NEXTCLOUD_URL` | Usually `http://127.0.0.1:28080` | Harness script/API URL for local Nextcloud access. Public room links use `NEXTCLOUD_PUBLIC_URL` / `CASSINI_HARNESS_PUBLIC_URL` in remote mode. |

## Registration shape after D-395

Installed-harness/prod registration shape:

```bash
occ app_api:app:register gocassini <daemon-name> \
  --info-xml /tmp/gocassini-info.xml \
  --env CASSINI_TALK_RECORDING_SECRET="${CASSINI_SECRET}" \
  --env CASSINI_TALK_SIGNALING_INTERNAL_SECRET="${SIGNALING_INTERNAL_SECRET}" \
  --wait-finish
```

Optional, only when callbacks from the ExApp cannot reach the advertised Nextcloud URL:

```bash
  --env CASSINI_TALK_BACKEND_URL="https://cloud.example.com"
```

## AppAPI env mutability

AppAPI deploy env is a container-creation input, not live Nextcloud app config.

Important consequences from source inspection:

- AppAPI only passes env vars declared in `info.xml`.
- `occ app_api:app:register --env KEY=VALUE` and the web install deploy-options form apply values at install/register time.
- `app_api:app:update` reuses previously stored deploy options; it has no `--env` option.
- Enabling an already registered ExApp does not redeploy with new env values.
- `app_api:app:config:set` writes a separate ExApp app-config store; it does not become process env and Cassini does not currently read it.
- Changing `spreed.recording_servers.secret` in Nextcloud does **not** update `CASSINI_TALK_RECORDING_SECRET` in a running ExApp container.

For local harness iteration, `cassini dev stack up --cassini installed-exapp` uses AppAPI `--test-deploy-mode` with all required `--env` values. For production secret rotation or first upgrade from a pre-D-395 install, plan a controlled ExApp redeploy/reinstall with data preserved.

## Secret rotation / coordinated updates

Rotate the Talk recording secret as a coordinated change:

1. Pause or avoid active recordings.
2. Update Nextcloud Talk `spreed.recording_servers.secret`.
3. Recreate/redeploy Cassini ExApp with matching `CASSINI_TALK_RECORDING_SECRET`.
4. Verify `/operator/status` reports the recording secret configured.
5. Run a controlled recording.

Rotate the HPB internal signaling secret as a coordinated change:

1. Update standalone signaling/HPB `internalsecret` and restart signaling as required.
2. Recreate/redeploy Cassini ExApp with matching `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.
3. Verify `/operator/status` reports internal signaling secret configured.
4. Run a private/group/1:1 recording to prove HPB-internal auth.

A mismatch on either side can fail before recording starts, during Talk HMAC validation, during signaling internal `hello`, or during callback/upload.

## Secret handling

- Never print secret values in docs, status responses, or logs.
- Status endpoints should report only booleans/presence.
- AppAPI deploy options may store secret values for redeploy/update; treat AppAPI/Nextcloud DB backups and admin logs as sensitive.
- The committed harness defaults are development-only and public in repo history; never reuse them in production.
