---
shaping: true
---

# D-395 — Production Deployment Notes

Status: final for D-395 implementation and validation.

## Deployment shape

Production Cassini should run as a Nextcloud AppAPI ExApp, not as the standalone `deployment/` compose bundle.

Expected production flow:

```text
Talk record button
→ Nextcloud Talk recording backend config
→ /index.php/apps/app_api/proxy/gocassini/api/v1/room/<token>
→ AppAPI route check (PUBLIC for Talk backend)
→ HaRP / ExApp container
→ cassini-operator
→ cassini record --talk-auth-mode hpb-internal
→ build + publish into APP_PERSISTENT_STORAGE
→ viewer serves transcript through ExApp route
```

## Before touching Talk

Verify the ExApp itself first:

1. AppAPI deploy daemon exists and Test Deploy passes.
2. Cassini ExApp is registered and enabled.
3. Container image/tag is the intended release.
4. Cassini and Cassini Admin menu entries appear in Nextcloud.
5. `GET /api/v1/welcome` through AppAPI proxy returns `{"version":1}`.
6. Admin can open Cassini Admin/control panel.
7. User/admin can open Cassini viewer.
8. Admin status endpoint reports storage and Talk config presence.

Status check:

```bash
curl -fsS -u admin:<app-password> \
  https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/status
```

Expected relevant fields:

```json
{
  "talk": {
    "secret_configured": true,
    "signaling_internal_secret_configured": true,
    "backend_url_override_configured": false
  }
}
```

These fields are boolean-only config presence checks; they never expose secret values.

## Required production inputs

Changing a value on the Nextcloud side does not mutate an already running ExApp container. The Talk recording secret and the ExApp deploy env must be kept in sync intentionally.

| Input | How to check |
|---|---|
| Talk recording secret | `CASSINI_TALK_RECORDING_SECRET` ExApp deploy option matches `spreed.recording_servers.secret`. |
| Signaling internal secret | `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` ExApp deploy option matches standalone signaling server `[clients] internalsecret`. |
| Talk recording backend URL | `spreed.recording_servers.servers[0].server` points to `https://cloud.example.com/index.php/apps/app_api/proxy/gocassini`. |
| Nextcloud callback reachability | ExApp can reach the URL Talk sends in `Talk-Recording-Backend`; if not, set `CASSINI_TALK_BACKEND_URL`. |
| Persistent storage | `APP_PERSISTENT_STORAGE` is mounted and status reports DB/work/site roots OK. |

## Existing install / upgrade note

D-395 adds a new ExApp deploy env declaration. Source inspection of AppAPI 33 shows:

- undeclared `--env` keys are ignored;
- `app_api:app:update` reuses previously stored deploy options and has no `--env` option;
- enabling an already registered ExApp does not recreate it with new env;
- `app_api:app:config:set` is separate runtime app config, not container env.

Therefore an existing pre-D-395 install may need a controlled reinstall/re-register/redeploy with data preserved so `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` is actually injected into the container.

Safe shape:

```bash
# Back up Talk config and confirm AppAPI data-volume backup policy first.
occ config:app:get spreed recording_servers > /root/recording_servers.backup

# Re-register/redeploy with all required env values. Exact production command
# depends on whether the install is App Store UI, local info.xml, or release
# package driven. The invariant is: preserve data, recreate container, pass env.
occ app_api:app:unregister gocassini --force
occ app_api:app:register gocassini <daemon-name> \
  --info-xml /path/to/gocassini-info.xml \
  --env CASSINI_TALK_RECORDING_SECRET="<same-as-spreed-recording-secret>" \
  --env CASSINI_TALK_SIGNALING_INTERNAL_SECRET="<same-as-signaling-internalsecret>" \
  --wait-finish
```

For local development/harness iteration, `--test-deploy-mode` is the preferred re-register path because it is designed for repeated local installs.

## Talk handoff

Back up existing Talk recording backend config first:

```bash
occ config:app:get spreed recording_servers | tee /root/recording_servers.backup
```

Register the Cassini backend:

```bash
occ config:app:set spreed recording_servers \
  --value='{"servers":[{"server":"https://cloud.example.com/index.php/apps/app_api/proxy/gocassini","verify":true}],"secret":"<same-secret-as-CASSINI_TALK_RECORDING_SECRET>"}'
occ config:app:set spreed call_recording --value=yes
```

Rollback:

```bash
occ config:app:set spreed recording_servers --value="$(cat /root/recording_servers.backup)"
# or if there was no previous backend:
occ config:app:delete spreed recording_servers
```

## Controlled production test

Use a non-critical conversation first.

Preferred D-395 post-fix test (mirrors the validated local helper):

1. Create a private/group or 1:1 test conversation where HPB-internal capture is required.
2. Start a call with at least one speaking participant.
3. Click Talk's Record button.
4. Confirm the operator creates a job in Cassini Admin.
5. Stop recording or leave call and let empty-room stop happen.
6. Confirm job reaches record → build → publish succeeded.
7. Open viewer and verify transcript appears.
8. Run a second controlled recording.
9. Confirm both new recordings and pre-existing viewer catalog entries remain.

## Debug checklist if Talk reports failure

Do not print secret values; check presence only.

Inside the ExApp container:

```bash
docker exec nc_app_gocassini sh -lc 'test -n "$CASSINI_TALK_RECORDING_SECRET" && echo recording_secret=set || echo recording_secret=missing'
docker exec nc_app_gocassini sh -lc 'test -n "$CASSINI_TALK_SIGNALING_INTERNAL_SECRET" && echo signaling_internal_secret=set || echo signaling_internal_secret=missing'
docker exec nc_app_gocassini sh -lc 'test -n "$CASSINI_TALK_BACKEND_URL" && echo backend_url_override=set || echo backend_url_override=empty'
docker exec nc_app_gocassini sh -lc 'printf "APP_PERSISTENT_STORAGE=%s\n" "$APP_PERSISTENT_STORAGE"'
```

Nextcloud/Talk config:

```bash
occ config:app:get spreed recording_servers
occ config:app:get spreed call_recording
occ config:system:get overwrite.cli.url
```

Job logs to inspect for:

```text
talk auth mode hpb-internal requires CASSINI_TALK_SIGNALING_INTERNAL_SECRET to be set
participants/active
recording/store
talk stopped
compose final output failed
requestoffer
no media
```

## Archive preservation check

```bash
docker exec nc_app_gocassini sh -lc 'find "$APP_PERSISTENT_STORAGE/operator/jobs/current" -maxdepth 1 -name "*.meeting" | sort'
docker exec nc_app_gocassini sh -lc 'python3 - <<PY
import json, os
p=os.path.join(os.environ["APP_PERSISTENT_STORAGE"], "site/published/catalog.json")
d=json.load(open(p))
print(len(d.get("meetings", [])))
print([m.get("id") for m in d.get("meetings", [])])
PY'
```

Interpretation:

- many `current/*.meeting` + many catalog entries, including both controlled test jobs: expected preservation;
- one `current/*.meeting` + one catalog entry after multiple recordings: publish input collapsed;
- many `current/*.meeting` + one catalog entry: investigate publish/export behavior;
- many catalog entries but viewer shows one: investigate viewer/catalog fetch or caching.

## Scope note

D-395 keeps the Cassini ExApp persistent volume as the authoritative store for rich artifacts and the viewer. Raw Talk upload to Nextcloud Files remains protocol/delivery behavior. Nextcloud-Files-native rich artifact delivery and owner-scoped archive redesign are separate product decisions.
