## D-263 Spike: room-token execution mapping

### Context

The main D-263 architecture is now:

- use Talk's native recording UX
- make Cassini the recording backend
- keep Cassini on the live-capture path

The previous spikes resolved:

- Talk protocol fit
- result/store lifecycle

The last execution-side ambiguity is:

- **Talk start requests identify the meeting by room token, while Cassini's current public recording entrypoint is shaped around a full Talk call URL**

### Goal

Decide how the Talk backend adapter should start Cassini recording work for a room token.

Specifically:

- determine whether Cassini truly needs a full call URL
- determine whether the adapter should reconstruct a synthetic call URL
- determine whether Cassini should grow a token/native target shape instead

### Sources used

- Current repo:
  - `cassini-go-recorder/internal/nextcloud/call_url.go`
  - `cassini-go-recorder/internal/nextcloud/ocs_client.go`
  - `cassini-go-recorder/internal/talk/recorder.go`
  - `cassini-go-recorder/internal/cassini/cli.go`
  - `cassini-operator/internal/operator/record_runtime.go`
- Official Nextcloud recorder:
  - `nextcloud/nextcloud-talk-recording/src/nextcloud/talk/recording/Participant.py`
  - `nextcloud/nextcloud-talk-recording/src/nextcloud/talk/recording/Config.py`
- Talk frontend source:
  - `nextcloud/spreed/src/utils/webrtc/index.js`
  - `nextcloud/spreed/src/mainRecording.js`

### Outcome

This spike is complete enough to lock the direction for D-263.

Selected conclusion for D-263:

- **Cassini does not fundamentally need a full public call URL to execute a recording**
- **The real execution identity is `baseURL + roomToken`**
- **For D-263, the clean implementation is to add a native internal recording target shape based on `baseURL` and `roomToken`, instead of forcing the Talk adapter to fabricate a URL**
- **The existing `--call <url>` CLI should remain as a user-facing convenience wrapper**

Put more concretely:

- the adapter should receive Talk's backend request, which already implies one Nextcloud base URL and one room token
- it should hand those values into Cassini directly
- it should not depend on a fake reconstructed string like `https://cloud.example.com/call/<token>` just to satisfy a parser

## What Cassini currently does

### Public CLI shape

The current user-facing recorder entrypoint is:

- `cassini record --call <CALL_URL> --out ...`

Inside the recorder, `ParseCallURL(...)` extracts:

- `baseURL`
- `roomToken`

from a path that contains:

- `/call/<token>`

### What happens after parsing

In `internal/talk/recorder.go`, after parsing the call URL:

- the recorder stores `baseURL`
- the recorder stores `roomToken`
- the OCS bootstrap uses `baseURL` and `roomToken`
- signaling settings are fetched by token
- call join happens by token

So the full URL is not used as a durable control identity.
It is only a convenience input form.

### The one place the full URL still matters

The recorder currently passes the original call URL into session-artifact metadata.

That is a metadata/provenance concern, not an execution requirement.

## What the official Nextcloud recorder does

The official recording server does not need a moderator-facing call URL either.

Its model is:

- configure one backend Nextcloud base URL
- receive a room token from Talk
- open `/index.php/call/{token}/recording`
- ask Talk for recording signaling settings using:
  - `signalingGetSettingsForRecording(token, random, checksum)`
- join the signaling/call flow for recording

That is strong evidence that the real identity is:

- Nextcloud backend base URL
- room token

not:

- some end-user-copied call URL

## What this means for Cassini

Cassini's current URL-shaped API is an artifact of the original manual workflow:

- colleague copies a Talk URL
- runs a script with that URL

That shape made sense for manual use.
It is not the right primary shape for a Talk recording-backend integration.

## Options considered

### A. Adapter reconstructs a synthetic call URL and uses today's API

Mechanism:

- Talk adapter receives:
  - backend base URL
  - room token
- adapter synthesizes something like:
  - `<baseURL>/call/<token>`
  - or `<baseURL>/index.php/call/<token>`
- adapter passes that into the existing `cassini record --call ...` path

Pros:

- smallest immediate code change
- can probably work because `ParseCallURL` only needs a path containing `/call/<token>`

Cons:

- keeps the wrong abstraction as the internal contract
- makes the Talk adapter responsible for URL-shape trivia
- risks coupling to deployment-specific path variations (`/index.php/...`)
- hides the true execution identity behind a fake convenience string

Conclusion:

- acceptable as a temporary bridge
- not the shape to lock in as the long-term D-263 design

### B. Cassini gains a native internal target shape: `baseURL + roomToken` — selected

Mechanism:

- introduce a recording target shape or equivalent internal runtime input with:
  - `baseURL`
  - `roomToken`
  - optional `callURL` for provenance/metadata only
- keep CLI `--call` as a wrapper that parses into that structure
- let the Talk adapter invoke the native target directly

Pros:

- matches how the recorder actually works after parsing
- matches how Talk identifies the room in backend requests
- removes unnecessary URL reconstruction from the adapter
- keeps manual CLI ergonomics unchanged
- makes future tests and backend-triggered runs cleaner

Cons:

- requires a small refactor across the recorder/operator boundary

Conclusion:

- best fit for D-263

## Recommended mapping

### Execution contract

The Talk adapter should treat the recording target as:

- `baseURL`
- `roomToken`
- `guestName`
- stop/duration settings as needed

The adapter already knows `baseURL` because it is the configured Talk backend origin associated with the incoming request.
The adapter already knows `roomToken` from:

- `POST /api/v1/room/{token}`

So there is no missing data in the backend request model.

### Cassini runtime direction

For D-263, the clean execution direction is:

1. preserve the existing CLI:
   - `cassini record --call <CALL_URL>`
2. add an internal/native target representation:
   - token-oriented, not URL-oriented
3. make the Talk adapter call that native path
4. keep the original call URL optional for session metadata only

## Why this matters for implementation slicing

This affects slice boundaries.

### I2 implications

`I2 Talk start/stop lifecycle compatibility` should not stop at:

- "Talk can start a generic operator job"

It should also include:

- native runtime mapping from Talk `roomToken` to Cassini recording target

### I3 implications

This spike does not materially change the result/store slice.
It only clarifies how the recording job is started cleanly.

## Decisions locked by this spike

1. **Do not make the Talk adapter depend on a fabricated user-facing call URL as the long-term contract.**
2. **Treat `baseURL + roomToken` as Cassini's true recording identity for Talk-backend integration.**
3. **Keep `--call <url>` as a convenience wrapper, not as the core backend integration shape.**
4. **Allow optional call-URL metadata/provenance, but do not require it for execution.**

## Remaining open question after this spike

The major planning ambiguities are now mostly resolved.
The main remaining implementation design choice is narrower:

- does the Talk-specific adapter live inside `cassini-operator`, or beside it while delegating into the same runtime primitives?
