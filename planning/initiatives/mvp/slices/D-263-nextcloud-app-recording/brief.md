---
shaping: true
---

# D-263 Brief — Native Talk recording UX with Cassini backend

## What this slice is

D-263 now covers the first Nextcloud-facing productization step for Cassini under a different integration choice:

- keep Nextcloud Talk's native recording UX
- configure Cassini as the recording backend behind that UX
- keep Cassini as the recording and downstream artifact owner
- test the backend lifecycle separately from real browser/WebRTC media capture
- avoid building a custom Talk meeting button

This is not the custom-trigger path anymore.

For D-263 we are explicitly choosing the "Native Talk UX + Cassini backend" direction.

## Initial request from Linear

The original user need was:

- install/configure something in Nextcloud
- let moderators start recording from the meeting UI
- have Cassini handle the actual recording and downstream pipeline

The revised interpretation is:

- use Talk's existing `Start recording` / `Stop recording`
- configure Talk so that those actions target Cassini as the recording backend
- adapt Cassini to the Talk recording-backend contract rather than adding a parallel UX

## Why this matters

Cassini already has the differentiated part of the product:

- live meeting capture
- portable meeting artifact generation
- transcription
- viewer-friendly output
- publish / inspect flows

What it does not yet have is a clean native Nextcloud integration boundary that feels normal to a Talk user.

Using the existing Talk recording UX matters because it:

- removes the brittle custom in-call button problem
- aligns with user expectations inside Talk
- reduces the amount of app-specific UI we need to ship and maintain

## The problem we are solving

We need a Nextcloud integration shape that satisfies all of the following:

1. Cassini remains the recording owner
   - Cassini still performs the actual room recording work
   - Cassini still produces its downstream artifacts
   - Cassini is adapted to Talk's recording-backend lifecycle

2. Talk remains the operator surface
   - the moderator uses the native Talk recording controls
   - no custom meeting button is required
   - the setup feels like standard Talk recording configuration

3. The solution stays explainable against supported Nextcloud/Talk surfaces
   - use the existing Talk recording backend model
   - prefer admin configuration and backend protocol compatibility over DOM shims

## Selected implementation direction

The selected direction for D-263 is:

- make Cassini behave like a Talk-compatible recording backend
- keep Talk's native recording UX as the moderator-facing control surface
- use admin configuration to point Talk at the Cassini backend URL and shared secret
- keep Cassini recording outside Nextcloud, on the already deployed external server
- implement the Talk-specific backend adapter inside `cassini-operator`

This means the Nextcloud-facing work is primarily:

- admin configuration
- backend reachability / health checks
- Talk recording-backend configuration
- backend deployment guidance that matches Talk's expectations

Cassini remains responsible for:

- receiving Talk start/stop recording requests
- starting and stopping the actual room recording flow
- producing its normal downstream outputs

## Why this direction

We previously shaped a custom Nextcloud app + Talk shim + signed trigger route path.

That path would have worked, but it carried unnecessary cost:

- custom Talk DOM integration
- custom moderator trigger UX
- a parallel control-plane surface next to the native Talk recording product

The new direction reuses the product surface Talk already has.

So the relevant integration is no longer:

- "start Cassini recording from a custom Nextcloud button"

It is:

- "let Talk's native record button drive Cassini through the recording-backend protocol"

## Expected Nextcloud shape

The cleanest supported surfaces now appear to be:

### 1. Talk recording backend configuration

Use Talk's existing recording backend model so that:

- native `Start recording` / `Stop recording` remain the moderator controls
- Talk sends signed backend requests to Cassini
- Cassini returns status and stored recording results through the expected backend flow

Important operational note from the official `nextcloud-talk-recording` reference:

- the recording backend is expected to be an **HTTP API**, with TLS typically terminated by a reverse proxy
- the official server also expects the **standalone Talk signaling server** to exist

For D-263 this means we should assume:

- Cassini's Talk adapter will be exposed as an external HTTP backend behind normal reverse-proxy/TLS setup
- "recording button appears in Talk" is downstream of proper Talk recording-backend configuration and the broader Talk deployment prerequisites

### 2. Minimal app or admin integration

There are two realistic implementation shapes:

- preferred for smallest scope: rely on existing Talk admin configuration if it is sufficient
- optional thin Nextcloud app: provide admin setup guidance, health checks, and later viewer integration

This is now the main D-263 product simplification:

- the frontend/UI risk moves down sharply
- the backend protocol-compatibility work becomes the main task

## Proposed user flow

1. Nextcloud admin enables/configures Talk recording backend settings to point to Cassini.
2. Admin validates that Cassini is reachable and correctly configured.
3. Moderator opens a Talk meeting.
4. Moderator clicks Talk's native `Start recording`.
5. Talk sends a signed recording-backend start request to Cassini.
6. Cassini starts its recording flow for that room.
7. Moderator clicks Talk's native `Stop recording`, or Talk otherwise ends the session.
8. Talk sends the matching stop request to Cassini.
9. Cassini finalizes the recording and integrates with the expected Talk recording-backend lifecycle.

## In scope

### 1. Talk recording-backend compatibility

- receive Talk recording backend start/stop requests
- verify Talk's request signing/auth scheme
- map Talk room identity into Cassini recording execution
- report backend status in the expected shape

### 2. Cassini backend control adaptation

- adapt the existing Cassini recording control boundary to Talk-driven lifecycle events
- preserve Cassini's downstream artifact pipeline
- define how Talk-facing recording result handling and Cassini artifacts relate

### 3. Admin setup / health

- define the required Cassini backend URL and secret setup
- support a backend health/readiness check
- document what "correctly configured" means for operators

## Explicitly out of scope

- custom Talk DOM injection
- custom moderator recording button
- custom Nextcloud meeting trigger route as the primary control path
- switching to "Talk records `.webm`, Cassini post-processes it"
- full viewer embedding inside Nextcloud
- complete artifact browser inside the Nextcloud app

## Repo signal that already helps

The current repo already gives D-263 the most important backend capability:

- Cassini already knows how to record a Talk meeting from room URL context
- `cassini-operator` already provides a control-plane surface for starting recording work
- Cassini already produces downstream artifacts after recording

Relevant code and docs already in repo:

- `README.md`
- `cassini-go-recorder/internal/nextcloud/call_url.go`
- `cassini-go-recorder/internal/nextcloud/ocs_client.go`
- `cassini-go-recorder/internal/talk/recorder.go`
- `cassini-operator/README.md`

So D-263 should not redesign recording itself.
It should focus on the Talk recording-backend boundary and Cassini compatibility with it.

## Nextcloud docs and ecosystem references

The following official references matter most for this revised D-263:

### Talk recording backend

- Nextcloud Talk recording docs:
  - https://nextcloud-talk.readthedocs.io/en/stable/recording/
- Official recording server repository:
  - https://github.com/nextcloud/nextcloud-talk-recording
- Recording server installation overview:
  - https://portal.nextcloud.com/article/Nextcloud-Talk/Recording-Server/Installation
- High-performance backend installation overview:
  - https://portal.nextcloud.com/article/Nextcloud-Talk/High-Performance-Backend/Installation-of-Nextcloud-Talk-High-performance-backend

### Talk integration

- Nextcloud developer manual, Talk integration:
  - https://docs.nextcloud.com/server/stable/developer_manual/digging_deeper/talk.html

### App settings / optional thin app

- Nextcloud developer manual, settings:
  - https://docs.nextcloud.com/server/stable/developer_manual/basics/setting.html

### HTTP client

- Nextcloud developer manual, HTTP client:
  - https://docs.nextcloud.com/server/27/developer_manual/digging_deeper/http_client.html

## Main shaping answers for D-263

1. **Talk owns the recording UX**
   - D-263 does not add a custom moderator recording button.

2. **Cassini remains the recording backend**
   - D-263 does not switch capture over to Talk's `.webm` output path.

3. **Backend protocol compatibility is now the main work**
   - The central question is whether Cassini cleanly implements the Talk recording-backend lifecycle.

4. **UI work is no longer the risky part**
   - The major risk has moved from DOM integration to backend contract adaptation.

5. **Initial success criterion is native triggerability**
   - A moderator can use Talk's existing record controls and Cassini responds correctly as the backend.

6. **The official recording server is a protocol reference, not an architecture template**
   - We should copy its Talk-facing auth/callback/upload behavior, but keep Cassini's own recorder/runtime model.

## What “done” should look like

A reviewer should be able to:

1. configure Talk to use Cassini as a recording backend
2. validate Cassini backend reachability/auth
3. open a Talk meeting as a moderator
4. click Talk's native `Start recording`
5. confirm Cassini starts recording that room
6. click Talk's native `Stop recording`
7. confirm Cassini finalizes the recording flow correctly

## Test strategy

D-263 needs two different test shapes because they answer different questions.

The local manual acceptance test is the full product proof:

1. start Nextcloud, HPS/signaling, and Cassini services locally
2. join a Talk call through a browser
3. start recording through Talk's native recording control, or by enabling automatic recording when the meeting starts
4. stop recording explicitly, or leave the call and let room-empty auto-stop end the recording
5. confirm Cassini operator stops, finalizes, processes, publishes, and the meeting is visible in Cassini viewer

This manual path is where media failures such as `no remuxable streams found in session artifact` belong. That error means the lifecycle reached Cassini, but the real browser/signaling/WebRTC/remux path did not produce usable media streams.

The automated Nextcloud integration test should be narrower:

1. create a Talk room in a real local Nextcloud test stack
2. start recording through the Talk recording integration
3. ensure Cassini receives the signed backend start request and starts a recording job
4. avoid real media capture by using a fake/noop recorder executor or deterministic synthetic run artifact
5. stop recording through Talk
6. ensure Cassini receives the stop request, reports the expected callbacks, and the operator pipeline progresses

The automated test is not a substitute for the manual media acceptance test. It verifies that the Nextcloud/Talk recording-backend integration works without making WebRTC media capture a prerequisite for every integration run.

See `planning/initiatives/mvp/slices/D-263-nextcloud-app-recording/test-strategy.md` for the detailed test split.

## Likely code areas

### In this repo

- `planning/initiatives/mvp/slices/D-263-nextcloud-app-recording/`
- likely Cassini backend API adaptation code
- likely `cassini-operator` HTTP/runtime changes if the operator remains the backend entrypoint

### On the Cassini side

- existing record entrypoint and Talk join path
- new Talk recording-backend protocol surface
- possible status/store-response compatibility work

## Expected effort

**Effort: medium**

The front-end integration is much smaller now.
The main work is backend contract compatibility:

- start/stop request handling
- auth/signature verification
- room-to-recording mapping
- status/store lifecycle compatibility

## Open risks and blanks

### 1. Exact Talk recording-backend contract fit

We still need to prove whether Cassini can satisfy the Talk backend protocol cleanly without reshaping too much of its control model.

### 2. Store/result lifecycle

We still need to decide how Talk's expected stored recording output and Cassini's downstream artifact pipeline relate.

### 3. Optional app scope

We still need to decide whether D-263 requires a thin Nextcloud app at all, or whether Talk admin configuration is enough for the first cut.
