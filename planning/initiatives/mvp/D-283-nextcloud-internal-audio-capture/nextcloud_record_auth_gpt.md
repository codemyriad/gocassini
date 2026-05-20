# Nextcloud Talk recording auth investigation

## TL;DR

Cassini is currently joining Talk **as a guest/user participant**. That is why it works for public calls and breaks for restricted and 1:1 calls.

The official Nextcloud Talk recorder does **not** solve this by inviting a recorder user.
It solves it by joining as a **trusted internal service**:

1. **Nextcloud recording secret** authenticates the recorder to Nextcloud for that room token.
2. **HPB / signaling `internalsecret`** authenticates the recorder to the standalone signaling server as an **internal client**.
3. The internal client joins the signaling room **without a Nextcloud session / participant invite**.
4. Internal clients are allowed to join rooms even if they were not invited, and the recording server is treated as a **hidden/internal participant**.

That is why the official recorder can work for restricted rooms and, by design, also for **1:1** rooms.

## What Cassini does today

Cassini’s current Talk bootstrap in `cassini-go-recorder/internal/talk/recorder.go` is guest-style:

- `GetRoom(...)`
- `MarkParticipantActive(...)`
- `SetGuestName(...)`
- fetch normal signaling settings
- signaling `hello` using normal `helloAuthParams`
- signaling room join with `sessionid`
- `JoinCall(...)`

Related code:

- `cassini-go-recorder/internal/talk/recorder.go`
- `cassini-go-recorder/internal/nextcloud/ocs_client.go`

This makes Cassini a normal call participant, so it is constrained by:

- guest access rules
- invite/participant rules
- 1:1 room membership rules

So the current “create recorder user and invite it” idea is the wrong layer for 1:1.

## How the official Nextcloud recorder authenticates

### 1) Moderator starts recording through Talk

In Talk, recording is started via the recording API:

- `POST /ocs/v2.php/apps/spreed/api/v1/recording/{token}`
- implemented in `nextcloud/spreed/lib/Controller/RecordingController.php`
- delegated by `nextcloud/spreed/lib/Service/RecordingService.php`

Talk then notifies the recording backend via signed HTTP:

- `nextcloud/spreed/lib/Recording/BackendNotifier.php`
- POST to recording server: `/api/v1/room/{token}`
- headers include:
  - `Talk-Recording-Backend`
  - `Talk-Recording-Random`
  - `Talk-Recording-Checksum`

The checksum is HMAC over `random + request-body` with the shared **recording backend secret**.

### 2) Recording server validates Nextcloud

Official recorder server:

- repo: `nextcloud/nextcloud-talk-recording`
- request handling in `src/nextcloud/talk/recording/Server.py`

It validates:

- backend URL is allowed
- recording secret for that backend matches
- checksum matches

So only a trusted Nextcloud server can tell it to start/stop.

### 3) Recorder loads the special recording page

Official recorder service:

- `src/nextcloud/talk/recording/Service.py`
- `src/nextcloud/talk/recording/Participant.py`

It opens:

- `/index.php/call/{token}/recording`

This is a dedicated Talk route for recording:

- `nextcloud/spreed/src/router/router.ts`
- `nextcloud/spreed/src/mainRecording.js`

### 4) Recorder asks Nextcloud for signaling settings using recording auth

The browser page calls:

- `OCA.Talk.signalingGetSettingsForRecording(...)`
- implemented in `nextcloud/spreed/src/utils/webrtc/index.js`

This hits:

- `GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings?token={token}`

with headers:

- `Talk-Recording-Random`
- `Talk-Recording-Checksum`

On the Talk side, `nextcloud/spreed/lib/Controller/SignalingController.php` treats this as a **recording request** if those headers validate.

Important detail:

- for recording requests, Talk uses `getRoomByToken($token)`
- it does **not** require `getRoomForUserByToken(...)`

So this is not user/guest membership auth.
It is **trusted-service auth for a room token**.

### 5) Recorder authenticates to HPB as an internal client

After getting signaling settings, the official recorder does **not** use the normal user ticket flow.
Instead it builds:

```json
{
  "random": "...",
  "token": "HMAC(random, internalsecret)",
  "backend": "https://nextcloud.example.com"
}
```

Source:

- `nextcloud/nextcloud-talk-recording/src/nextcloud/talk/recording/Participant.py`

Talk frontend code then does:

- `settings.helloAuthParams.internal = internalClientAuthParams`
- `signalingJoinCallForRecording(...)`

Source:

- `nextcloud/spreed/src/utils/webrtc/index.js`

The standalone signaling server supports this `internal` client type.
Docs/code:

- `strukturag/nextcloud-spreed-signaling/docs/standalone-signaling-api-v1.md`
- `strukturag/nextcloud-spreed-signaling/server/hub.go`

Key upstream statement from signaling docs:

> Internal clients can join any room, even if they have not been invited.

And `server/hub.go` validates internal clients using only:

- `random`
- `token = HMAC(random, internalsecret)`
- `backend`

### 6) Recorder joins without a Nextcloud participant session

Upstream Talk code explicitly says:

- “No Nextcloud session ID is needed to join the room with an internal client”
- `nextcloud/spreed/src/utils/webrtc/index.js`

Also, before joining, it sends an internal `incall` message to reduce flags to plain `IN_CALL`, so others do not treat it as a normal publisher.

### 7) The recording server is treated as internal/hidden

Useful evidence in Talk UI/store code:

- `nextcloud/spreed/src/stores/session.ts`
- comment: `recording server joining as hidden participant`

So the recorder is not handled like a normal invited room member.

## Why this solves restricted rooms and 1:1

Because the official recorder is **not joining as a conversation participant**.
It is joining as a **trusted internal signaling client**.

That means:

- no guest admission needed
- no invite needed
- no “extra participant slot” problem in 1:1
- no need to add a recorder user to the room

## Is the official recorder able to record 1:1 calls?

### Conclusion

**Yes, by design, it should be able to record 1:1 calls.**

### Evidence

1. **Internal clients can join any room even if not invited**
   - signaling docs: `nextcloud-spreed-signaling/docs/standalone-signaling-api-v1.md`

2. **The recording server joins as hidden/internal**, not as a normal participant
   - `nextcloud/spreed/src/stores/session.ts`

3. **Recording UI is not excluded for one-to-one conversations**
   - `nextcloud/spreed/src/components/TopBar/TopBarMenu.vue`
   - recording actions are not gated out for 1:1

4. **1:1 participants are owners**
   - feature evidence: `nextcloud/spreed/tests/integration/features/command/user-transfer-ownership.feature`
   - one-to-one rooms show `participantType = 1` (owner)
   - the recording start API requires moderator/owner permissions

### Caveat

I did **not** find an explicit upstream automated test that says “record 1:1 call”.
So this is a **code-backed conclusion**, not a found e2e fixture.

## What the official recorder can do

From upstream docs/code/tests, the official recorder can:

- start/stop from Talk’s built-in recording flow
- record **video+audio** or **audio only**
- join as trusted internal service through HPB
- auto-stop when call ends / last participant leaves
- notify Talk of `started` / `stopped` / `failed`
- upload the final recording back to Nextcloud Files as the initiating owner
- trigger transcript/summary follow-up in Nextcloud

Important limitation for Cassini:

- it records the **rendered browser session + mixed audio**
- it does **not** keep per-user RTP/media streams the way Cassini does

So we should copy its **auth/join model**, not its capture model.

## Recommended Cassini solution

## Recommendation

Use the official recorder’s **trust chain**, but keep Cassini’s **existing RTP capture/remux pipeline**.

### Minimal technical change

Add a second Talk auth mode to Cassini:

- `guest` (current)
- `recording-internal` (new)

In `recording-internal` mode:

1. Parse call URL -> `baseURL`, `roomToken`
2. **Do not** call:
   - `MarkParticipantActive`
   - `SetGuestName`
   - `JoinCall`
3. Fetch signaling settings via recording-auth request:
   - `GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings?token=<roomToken>`
   - signed with **recording secret**
4. Connect to HPB websocket
5. Send signaling `hello` as **client type `internal`** using **signaling `internalsecret`**
6. Send internal `incall = IN_CALL` update
7. Join room with signaling `room` message using only `roomid` (no Nextcloud session id)
8. Keep Cassini’s existing subscriber / `requestoffer` / answer / RTP capture logic unchanged

### Why this is enough

Cassini already knows how to:

- discover remote sessions
- request offers
- answer them
- receive remote tracks
- persist per-user RTP
- remux locally

The piece that needs changing is only the **bootstrap/auth path**.

## Best product integration for `cassini-operator`

The cleanest product-level solution is:

### Make `cassini-operator` act as a Talk recording backend

Implement the same API surface as the official recorder server:

- `GET /api/v1/welcome`
- `POST /api/v1/room/{token}`

Validate:

- `Talk-Recording-Backend`
- `Talk-Recording-Random`
- `Talk-Recording-Checksum`

Then:

- on `start`: create operator job using `recording-internal` auth mode
- on `stop`: stop the running job
- send callbacks back to Talk:
  - `started`
  - `stopped`
  - `failed`

This gives you:

- the same access model as official Talk recording
- native support for restricted + 1:1 rooms
- Talk-side audit/system messages/status
- no manual copy-link workflow
- no fake recorder user

### Why this is better than only adding secrets to manual jobs

If you only add internal auth to the current manual “copy link to Cassini” flow, Cassini becomes a privileged service that can record any known room token.
That is technically fine, but you lose Talk-native authorization semantics.

If the operator speaks the official recording backend API, then recording is still initiated from Talk by an allowed moderator/owner, which is the better trust model.

## Suggested Cassini changes

### 1) Recorder config

Add deployment config for:

- Nextcloud **recording secret**
- signaling server **internalsecret**

For multi-backend deployments, follow the same model as the official recorder:

- map `Nextcloud base URL -> recording secret`
- map `signaling URL -> internalsecret`

Also normalize signaling URLs like upstream does (`https://...` ↔ `wss://...`).

### 2) `internal/nextcloud`

Add a method like:

- `FetchSignalingSettingsForRecording(ctx, roomToken, recordingSecret)`

Behavior:

- GET `/ocs/v2.php/apps/spreed/api/v3/signaling/settings?token=<roomToken>`
- signed with `Talk-Recording-Random` / `Talk-Recording-Checksum`

### 3) `internal/talk/recorder.go`

Split bootstrap into two paths:

- current guest bootstrap
- new internal recording bootstrap

New path should:

- skip participants/active lifecycle entirely
- use internal signaling hello
- send `internal/incall`
- join signaling room without `sessionid`
- skip OCS call join/leave

### 4) `internal/signaling/client.go`

No major redesign needed.
It already sends raw JSON requests/responses.
You mainly need new request builders for:

- `hello` with `auth.type = "internal"`
- `internal/incall`
- `room` join without `sessionid`

### 5) `cassini-operator`

Recommended additions:

- optional Talk recording-backend compatibility API
- per-backend secret config
- per-job metadata:
  - auth mode
  - backend URL
  - initiating actor
  - owner

## Exact auth flow Cassini should mimic

### To Nextcloud (recording-auth signaling settings)

```text
GET /ocs/v2.php/apps/spreed/api/v3/signaling/settings?token=<roomToken>
Headers:
  Talk-Recording-Random: <random>
  Talk-Recording-Checksum: HMAC_SHA256(recordingSecret, random)
```

(`getSettings()` validates against empty body for recording requests.)

### To HPB (internal hello)

```json
{
  "type": "hello",
  "hello": {
    "version": "2.0",
    "auth": {
      "type": "internal",
      "url": "https://nextcloud.example.com/ocs/v2.php/apps/spreed/api/v3/signaling/backend",
      "params": {
        "random": "...",
        "token": "HMAC_SHA256(internalSecret, random)",
        "backend": "https://nextcloud.example.com"
      }
    }
  }
}
```

### After hello

```json
{
  "type": "internal",
  "internal": {
    "type": "incall",
    "incall": {
      "incall": 1
    }
  }
}
```

Then join room:

```json
{
  "type": "room",
  "room": {
    "roomid": "<roomToken>"
  }
}
```

After that, Cassini can continue with its current `requestoffer`/subscriber flow.

## Bottom line

The fix is **not** “invite the recorder user differently”.
The fix is to stop joining as a user/guest participant at all.

Cassini should join the Talk call the same way the official recorder does:

- **trusted recording backend** to Nextcloud
- **trusted internal client** to HPB/signaling
- **hidden/non-invited internal room observer**
- keep Cassini’s own RTP capture/remux pipeline

That will solve:

- restricted rooms
- rooms that do not accept guests
- **1:1 calls**

while preserving Cassini’s per-user stream capture design.

## Sources consulted

### Cassini

- `cassini-go-recorder/internal/talk/recorder.go`
- `cassini-go-recorder/internal/nextcloud/ocs_client.go`
- `cassini-operator/internal/operator/record_runtime.go`

### Nextcloud Talk

- `nextcloud/spreed/lib/Controller/RecordingController.php`
- `nextcloud/spreed/lib/Controller/SignalingController.php`
- `nextcloud/spreed/lib/Recording/BackendNotifier.php`
- `nextcloud/spreed/lib/Service/RecordingService.php`
- `nextcloud/spreed/src/utils/webrtc/index.js`
- `nextcloud/spreed/src/mainRecording.js`
- `nextcloud/spreed/src/router/router.ts`
- `nextcloud/spreed/src/stores/session.ts`
- `nextcloud/spreed/src/components/TopBar/TopBarMenu.vue`
- `nextcloud/spreed/docs/recording.md`
- `nextcloud/spreed/tests/integration/features/callapi/recording.feature`
- `nextcloud/spreed/tests/integration/features/command/user-transfer-ownership.feature`

### Official recorder

- `nextcloud/nextcloud-talk-recording/src/nextcloud/talk/recording/Server.py`
- `nextcloud/nextcloud-talk-recording/src/nextcloud/talk/recording/Service.py`
- `nextcloud/nextcloud-talk-recording/src/nextcloud/talk/recording/Participant.py`
- `nextcloud/nextcloud-talk-recording/src/nextcloud/talk/recording/BackendNotifier.py`
- `nextcloud/nextcloud-talk-recording/src/nextcloud/talk/recording/Config.py`
- `nextcloud/nextcloud-talk-recording/docs/recording-api.md`
- `nextcloud/nextcloud-talk-recording/docs/installation.md`

### Standalone signaling server

- `strukturag/nextcloud-spreed-signaling/docs/standalone-signaling-api-v1.md`
- `strukturag/nextcloud-spreed-signaling/server/hub.go`
- `strukturag/nextcloud-spreed-signaling/server.conf.in`

## Addendum: validating Talk media modes and implementation coverage

## Short answer

- Your assumption is **partly wrong**.
- Without HPB, Talk media does **not** flow through the signaling server.
- The signaling layer carries **offers / answers / ICE candidates / control messages**.
- The media layer is still **WebRTC media**:
  - **direct peer-to-peer**, or
  - **relayed by TURN** when direct connectivity fails.
- There is also **no general automatic runtime fallback** of “HPB down => switch to internal P2P mode”. Signaling mode is configured separately.

## The relevant Talk modes

There are really **two axes** to keep separate:

1. **signaling mode**
2. **media topology**

### A) Internal signaling + P2P media

This is the “no HPB configured” mode.

Evidence:

- Talk settings expose `signaling_mode = internal | external | conversation_cluster` and document `internal` as the mode used when no HPB is configured:
  - `nextcloud/spreed/docs/settings.md`
- The frontend chooses signaling implementation from `settings.signalingMode`:
  - `nextcloud/spreed/src/utils/signaling.js`
- Talk docs describe the non-HPB baseline as **peer-to-peer**:
  - `nextcloud/spreed/docs/scalability.md`
  - `nextcloud/spreed/docs/TURN.md`

Media behavior in this mode:

- best case: **browser <-> browser direct WebRTC**
- fallback: **browser <-> TURN <-> browser**

Notably:

- **no standalone signaling server is involved**
- **no HPB WebRTC gateway is involved**
- **the signaling path is not the media path**

### B) External / standalone signaling + P2P media

This is possible when the standalone signaling server is used **without MCU/SFU functionality**.

Evidence:

- the standalone signaling server config says MCU functionality can be disabled by leaving `[mcu]` empty:
  - `strukturag/nextcloud-spreed-signaling/server.conf.in`
- the standalone signaling API has generic **client-to-client signaling messages** for WebRTC offers/answers/candidates:
  - `strukturag/nextcloud-spreed-signaling/docs/standalone-signaling-api-v1.md`
- the Talk frontend branches on `signaling.hasFeature('mcu')`; without MCU it creates direct peers instead of using the subscriber/requestoffer path:
  - `nextcloud/spreed/src/utils/webrtc/webrtc.js`
  - `nextcloud/spreed/src/utils/webrtc/simplewebrtc/simplewebrtc.js`

Media behavior in this mode:

- best case: **browser <-> browser direct WebRTC**
- fallback: **browser <-> TURN <-> browser**

Again:

- the standalone signaling server transports **signaling**, not RTP media

### C) External / standalone signaling + HPB / SFU media

This is the normal “HPB” case.

Evidence:

- Talk docs describe HPB as signaling server + WebRTC gateway / SFU:
  - `nextcloud/spreed/docs/developer-setup.md`
  - `nextcloud/spreed/docs/scalability.md`
- the signaling server README is explicit:
  - signaling server hides Janus details from clients
  - **only WebRTC media is exchanged directly between the gateway and the clients**
  - source: `strukturag/nextcloud-spreed-signaling/README.md`
- the standalone signaling API has an SFU/MCU-specific publish/subscribe flow:
  - `strukturag/nextcloud-spreed-signaling/docs/standalone-signaling-api-v1.md`

Media behavior in this mode:

- best case: **client <-> HPB WebRTC gateway (Janus/proxy)**
- fallback: **client <-> TURN <-> HPB WebRTC gateway**

Important correction to the original assumption:

- even here, media does **not** flow through the signaling server websocket
- it flows through the **WebRTC gateway / SFU**

## TURN does not make signaling a media proxy

A useful mental model is:

- **signaling server / signaling API** = session discovery + SDP + ICE + control
- **TURN** = network relay for media when peers/gateway cannot connect directly
- **HPB WebRTC gateway / SFU** = media hub in centralized mode

So the non-HPB fallback is **not** “media through signaling server”.
It is **media through direct peer connections, maybe TURN-relayed**.

## Does the same Cassini implementation cover all of these?

## Short answer

**No.**

The planned “Nextcloud internal recording” bootstrap does **not** by itself cover all Talk modes, and **Cassini’s current capture logic is only a clean fit for HPB/SFU mode**.

## Coverage by mode

### 1) Internal signaling + P2P media

**Not covered** by the planned upstream-like internal recorder approach.

Why:

- the official recording server explicitly requires the **standalone signaling server**:
  - `nextcloud/nextcloud-talk-recording/README.md`
- the upstream internal-client auth (`hello.auth.type = internal`) exists on the **standalone signaling server**, not on Talk’s internal signaling mode:
  - `strukturag/nextcloud-spreed-signaling/docs/standalone-signaling-api-v1.md`
  - `strukturag/nextcloud-spreed-signaling/server/hub.go`
- Cassini’s current recorder code expects the standalone signaling / subscriber flow:
  - `cassini-go-recorder/internal/talk/recorder.go`

Implication:

- if a Talk deployment is running in **internal signaling mode**, the proposed trusted-internal recorder join path is **not available**
- Cassini would need a **different implementation** for that mode

That different implementation would need to emulate Talk’s **direct peer** behavior rather than SFU subscription behavior.

### 2) External signaling + P2P media

**Auth bootstrap:** probably yes

**Current Cassini capture logic:** no

Why:

- internal-client auth is available on the standalone signaling server, so the **join/auth path** should work
- but Cassini currently captures by creating subscriber peers and sending `requestoffer`
- upstream Talk code is explicit that `requestOffer` only works **with MCU**:
  - `nextcloud/spreed/src/utils/signaling.js`
  - it literally logs: `Can't request an offer without a MCU.`

Implication:

- if the deployment uses standalone signaling but **no MCU/SFU**, Cassini cannot keep the current `requestoffer`-based media capture unchanged
- it would need to implement the **direct peer offer/answer strategy** used by Talk browsers in non-MCU mode

So in this mode:

- **the auth model can still be copied from the official recorder**
- **the capture model cannot** stay as-is

### 3) External signaling + HPB / SFU media

**This is the best fit and the main supported target for the current Cassini design.**

Why:

- internal-client auth works here
- the room join works here
- the current Cassini media model already matches the HPB subscriber flow:
  - discover sessions
  - `requestoffer`
  - receive remote tracks
  - capture per-user streams

This is also the mode most directly aligned with:

- `cassini-go-recorder/internal/talk/recorder.go`
- the upstream standalone signaling subscriber flow in `requestoffer`

## Important caveat: “HPB unavailable” is ambiguous

If by “HPB unavailable” you mean:

### “The deployment was configured without HPB”

Then Talk is in **internal signaling + P2P/TURN** mode.
That is a **different architecture**, and the current Cassini internal-recorder plan does **not** cover it.

### “The deployment is configured for external signaling/HPB, but the HPB is temporarily broken”

Then you should **not** assume a transparent fallback to internal signaling.

The frontend selects signaling implementation from `signalingMode`:

- `internal` => `Signaling.Internal`
- otherwise => `Signaling.Standalone`
- source: `nextcloud/spreed/src/utils/signaling.js`

So a broken external signaling / HPB deployment is more likely to:

- fail calls
- fail recording
- keep retrying / reconnecting in that mode

rather than switching itself to a completely different signaling stack.

## What this means for Cassini planning

## If your target is “record restricted and 1:1 meetings in normal enterprise Talk setups”

Then implementing:

- **recording-backend auth to Nextcloud**, plus
- **internal-client auth to standalone signaling**, plus
- the existing **subscriber/requestoffer capture**

is the right plan **for HPB/SFU deployments**.

## If your target is “record all Talk deployments, including no-HPB / pure P2P deployments”

Then this is **not enough**.
You would need at least one extra capture path:

- **internal signaling / direct-peer capture** for no-HPB deployments

and probably another compatibility path for:

- **standalone signaling without MCU**

In both of those cases, the core problem is that Cassini’s current media acquisition is **subscriber-based SFU capture**, while the deployment is doing **peer-to-peer media negotiation**.

## Practical conclusion

### What is true

- Talk can operate without HPB in **P2P** mode.
- TURN may relay media in both P2P and HPB cases.
- The signaling server is **not** the RTP/media transport.
- The official recording auth model is tied to **standalone signaling**.
- Cassini’s current capture logic is a good fit for **HPB/SFU** mode.

### What is not true

- “non-HPB media flows through the signaling server”
- “one internal-recorder implementation automatically covers every Talk topology”
- “if HPB is down Talk just falls back to the same thing but P2P”

## Final recommendation for scope

Treat the work as **two separate support levels**:

### Tier 1: supported first

- **External signaling + HPB/SFU**
- Use the official recorder auth model
- Keep Cassini’s current capture logic

### Tier 2: separate future slice

- **No HPB / internal signaling P2P**
- and possibly **external signaling without MCU**
- Requires a different media negotiation/capture strategy

That split best matches the upstream architecture and avoids overpromising “all Talk modes” coverage from a change that only solves the **auth bootstrap** side.
