---
shaping: true
---

# Nextcloud API Spike: private calls and recording-from-start

## Context

The continuation shape now prefers a separate `cassini dev play-private` surface. The remaining unknown is whether Nextcloud Talk exposes enough API surface for the command to:

1. authenticate as scaffolded Nextcloud users,
2. create and join private 1:1 conversations/calls, and
3. start Talk integrated recording from Nextcloud before playback media begins.

## Goal

Identify the concrete Nextcloud/Talk API sequence needed for `cassini dev play-private --conversation synthetic|admin` and determine whether the preferred shape can satisfy the requirements without using the Cassini operator API directly.

## Questions

| # | Question |
|---|----------|
| **N1-Q1** | How do we authenticate the synthetic playback clients as real Nextcloud users? |
| **N1-Q2** | How do we create or resolve private 1:1 Talk conversations idempotently? |
| **N1-Q3** | How do authenticated clients join a private conversation and private call? |
| **N1-Q4** | How do we start Talk integrated recording through Nextcloud, and what permissions/status values are required? |
| **N1-Q5** | How can playback avoid losing the beginning of the fixture while recording starts? |
| **N1-Q6** | What implementation changes are needed in the current harness player/rotator path? |

## Acceptance

This spike is complete when we can describe the Nextcloud API sequence, identify required auth/session behavior, and outline the implementation changes needed for private 1:1 playback with recording started through Nextcloud.

---

## Evidence sources

Local code/docs:

- `harness/bin/d263-nextcloud-lifecycle.sh`
- `harness/bin/bootstrap.sh`
- `harness/go-talk-rotator/main.go`
- `cassini-operator/internal/operator/talk_backend.go`
- `cassini-operator/internal/operator/record_runtime.go`

Upstream Nextcloud Talk source/docs inspected from `nextcloud/spreed` main:

- `docs/conversation.md`
- `docs/call.md`
- `docs/recording.md`
- `docs/constants.md`
- `lib/Controller/RoomController.php`
- `lib/Controller/CallController.php`
- `lib/Controller/RecordingController.php`
- `lib/Service/RoomService.php`
- `lib/Service/ParticipantService.php`
- `lib/Service/RecordingService.php`
- `lib/Recording/BackendNotifier.php`
- `lib/Controller/SignalingController.php`

---

## Findings

### N1-Q1 — Authentication

For the private path, the playback clients must be authenticated Nextcloud users, not Talk guests.

Mechanism:

1. Create one OCS HTTP client per simulated participant.
2. Give each client its own cookie jar.
3. Send Basic Auth (`username:password`) on OCS requests for that participant.
4. Reuse that same cookie jar through:
   - `POST /ocs/v2.php/apps/spreed/api/v4/room/{token}/participants/active`
   - `GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings?token={token}`
   - `POST /ocs/v2.php/apps/spreed/api/v4/call/{token}`
   - recording start if this participant is the recording starter

Why the cookie jar matters:

- Talk's call docs explicitly note that joining a room/call needs cookies because a session is required.
- `RoomController::joinRoom()` stores the Talk session id in the Nextcloud server session for the current room.
- `SignalingController::getSettings()` returns user-bound `helloAuthParams` when the request is authenticated.
- The standalone signaling hello then validates the returned ticket through `/ocs/v2.php/apps/spreed/api/v3/signaling/backend`.

Current gap:

- `harness/go-talk-rotator` currently has a cookie jar, but it does not support per-bot Basic Auth credentials. Its OCS client is guest-only.

Implementation implication:

- Extend the rotator with optional per-bot credentials, e.g. repeated `--auth-user` / `--auth-password` flags or a private-player wrapper that maps participant index to credentials.
- When credentials are present, set Basic Auth on every OCS request for that bot and skip the guest-name endpoint.

### N1-Q2 — Idempotent private conversation creation

Talk conversation type constants:

- `1` = one-to-one
- `2` = group
- `3` = public

The private 1:1 creation endpoint is:

```http
POST /ocs/v2.php/apps/spreed/api/v4/room
OCS-APIRequest: true
Accept: application/json
Authorization: Basic <creator-user>
Content-Type: application/x-www-form-urlencoded

roomType=1&invite=<target-user-id>
```

Evidence:

- `docs/conversation.md` says creating `roomType = 1` uses `invite` as the target user id.
- `RoomController::createRoom()` routes `Room::TYPE_ONE_TO_ONE` to `createOneToOneRoom($invite)`.
- Existing one-to-one conversations return `200 OK`; newly created conversations return `201 Created`.
- `RoomService::createOneToOneConversation()` creates the one-to-one room with both user ids encoded in the room name and initially adds the creator as `Participant::OWNER`.
- `ParticipantService::ensureOneToOneRoomIsFilled()` adds missing one-to-one participants as `Participant::OWNER` when the other user later joins/fetches the room.

Scaffold implications:

- Create synthetic conversation as `cassini-erlich` inviting `cassini-monica`.
- Create admin conversation as `admin` inviting `cassini-erlich`.
- Store both returned tokens in `harness/runtime`, e.g. a JSON scaffold state file.
- Re-running the scaffold is idempotent because the one-to-one API reuses existing rooms and returns the existing token.

User creation is not the hard API seam:

- Existing harness bootstrap already creates users with `occ user:add --password-from-env --display-name=...`.
- `play-private --scaffold-only` can follow that harness convention or use the provisioning API later. The private Talk API does not depend on which user-creation mechanism we choose.

### N1-Q3 — Joining private conversations and calls

Conversation/session join:

```http
POST /ocs/v2.php/apps/spreed/api/v4/room/{token}/participants/active
OCS-APIRequest: true
Accept: application/json
Authorization: Basic <participant-user>
Cookie: <participant cookie jar>
Content-Type: application/x-www-form-urlencoded

force=true
```

Expected response includes the session id in the formatted room payload or the current guest-compatible response shape. Existing rotator code already expects `data.sessionId` and stores it as `nextcloudSessionID`.

Signaling settings:

```http
GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings?token={token}
OCS-APIRequest: true
Accept: application/json
Authorization: Basic <participant-user>
Cookie: <same participant cookie jar>
```

This returns:

- external signaling server URL
- `helloAuthParams` for the authenticated user
- STUN/TURN settings

Signaling join remains the existing rotator sequence:

1. connect WebSocket to the returned signaling server,
2. send `hello` with `auth.url = <base>/ocs/v2.php/apps/spreed/api/v3/signaling/backend` and returned params,
3. send `room` with `roomid = token` and `sessionid = sessionId from participants/active`.

Call join:

```http
POST /ocs/v2.php/apps/spreed/api/v4/call/{token}
OCS-APIRequest: true
Accept: application/json
Authorization: Basic <participant-user>
Cookie: <same participant cookie jar>
Content-Type: application/json

{"flags":7,"silent":false,"recordingConsent":true,"silentFor":[]}
```

Notes:

- `flags=7` means in-call + audio + video (`1|2|4`).
- Existing rotator already uses this effective value (`joinFlagsAudioVideo`).
- `recordingConsent=true` is safe for deployments that require consent; if consent is not required, Talk accepts the call join path normally.
- `CallController::joinCall()` calls `ensureOneToOneRoomIsFilled()` for one-to-one rooms, then changes the user's in-call state and sets the room active time.

Current gap:

- The rotator can join public rooms as guests. It needs authenticated OCS requests and guest-name skipping to join private rooms as users.

### N1-Q4 — Starting Talk integrated recording

The Nextcloud-native recording start endpoint is:

```http
POST /ocs/v2.php/apps/spreed/api/v1/recording/{token}
OCS-APIRequest: true
Accept: application/json
Authorization: Basic <moderator-or-owner-user>
Cookie: <same participant cookie jar>
Content-Type: application/x-www-form-urlencoded

status=1
```

Status constants:

- `1` = recording video
- `2` = recording audio
- `3` = starting video recording
- `4` = starting audio recording

Use `status=1` for this simulator because the player publishes audio + video and the existing D-263 lifecycle script defaults to `RECORDING_STATUS=1`.

Required preconditions from docs/source:

- Talk recording backend is configured and `call_recording` is enabled. Harness bootstrap already sets:
  - `spreed recording_servers`
  - `spreed call_recording=yes`
- The caller must be a logged-in moderator/owner participant.
- The call must already be active (`RecordingService::start()` fails with `error=call` if `active_since` is absent).
- A guest cannot start recording.
- If recording is already in progress, the endpoint fails with `error=recording`.

Why 1:1 participants can start it:

- `RoomService::createOneToOneConversation()` adds the creator as `Participant::OWNER`.
- `ParticipantService::ensureOneToOneRoomIsFilled()` adds missing 1:1 participants as `Participant::OWNER`.
- `RecordingController::start()` requires a logged-in moderator participant; owner satisfies that.

What happens after POST:

- `RecordingService::start()` calls `BackendNotifier::start()` synchronously.
- The notifier posts to the configured recording backend at `/api/v1/room/{token}` with signed Talk recording headers.
- Only after the backend accepts the start request does Talk set room recording state to `RECORDING_VIDEO_STARTING` (`3`).
- The Cassini operator's Talk recording backend then starts a normal `hpb-internal` record job without the simulator calling the operator API directly.
- When `cassini record` emits `talk recorder running:`, the operator calls Nextcloud's `/recording/backend` callback with type `started`, and Talk moves room state to recording video (`1`).

This satisfies the requirement boundary: the simulator calls Nextcloud Talk's recording endpoint, not the Cassini operator API.

### N1-Q5 — Recording from the start of playback

The recording API requires an active call before `POST /recording/{token}` succeeds, so the command cannot start recording before any call participant joins.

Best mechanism:

1. Authenticate the recording-starter participant for the selected private conversation.
2. Join the conversation/session as that participant.
3. Join the call with `flags=7` to activate the call.
4. Immediately call `POST /recording/{token}` with `status=1` as that same participant.
5. Poll `GET /ocs/v2.php/apps/spreed/api/v4/room/{token}` until `callRecording == 1` (recording video), with a bounded timeout.
6. Only then start/unmute the media-publishing bots.
7. After playback exits or fails, stop recording with `DELETE /recording/{token}` and leave the preflight call/session through Nextcloud APIs.

This gives a small silent pre-roll in the call but avoids losing the fixture's opening speech. It also converts "recording from the start" into a concrete implementation rule: media starts only after Talk reports recording active.

Implementation options:

- More integrated: add a recording gate inside the private rotator path. The first authenticated bot joins the call and starts recording before any bot starts WebRTC/media.
- Selected for slicing: `play-private` performs an OCS preflight join/start-recording with the recording starter, waits for `callRecording == 1`, then launches the authenticated rotator. Cleanup stops recording and leaves the preflight session after the player exits. This may create a short silent pre-roll, but it is API-simple and gives the strongest guarantee that media starts after recording is active.

### N1-Q6 — Implementation changes needed

Minimum implementation changes:

1. **New command surface**
   - Add `cassini dev play-private`.
   - Support `--conversation synthetic|admin`.
   - Support `--scaffold-only` to run the scaffold and exit.
   - Keep `cassini dev play` unchanged for public room playback except for the Pied Piper fixture swap if we choose to share the descriptor.

2. **Scaffold state**
   - Ensure users:
     - `cassini-erlich` display `Erlich Bachman`
     - `cassini-monica` display `Monica Hall`
   - Use `CASSINI_PLAY_SCAFFOLD_PASSWORD` or a documented harness default.
   - Create one-to-one conversations:
     - synthetic: authenticated as `cassini-erlich`, invite `cassini-monica`
     - admin: authenticated as `admin`, invite `cassini-erlich`
   - Persist a runtime JSON file with user ids, display names, password source, conversation tokens, call URLs, fixture info, and Nextcloud base URL.

3. **Authenticated rotator support**
   - Add optional per-participant credentials to `harness/go-talk-rotator`.
   - Set Basic Auth on all OCS requests when credentials are present.
   - Keep one cookie jar per bot.
   - Skip `setGuestName` for authenticated users.
   - Keep existing guest behavior for public-room flows.

4. **Recording gate and cleanup**
   - Add private playback orchestration that starts recording via Nextcloud after call activation but before fixture media starts.
   - Poll room state until `callRecording == 1` (or fail with a clear timeout/error).
   - After playback exits or fails, stop recording and leave the preflight call/session through Nextcloud APIs.
   - Do not call `POST /jobs` or any Cassini operator endpoint directly.

5. **Pied Piper fixture use**
   - Ensure/use `harness/scenarios/synthetic-pied-piper.v1.json` and `harness/media/processed/synthetic-pied-piper-v1`.
   - For `--conversation synthetic`, use both first speakers (`erlich`, `monica`).
   - For `--conversation admin`, use first speaker media for `cassini-erlich`; admin starts/owns recording and remains silent.

---

## API sequence sketches

### Scaffold: synthetic conversation

```bash
# User creation can use occ like harness bootstrap.
# Conversation creation should use the creator participant's auth.
curl -sS -u "cassini-erlich:$PASSWORD" \
  -H 'OCS-APIRequest: true' \
  -H 'Accept: application/json' \
  -X POST "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room" \
  --data-urlencode 'roomType=1' \
  --data-urlencode 'invite=cassini-monica'
```

### Scaffold: admin conversation

```bash
curl -sS -u "$ADMIN_USER:$ADMIN_PASSWORD" \
  -H 'OCS-APIRequest: true' \
  -H 'Accept: application/json' \
  -X POST "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room" \
  --data-urlencode 'roomType=1' \
  --data-urlencode 'invite=cassini-erlich'
```

### Private call + recording gate

```bash
# 1. Join conversation/session as the recording starter.
curl -sS -u "cassini-erlich:$PASSWORD" -b erlich.cookie -c erlich.cookie \
  -H 'OCS-APIRequest: true' \
  -H 'Accept: application/json' \
  -X POST "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/room/$TOKEN/participants/active" \
  --data-urlencode 'force=true'

# 2. Join/activate call.
curl -sS -u "cassini-erlich:$PASSWORD" -b erlich.cookie -c erlich.cookie \
  -H 'OCS-APIRequest: true' \
  -H 'Accept: application/json' \
  -H 'Content-Type: application/json' \
  -X POST "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/call/$TOKEN" \
  --data '{"flags":7,"silent":false,"recordingConsent":true,"silentFor":[]}'

# 3. Start Nextcloud/Talk integrated recording.
curl -sS -u "cassini-erlich:$PASSWORD" -b erlich.cookie -c erlich.cookie \
  -H 'OCS-APIRequest: true' \
  -H 'Accept: application/json' \
  -X POST "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v1/recording/$TOKEN" \
  --data-urlencode 'status=1'

# 4. Poll GET /room/$TOKEN until ocs.data.callRecording == 1.
# 5. Launch authenticated media bots only after that condition passes.
# 6. After playback exits or fails, stop through Nextcloud and leave preflight session.
curl -sS -u "cassini-erlich:$PASSWORD" -b erlich.cookie -c erlich.cookie \
  -H 'OCS-APIRequest: true' \
  -H 'Accept: application/json' \
  -X DELETE "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v1/recording/$TOKEN"

curl -sS -u "cassini-erlich:$PASSWORD" -b erlich.cookie -c erlich.cookie \
  -H 'OCS-APIRequest: true' \
  -H 'Accept: application/json' \
  -H 'Content-Type: application/json' \
  -X DELETE "$NEXTCLOUD_URL/ocs/v2.php/apps/spreed/api/v4/call/$TOKEN" \
  --data '{"all":false}'
```

---

## Spike conclusion

The preferred shape is supported by documented and source-confirmed Nextcloud Talk APIs.

Requirements impact:

- R2/R3 are supported by one-to-one room creation (`roomType=1&invite=<user>`), with idempotent existing-room behavior.
- R4 is supported by the existing rotator mechanics once OCS requests can authenticate per bot.
- R5 is supported by `POST /ocs/v2.php/apps/spreed/api/v1/recording/{token}`; this calls the configured Talk recording backend from Nextcloud and does not require the simulator to call the operator API.
- R6 is supported by a scaffold runtime state file plus `cassini dev play-private --conversation ...`.

Main implementation risks:

- Timing: `POST /recording/{token}` only moves Talk to a starting state; the recorder becomes active after the backend callback. The implementation should poll `callRecording == 1` before starting media to avoid clipping the beginning of the fixture.
- Cleanup: the preflight recording-starter session must not keep the call artificially active after playback. The implementation should stop recording and leave that starter call/session through Nextcloud APIs.

Recommended next step:

- Breadboard/slice the selected shape around: fixture swap, scaffold, authenticated private rotator, recording gate, cleanup, and validation.
