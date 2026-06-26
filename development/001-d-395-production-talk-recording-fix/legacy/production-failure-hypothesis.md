# Production ExApp Failure Hypothesis

Status: hypothesis, not yet verified on the production AppAPI host
Date: 2026-06-22

## Problem

The deployed Nextcloud ExApp can fail when recording is started through Talk, even though local/dev paths can pass. The failure must be diagnosed in the production ExApp topology, not only in compose or direct-operator tests.

The current production shape is:

```text
Talk record button
→ Nextcloud AppAPI proxy route /apps/app_api/proxy/gocassini/api/v1/room/<token>
→ HaRP / ExApp container
→ cassini-operator Talk backend
→ child cassini recorder
→ Talk signaling/HPB + Talk callbacks/store upload
→ build + publish from AppAPI persistent storage
```

## Primary Hypothesis: Missing HPB-Internal Secret

The most likely failure is that the production ExApp does not receive `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.

Evidence from current `main`:

- `cassini-operator/internal/operator/talk_backend.go` creates Talk-triggered jobs with `TalkAuthMode: hpb-internal`.
- `cassini-go-recorder/internal/talk/recorder.go` validates that `hpb-internal` has both `CASSINI_TALK_RECORDING_SECRET` and `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.
- `deployment/compose.yml` and harness paths pass `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.
- `appinfo/info.xml` does not declare `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.
- AppAPI only passes admin-supplied environment variables declared in `appinfo/info.xml`; undeclared variables are dropped.
- `/operator/status` reports Talk recording secret and backend URL presence, but not internal signaling secret presence.
- `docs/exapp-install.md` still describes guest/public-room recording, so it does not instruct admins to configure the HPB-internal secret.

Expected log if this hypothesis is true:

```text
talk auth mode hpb-internal requires CASSINI_TALK_SIGNALING_INTERNAL_SECRET to be set
```

## Secondary Hypotheses

### H2: ExApp Cannot Reach The Talk Backend URL

PR #41 surfaced that `CASSINI_TALK_BACKEND_URL` must be a URL the ExApp container can dial back to Nextcloud. A browser-reachable URL is not enough if the container network cannot resolve or route it.

Expected evidence:

- recorder/operator logs show callback, participants, or upload requests failing with connection, DNS, TLS, proxy, or HTTP errors;
- `CASSINI_TALK_BACKEND_URL` points at a host visible to users but not to the ExApp container.

### H3: Nextcloud `overwrite.cli.url` Is Not Reachable From The ExApp

PR #41 also surfaced that Nextcloud can advertise URLs derived from `overwrite.cli.url`. If that value is not reachable from the ExApp container, recorder calls can fail after the Talk start handoff.

Expected evidence:

- failures on OCS/Talk participant or room-state requests;
- different behavior between bot streamers inside the harness and the recorder inside the ExApp container.

### H4: Talk Store Upload Or Stopped Callback Fails

Cassini uploads raw `recording.mkv` to Talk's store endpoint as part of the native recording lifecycle. Callback/store failure can make Talk show recording failure even if Cassini has preserved local run artifacts.

Expected evidence:

- `talk_delivered_at` remains unset;
- logs show failed `stopped` callback or `/store` upload;
- Talk UI reports failure while Cassini work-root artifacts exist.

### H5: Media Capture Or Final MKV Composition Fails

Recorder/media failures can still occur after a valid handoff. Open PRs #80 and #81 address D-386 capture reliability by surfacing and recovering participants whose media never starts.

Expected evidence:

- recorder logs show requestoffer exhaustion, no media, low/no audio, final compose failure, or truncated media;
- job attempt logs show a recording-stage failure before build/publish.

### H6: Published Archive Input Collapses

This is a separate visible-output failure rather than the likely Talk-recording failure. `cassini publish` rebuilds from `<work-root>/current/*.meeting`; if production `current` contains only the latest meeting, the live catalog will also contain only that meeting.

Detailed evidence and checks live in `archive-overwrite-hypothesis.md`.

## Read-Only Validation Plan

Run these checks on the production Nextcloud/AppAPI Docker host without printing secret values:

```bash
docker inspect nc_app_gocassini --format '{{.Config.Image}}'
docker exec nc_app_gocassini sh -lc 'printf "APP_PERSISTENT_STORAGE=%s\n" "$APP_PERSISTENT_STORAGE"'
docker exec nc_app_gocassini sh -lc 'test -n "$CASSINI_TALK_RECORDING_SECRET" && echo talk_recording_secret=set || echo talk_recording_secret=missing'
docker exec nc_app_gocassini sh -lc 'test -n "$CASSINI_TALK_SIGNALING_INTERNAL_SECRET" && echo talk_signaling_internal_secret=set || echo talk_signaling_internal_secret=missing'
docker exec nc_app_gocassini sh -lc 'test -n "$CASSINI_TALK_BACKEND_URL" && echo talk_backend_url_override=set || echo talk_backend_url_override=empty'
```

Inspect latest job/attempt logs for:

```text
CASSINI_TALK_SIGNALING_INTERNAL_SECRET
participants/active
recording/store
talk stopped
compose final output failed
requestoffer
no media
```

Check Nextcloud/Talk config:

```bash
occ config:app:get spreed recording_servers
occ config:system:get overwrite.cli.url
occ config:app:get spreed call_recording
```

Check archive preservation:

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

## Expected Fix If H1 Is Confirmed

- Declare `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` in `appinfo/info.xml` with safe admin-facing help text.
- Update `docs/exapp-install.md` so HPB-internal is the documented default path and guest/public behavior is only a fallback if intentionally supported.
- Update `/operator/status` to report internal signaling secret presence without exposing the value.
- Re-register or redeploy the ExApp with the internal secret set through AppAPI.
- Run a controlled production recording test and verify record → build → publish → Talk upload.
