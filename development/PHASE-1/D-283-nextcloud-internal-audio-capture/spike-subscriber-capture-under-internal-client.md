# X1 Spike: Subscriber capture under internal HPB client

## Context

D-283 is not trying to redesign Cassini's media pipeline.
The intended change is to replace the **guest-participant bootstrap** with the **trusted internal HPB/signaling bootstrap** used by Nextcloud Talk recording.

That means the core spike question is not "can Cassini still write RTP into session artifacts?"
Cassini already does that.
The real question is whether, after joining as an **internal client**, Cassini still gets the signaling surface it needs to drive the **existing subscriber/requestoffer capture path**.

This matters because the current shaping direction is:

- support **Nextcloud Talk with standalone signaling + HPB only**
- keep the new HPB-internal path **side-by-side** with the current guest path
- make the HPB-internal path the **default**
- preserve Cassini's current **per-participant capture**, **speaker-attributed downstream processing**, and **multi-track MKV** value

## Goal

Determine whether Cassini's existing subscriber/requestoffer capture flow can remain the media path when the recorder joins Nextcloud Talk as an **internal HPB client** instead of a normal guest participant.

## Current evidence

### What the current recorder actually depends on

From `cassini-go-recorder/internal/talk/recorder.go`:

1. The current bootstrap is guest/user style:
   - `GetRoom(...)`
   - `MarkParticipantActive(...)`
   - `SetGuestName(...)`
   - `FetchSignalingSettings(...)`
   - signaling `hello`
   - signaling room join with `sessionid`
   - `JoinCall(...)`

2. After bootstrap, the active media path is signaling-driven:
   - room / participants events reveal remote session ids
   - `ensureSubscriber(remoteSessionID)` creates a subscriber peer per remote session
   - Cassini sends `requestoffer`
   - remote participant returns `offer`
   - Cassini answers and receives tracks in `OnTrack(...)`
   - RTP/RTCP are written into session artifacts and later remuxed into multi-track MKV

3. The most critical dependency is **remote signaling session discovery**:
   - `handleRoomEvent(...)` and `handleParticipantsEvent(...)` are what feed remote session ids into `ensureSubscriber(...)`
   - without remote session ids, Cassini cannot start `requestoffer`
   - current code does **not** have an alternate sender-discovery path that bypasses room/participants events

4. The current Nextcloud participant session id looks bootstrap-only:
   - `nextcloudSessionID` is produced by `MarkParticipantActive(...)`
   - current recorder uses it to join the signaling room as a normal participant (`room.sessionid = r.nextcloudSessionID`)
   - after bootstrap, it is not part of the subscriber/requestoffer media loop
   - that is strong evidence that the main migration target is the join/auth path, not RTP capture itself

5. Participant identity is useful but not the main blocker:
   - if display name is missing, `ensureSessionCapture(...)` falls back to `participant-<shortid>`
   - that means missing names are a quality problem, but not automatically a capture blocker

### Initial dependency map

| Behavior | Current dependency | If internal client lacks it |
|----------|--------------------|-----------------------------|
| Join signaling room | `hello` response with `signalingSessionID`, then room join using `nextcloudSessionID` | must swap to internal `hello` + room join without `sessionid` |
| Discover remote sessions | room `join` and/or participants `update` events | subscriber creation stalls; `requestoffer` cannot start |
| Request remote media | remote signaling session id only | likely reusable if session discovery still works |
| Capture/write RTP | `OnTrack(...)` on subscriber peers | likely reusable unchanged |
| Label tracks with participant metadata | display name / participant id from room/participants events | capture can continue, but naming/metadata quality may degrade |
| Stop-on-room-empty behavior | subscriber add/remove events | still works if subscriber lifecycle still tracks remote sessions correctly |

### What the upstream auth investigation already suggests

From `nextcloud_record_auth_gpt.md`:

- the official recording path joins HPB as an **internal client**
- the internal client joins the signaling room **without** a Nextcloud participant session id
- internal clients can join rooms even if not invited
- for HPB/SFU mode, the recommended Cassini change is specifically to keep the current subscriber/requestoffer path and only swap the bootstrap/auth layer

### What remains unknown

The missing proof is whether an internal client in the targeted HPB setup still receives the **same enough signaling event surface** to support:

- remote session discovery
- `requestoffer`
- remote offers/candidates/tracks
- stable enough participant/session identity for useful downstream labeling

## Static exploration findings so far

### Finding 1 — the key unknown is narrower than it first looked

The static code read strengthens this split:

- **bootstrap concerns**
  - trusted signaling settings fetch
  - internal `hello`
  - internal `incall`
  - room join without `sessionid`
- **existing reusable media concerns**
  - remote session discovery
  - `requestoffer`
  - remote `offer` / `candidate`
  - `OnTrack(...)`
  - RTP/RTCP persistence and remux

So the hardest question is no longer “does internal mode require a new recorder architecture?”
It is much narrower:

> after internal join, does Cassini still see enough of the room/session signaling surface to discover remote sessions and start the existing subscriber flow?

### Finding 2 — `nextcloudSessionID` is not part of the media loop

Static read of `cassini-go-recorder/internal/talk/recorder.go` shows:

- `nextcloudSessionID` is created by `MarkParticipantActive(...)`
- it is used to join the signaling room as a normal participant
- after that, the capture loop depends on `signalingSessionID`, remote session ids, and signaling messages/events
- the track capture path itself does **not** use `nextcloudSessionID`

This is strong evidence that the migration target is still the **join/auth layer** first.

### Finding 3 — current recorder has exactly one active remote-session discovery path

Cassini currently learns remote session ids through:

- room `join` events
- participants `update` events
- subsequent signaling messages once a remote session is already known

Important implication:

- if internal join does **not** produce room/participants discovery events, current subscriber creation stalls
- the code does not currently contain a second proven discovery path that bypasses those events

### Finding 4 — signaling docs say the initial participant list comes as room events, not the room-join response

Source: `/tmp/nextcloud-spreed-signaling/docs/standalone-signaling-api-v1.md`

The standalone signaling docs show:

- the `room` join response contains room confirmation/properties/bandwidth
- the **initial list of users in the room** is delivered through subsequent room `join` events
- room `join` events include:
  - signaling `sessionid`
  - optional `userid`
  - `user` payload
  - `roomsessionid`
  - optional `features`

This matters because it weakens one earlier fallback idea:

- Cassini is **not** likely to recover remote session discovery from the room-join response alone
- the correct static expectation is that remote session discovery should continue to come from **room events** and **participants events**

### Finding 5 — signaling docs describe exactly the event fields Cassini needs

Source: `/tmp/nextcloud-spreed-signaling/docs/standalone-signaling-api-v1.md`

The docs explicitly describe:

- room `join` events carrying `sessionid` and `roomsessionid`
- participants `update` events carrying participant objects
- when a participant is in the call, the participant information includes both:
  - signaling session id (`sessionId`)
  - Nextcloud session id (`nextcloudSessionId`)
- `requestoffer` as the normal MCU subscriber message, addressed to a publisher session id

This is a strong static match to what Cassini currently consumes:

- Cassini primarily needs the **signaling session id** to create subscribers and send `requestoffer`
- `roomsessionid`, `userid`, and user/display metadata are valuable for labeling, but secondary to capture viability

### Finding 6 — Talk frontend source confirms the recording path uses internal hello + internal incall + joinRoom without a Nextcloud session id

Sources:

- `/tmp/nextcloud-spreed/src/utils/signaling.js`
- `/tmp/nextcloud-spreed/src/utils/webrtc/index.js`

Relevant behaviors in Talk source:

- if `settings.helloAuthParams.internal` is present, standalone signaling hello is sent with `auth.type = 'internal'`
- after hello, Talk rejoins the room when either a normal `nextcloudSessionId` exists **or** internal auth is present
- `signalingJoinCallForRecording(...)`:
  - injects `helloAuthParams.internal`
  - sends `internal/incall`
  - joins the room with `signaling.joinRoom(token)`
  - explicitly comments: **“No Nextcloud session ID is needed to join the room with an internal client”**
- `joinCall(...)` for internal clients short-circuits because the incall flags were already set during room join

This is strong source-level confirmation that the intended recording path is indeed:

- internal hello
- internal incall
- room join without Nextcloud session id
- then normal signaling/WebRTC setup on top

### Finding 7 — signaling server source strongly suggests internal clients can subscribe successfully in MCU mode

Sources:

- `/tmp/nextcloud-spreed-signaling/server/hub.go`
- `/tmp/nextcloud-spreed-signaling/server/clientsession.go`
- `/tmp/nextcloud-spreed/src/utils/signaling.js`

Relevant source evidence:

1. `hub.go` explicitly allows internal clients to join any room.
2. `hub.go` also explicitly returns `true` from `isInSameCall(...)` for internal clients:
   - comment: **“Internal clients may subscribe all streams.”**
3. `processMcuMessage(...)` handles `requestoffer` by creating/getting an MCU subscriber for the target publisher session.
4. Talk frontend source warns that `requestOffer` only works with MCU.

Combined read:

- in the supported **HPB/SFU** scope, the signaling server does not appear to block internal clients from using the subscriber flow
- this is stronger than “plausible”: static source says internal clients may subscribe all streams, and `requestoffer` is the MCU subscription path

### Finding 8 — signaling server tests show internal clients do receive join/participants signaling in rooms

Source:

- `/tmp/nextcloud-spreed-signaling/server/virtualsession_test.go`

The test coverage is not a Cassini-equivalent recorder test, but it is still highly relevant:

- an internal client joins a room successfully
- a normal client joins the same room
- both sides can observe joined sessions
- participants `update` events include the internal client session id and `inCall` state
- ordering can vary slightly, but the event surface exists

This is the strongest static evidence so far that internal clients are not hidden from the room/participants signaling surface in a way that would automatically break Cassini.

### Finding 9 — Talk UI/store code expects the recording participant to rely on signaling data, not Nextcloud peer fetches

Sources:

- `/tmp/nextcloud-spreed/src/components/CallView/CallView.vue`
- `/tmp/nextcloud-spreed/src/stores/session.ts`

Relevant comments in Talk source:

- the recording participant has **no Nextcloud session**, so it cannot fetch peers
- Talk expects the needed recording data to become available from **signaling data**
- the recording server may appear as a hidden participant without normal Nextcloud-session-backed data

This reinforces the expected data model for Cassini internal mode:

- do not depend on Nextcloud participant-session lifecycle
- do depend on signaling room/participants/message data

### Provisional X1 status

| Question | Static status | Current read |
|---|---|---|
| **X1-Q1** | Mostly answered | Room/participants events are the critical discovery surface; participant names/ids improve metadata quality but are secondary to capture viability. |
| **X1-Q2** | Strongly supported by docs/source | Signaling docs say initial room membership is delivered through room events, and signaling-server tests show internal clients do receive join/participants signaling in rooms. |
| **X1-Q3** | Strongly supported in HPB/SFU scope | `requestoffer` is the MCU subscriber path, and signaling-server source explicitly says internal clients may subscribe all streams. |
| **X1-Q4** | Partially answered | Room `join` events and participants updates can carry signaling session ids, room session ids, user ids, and user payloads; hidden/internal cases may still reduce labeling quality in some paths. |
| **X1-Q5** | Reframed | The room-join-response fallback now looks unlikely; if discovery still fails in practice, the gap would more likely be event timing/payload handling than missing room state in the join response. |

## Questions

| # | Question |
|---|----------|
| **X1-Q1** | Which current recorder behaviors are strictly dependent on room / participants signaling events, and which are only nice-to-have metadata enrichment? |
| **X1-Q2** | After an internal-client `hello` + internal `incall` + room join without `sessionid`, does the recorder still receive the remote session discovery events needed to create subscribers? |
| **X1-Q3** | In the supported HPB setup, can an internal client still use the existing `requestoffer` → `offer` → `answer` → `OnTrack` flow against normal participant sessions? |
| **X1-Q4** | If the internal client capture path works, what participant identity fields are still available for session artifact labeling and multi-track output metadata? |
| **X1-Q5** | If the internal client does **not** receive the current room / participants discovery surface, what is the minimum additional mechanism Cassini would need to discover remote sessions without redesigning the whole capture pipeline? |

## Acceptance

Spike is complete when:

- we can describe exactly what parts of the current recorder are bootstrap-only versus signaling-surface dependent
- we can describe whether an internal HPB client can reuse the current subscriber/requestoffer capture path as-is, reuse it with small event-handling changes, or cannot reuse it cleanly
- we can identify the concrete recorder touchpoints that would change if we proceed with the HPB-internal shape
- we can call out any follow-up spikes needed before implementation
