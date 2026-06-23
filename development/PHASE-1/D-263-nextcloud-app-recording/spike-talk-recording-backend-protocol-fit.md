## D-263 Spike: Talk recording-backend protocol fit

### Context

The revised D-263 architecture is now:

- keep Talk's native recording UX
- make Cassini the recording backend behind that UX
- preserve Cassini-owned live capture and downstream artifacts

That moves the main technical seam away from custom Nextcloud UI and onto one backend question:

- **how well does the Talk recording-backend protocol fit Cassini's current control model**

This matters because Cassini already has:

- an existing live Talk recording path
- an operator runtime that starts jobs from a generic trigger API

But Talk's recording backend does not drive a generic trigger API.
It drives a very specific start/stop/store/status lifecycle.

### Goal

Compare the current Talk recording-backend contract to Cassini's current control/runtime shape and decide:

- whether `cassini-operator` can serve that contract directly
- whether a thinner Talk-specific backend adapter is needed
- where the hard mismatches are
- which seam is likely to be easiest to implement first

### Sources used

- Talk recording API docs:
  - https://nextcloud-talk.readthedocs.io/en/stable/recording/
- Talk backend notifier source:
  - `nextcloud/spreed/lib/Recording/BackendNotifier.php`
- Talk recording backend admin settings source:
  - `nextcloud/spreed/src/components/AdminSettings/RecordingServers.vue`
- Talk recording backend config source:
  - `nextcloud/spreed/lib/Config.php`
- Current repo:
  - `cassini-operator/README.md`
  - `cassini-operator/internal/operator/run.go`
  - `cassini-operator/internal/operator/record_runtime.go`

### Outcome

This spike is complete enough to lock the protocol-fit conclusion.

Selected conclusion for D-263:

- **Talk should not talk directly to the current generic `POST /jobs` operator surface**
- **Cassini needs a Talk-specific backend adapter surface**
- **That adapter can still delegate into the existing operator/runtime or equivalent internal control path**
- **The hardest mismatch is not start/stop; it is the result/store/callback lifecycle**

Put more concretely:

- `cassini-operator` is close to the right execution engine
- it is not currently shaped like the Talk recording backend contract
- the cleanest path is a thin Talk-backend adapter in front of the existing Cassini execution model

## What Talk expects

### 1. Backend start/stop entrypoint

From `BackendNotifier.php`, Talk sends requests to:

- `POST /api/v1/room/{token}`

Body shape:

- start:
  - `{"type":"start","start":{"status":...,"owner":"...","actor":{...}}}`
- stop:
  - `{"type":"stop","stop":{"actor":{...}}}`

Auth/signature:

- headers:
  - `Talk-Recording-Random`
  - `Talk-Recording-Checksum`
  - `Talk-Recording-Backend`
- checksum:
  - HMAC-SHA256 over `random + body` using the Talk recording secret

This is not a generic JSON job-submission API.

### 2. Backend-to-Talk callbacks

Talk also exposes:

- `POST /ocs/v2.php/apps/spreed/api/v1/recording/backend`

For backend state reporting:

- `started`
- `stopped`
- `failed`

This means the recording backend is expected to notify Talk about lifecycle transitions.

### 3. Recording file store flow

Talk exposes:

- `POST /ocs/v2.php/apps/spreed/api/v1/recording/{token}/store`

For the recording backend to upload the recorded file back to Nextcloud.

That request requires:

- multipart file upload
- `owner`
- Talk recording auth headers:
  - `TALK_RECORDING_RANDOM`
  - `TALK_RECORDING_CHECKSUM`

So Talk's backend protocol is not only start/stop.
It also includes:

- callback notifications
- upload/store back into Nextcloud

## What Cassini currently has

### 1. A generic trigger API via `cassini-operator`

Current operator entrypoint:

- `POST /jobs?provider=nextcloud-talk`

Body shape:

- `platform`
- `url`
- optional `guestName`
- optional `duration`
- optional `stopWhenRoomEmpty`
- optional `roomEmptyGrace`

Current semantics:

- returns `202 {id}`
- creates a persisted job row
- starts asynchronous recording work

This is an operator/job API, not a Talk backend protocol surface.

### 2. Existing live recording execution path

Current operator runtime already knows how to:

- run `cassini doctor --target record`
- run `cassini record --call <url> --out ... --name ...`
- stop recording through operator-owned process control

This is useful, because it means the recording engine already exists.

### 3. No Talk recording-backend callbacks/store lifecycle

The current repo does not yet provide:

- `POST /api/v1/room/{token}` in Talk's expected shape
- Talk-signature verification using `random + body`
- backend `started` / `stopped` / `failed` callbacks to Talk
- `recording/{token}/store` upload-back flow

That is the real gap.

## Protocol-fit comparison

| Concern | Talk backend expects | Cassini currently has | Fit |
|--------|-----------------------|-----------------------|-----|
| Start request path | `POST /api/v1/room/{token}` | `POST /jobs?provider=nextcloud-talk` | ❌ |
| Start request body | `type=start` with `status`, `owner`, `actor` | generic trigger body with `url`, options | ❌ |
| Stop request path | same backend room endpoint with `type=stop` | `POST /jobs/:id/stop` by job id | ❌ |
| Request auth | `Talk-Recording-*` headers over `random + body` | generic operator auth is not implemented yet / previous D-263 spike proposed a different HMAC shape | ❌ |
| Room identity | room token in URL path | full Talk call URL in request body | ⚠️ |
| Async execution engine | backend starts real recording work | operator already does this | ✅ |
| Backend lifecycle callback | `started` / `stopped` / `failed` to Talk | not implemented | ❌ |
| Recording file store | upload recording back to Talk via OCS store endpoint | not implemented | ❌ |
| Downstream Cassini artifacts | not part of Talk protocol | already exists | ✅ |

## Main mismatches

### M1. Control identity mismatch: room token vs job id

Talk controls recording by room token:

- start room token
- stop room token

Current operator control identity is:

- create a job
- stop by job id

That means a direct passthrough from Talk to the current operator API would require an awkward translation layer anyway.

### M2. Start body mismatch

Talk sends:

- recording `status`
- `owner`
- `actor`

Current operator expects:

- full call URL
- guest/stop controls

These are not the same abstraction.

### M3. Result/store lifecycle mismatch

This is the hardest one.

Talk expects the backend to:

- upload the produced recording file back into Nextcloud
- notify Talk of started/stopped/failed lifecycle events

Cassini currently thinks in terms of:

- local recording artifacts
- build/publish pipeline
- operator job rows and attempt history

Those two worlds can coexist, but they do not line up automatically.

## Answered questions

| # | Decision | Answer |
|---|----------|--------|
| **D263-FIT1** | Can Talk use the current `POST /jobs` surface directly? | No. The protocol shape is too different. |
| **D263-FIT2** | Is the current operator still useful? | Yes. It is a plausible execution engine behind a Talk-specific adapter. |
| **D263-FIT3** | What is the easiest part of the Talk protocol to support? | Start/stop request acceptance and mapping into existing Cassini recording execution. |
| **D263-FIT4** | What is the hardest part? | The file-store and lifecycle-callback contract back into Talk. |
| **D263-FIT5** | Should D-263 force Talk to speak a Cassini-specific generic API? | No. Better to adapt Cassini to Talk's backend protocol than to bypass the native integration model again. |
| **D263-FIT6** | Should the Talk backend surface be the same as the current operator API? | No. Use a thin Talk-specific adapter surface. |

## Recommended shape

### Recommended D-263 backend boundary

Use:

- a **thin Talk-backend adapter**

That adapter should:

1. accept Talk's `POST /api/v1/room/{token}` requests
2. verify Talk's `Talk-Recording-*` auth scheme
3. translate start/stop requests into Cassini recording control actions
4. report `started` / `stopped` / `failed` back to Talk
5. upload/store the resulting recording file back to Talk

Behind that adapter:

- reuse the existing operator/runtime or equivalent Cassini execution path as much as possible

### Why this is better than reshaping Talk

This keeps the native product surface intact:

- Talk continues to think it has a normal recording backend
- Cassini adapts once at the backend edge
- internal Cassini execution can still reuse operator/runtime concepts

## Possible internal mappings

### Option A: Thin adapter in front of `cassini-operator`

Flow:

1. Talk `POST /api/v1/room/{token}` start
2. adapter verifies Talk auth
3. adapter derives full call URL from Talk backend base + token
4. adapter launches/creates internal recording work using operator/runtime primitives
5. adapter remembers `room token -> operator job id`
6. Talk stop request uses room token
7. adapter resolves job id and stops/finalizes it
8. adapter performs Talk callbacks/store upload

Pros:

- maximal reuse of existing operator runtime

Cons:

- requires a token-to-job mapping layer
- current operator stop path is job-id-centric, not room-token-centric

### Option B: Thin adapter in front of a more direct Cassini runtime control path

Flow:

1. Talk start/stop hits adapter
2. adapter drives a Cassini-specific in-memory/session runtime keyed by room token
3. result/store callbacks are handled in the same adapter/runtime boundary

Pros:

- lines up more naturally with Talk's room-token-centric lifecycle

Cons:

- less direct reuse of the current operator API shape

### Selected direction for first implementation

For D-263, the best first bet appears to be:

- **Option A unless the job-id/room-token translation becomes too awkward in practice**

Reason:

- the execution engine already exists in `cassini-operator`
- start/stop recording behavior already exists
- the remaining work is mostly protocol adaptation and mapping

But this should be tested quickly, because if room-token lifecycle management becomes unnatural, Option B may become cleaner.

## What this means for the current planning docs

The revised D-263 shape was correct to move away from the custom Talk button path.

But the current slice phrasing should now be interpreted more narrowly:

- not "make Talk talk to the existing operator API directly"
- but "make Cassini expose a Talk-compatible backend surface, likely backed by existing operator/runtime behavior"

## Acceptance

This spike is complete because it answers the main fit question:

- Talk's recording backend protocol does **not** match the current generic operator API directly
- Cassini needs a Talk-specific backend adapter surface
- the existing operator/runtime is still likely reusable behind that adapter
- the main difficult seam is the result/store callback lifecycle, not start/stop alone

## Reassessment

The next uncertainty is no longer "should we use native Talk UX?"
That is settled.

The next concrete decision is:

- whether to implement the Talk-backend adapter directly inside `cassini-operator`
- or as a thinner sibling entry surface that delegates into operator/runtime behavior

That is the next focused shaping/implementation question.
