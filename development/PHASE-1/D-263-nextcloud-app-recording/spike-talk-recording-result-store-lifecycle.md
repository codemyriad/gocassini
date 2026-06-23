## D-263 Spike: Talk recording result/store lifecycle

### Context

The revised D-263 architecture is now:

- keep Talk's native recording UX
- make Cassini the recording backend behind that UX
- preserve Cassini-owned live capture and downstream artifacts

The remaining hard seam is not start/stop request handling.
It is what happens after capture finishes:

- what Talk expects a recording backend to report
- what file Talk expects a recording backend to upload
- what Cassini needs to keep for its own artifact, transcription, and viewer pipeline

### Goal

Define the least awkward way for Cassini to satisfy Talk's recording result lifecycle without collapsing Cassini's richer output model down to "just one uploaded file".

Specifically:

- determine callback and upload ordering
- determine which Cassini output should be uploaded back to Talk
- determine whether Cassini's portable `.opus` should be part of Talk's `/store` contract
- decide how this affects the adapter boundary in front of `cassini-operator`

### Sources used

- Talk recording API docs:
  - https://nextcloud-talk.readthedocs.io/en/stable/recording/
- Talk backend controller:
  - `nextcloud/spreed/lib/Controller/RecordingController.php`
- Talk recording service:
  - `nextcloud/spreed/lib/Service/RecordingService.php`
- Official Nextcloud recording server:
  - `nextcloud/nextcloud-talk-recording/src/nextcloud/talk/recording/BackendNotifier.py`
  - `nextcloud/nextcloud-talk-recording/src/nextcloud/talk/recording/Service.py`
  - `nextcloud/nextcloud-talk-recording/src/nextcloud/talk/recording/RecorderArgumentsBuilder.py`
- Current repo:
  - `README.md`
  - `docs/portable-meeting-format.md`
  - `docs/architecture.md`
  - `cassini-go-recorder/README.md`
  - `cassini-operator/internal/operator/record_runtime.go`

### Outcome

This spike is complete enough to lock the result/store direction for D-263.

Selected conclusion for D-263:

- **Cassini should satisfy Talk's `/store` step by uploading an allowed recording file, not the portable `.opus` artifact**
- **The right upload candidate is Cassini's recorded `.mkv` deliverable**
- **Cassini's portable `.opus` and viewer-oriented outputs should remain downstream Cassini artifacts, not the primary Talk store payload**
- **The Talk adapter should send `started`, then on stop send `stopped`, then perform `/store` upload, matching the official recording server ordering**
- **The Talk adapter boundary should own the callback/upload protocol, while `cassini-operator` remains the execution engine that produces the recording outputs**

Put more concretely:

- Talk wants one ordinary recording file stored in the moderator's Talk files area
- Cassini wants to keep richer meeting outputs
- the clean compromise is:
  - upload the Cassini meeting `.mkv` to Talk
  - keep the Cassini run/MKV/build pipeline intact behind that

## What Talk expects after recording stops

### 1. Backend lifecycle callbacks

Talk exposes:

- `POST /ocs/v2.php/apps/spreed/api/v1/recording/backend`

The recording backend uses that endpoint to report:

- `started`
- `stopped`
- `failed`

From the official recording server implementation, the practical ordering is:

1. capture becomes live
2. backend sends `started`
3. stop is requested
4. backend finalizes capture
5. backend sends `stopped`
6. backend uploads the recording file through `/store`

That means the callback lifecycle and the file-upload lifecycle are related, but distinct.

### 2. File upload to `/store`

Talk exposes:

- `POST /ocs/v2.php/apps/spreed/api/v1/recording/{token}/store`

That upload requires:

- multipart form upload
- `owner`
- Talk recording auth headers

Important detail from Talk's server-side implementation:

- request authentication for `/store` is **not** based on the multipart body
- it verifies HMAC over `random + roomToken`

So the Cassini adapter should treat `/store` as a special-case signed upload, not as the same checksum rule as JSON callback bodies.

### 3. Allowed file formats

Talk validates uploaded recording files against this allowlist in `RecordingService`:

- `audio/ogg` => `.ogg`
- `video/ogg` => `.ogv`
- `video/mp4` => `.mp4`
- `video/webm` => `.webm`
- `video/x-matroska` => `.mkv`

That matters because Cassini's portable `.opus` artifact is **not** in this allowlist.

## What the official recording server does

The official `nextcloud-talk-recording` server is a useful reference because it shows the protocol as Nextcloud expects it to be used, not just documented.

### Start/stop/callback/upload flow

From `Service.py` and `BackendNotifier.py`:

- on successful join/start:
  - send `started`
- on stop:
  - tear down helpers and finalize the local recording file
  - send `stopped`
  - upload the file to `/store`
  - remove the local file after successful upload

That means Talk does **not** require upload to happen before the `stopped` callback.

### Output file type

The official server builds its output filename using ffmpeg arguments and an extension chosen by configuration.
The default model is still "produce one ordinary media file and upload it back to Talk".

It does not try to upload a custom rich meeting container.

## What Cassini currently wants

Cassini's current output model is richer than Talk's recording-backend contract.

### 1. Live recording output

Cassini's recording path centers on:

- live Talk capture
- session artifacts
- a final meeting `.mkv`

That `.mkv` is already described in-repo as the public playback/transcription input and the recorder's main post-capture deliverable.

### 2. Downstream build outputs

From the local docs, the normal post-recording flow is:

- recording `.mkv`
- `cassini build`
- meeting bundle:
  - `meeting.webm`
  - transcript files
  - captions
  - summary
  - manifest
- portable `.opus`

So the portable `.opus` is **not** the capture-time recording file.
It is a later packaging format for Cassini consumption and sharing.

## Main mismatch

Talk's `/store` endpoint is designed for:

- one conventional uploaded recording file

Cassini's final product model is designed for:

- one richer downstream artifact set

Those are not the same thing.

The key mistake would be trying to force the portable `.opus` file into Talk's `/store` step.

That would be awkward for three reasons:

1. Talk does not list `.opus` as an allowed recording upload format.
2. Cassini's portable `.opus` is not the natural immediate output of live capture.
3. Cassini still needs its `.mkv` as the best native input to its own build pipeline.

## Recommended mapping

### Upload target for Talk

Cassini should upload:

- **the recorded meeting `.mkv`**

Why `.mkv` is the right fit:

- Talk explicitly allows `.mkv`
- Cassini already treats `.mkv` as the primary recording deliverable after capture
- Cassini's downstream transcription/build path already uses `.mkv` naturally
- it avoids adding a new "export a Talk-safe upload file" conversion step in D-263

### Outputs Cassini should keep outside Talk's `/store`

Cassini should keep, outside the Talk upload contract:

- session artifacts
- the recorded `.mkv` in Cassini-managed storage if needed for retention/rebuilds
- meeting bundle outputs
- the portable `.opus`
- viewer/published outputs

Talk only needs one uploaded recording file to satisfy the recording backend contract.
Cassini can still keep its richer outputs for its own product model.

## Recommended lifecycle for D-263

### Start

1. Talk sends `POST /api/v1/room/{token}` with `type=start`
2. Cassini adapter verifies Talk signature
3. Cassini adapter resolves room token -> Talk call URL / execution identity
4. Cassini adapter starts recording work through `cassini-operator` or equivalent runtime
5. When Cassini is actually live in the room, adapter sends `started`

### Stop

1. Talk sends `POST /api/v1/room/{token}` with `type=stop`
2. Cassini adapter signals stop to the running recording job for that room
3. Cassini finalizes the meeting `.mkv`
4. Cassini adapter sends `stopped`
5. Cassini adapter uploads the `.mkv` to `/recording/{token}/store`
6. Cassini may continue its own downstream `build`/packaging flow after or alongside upload

### Failure

Send `failed` when Cassini cannot reach a real recording start or when capture/finalization fails before a valid recording file exists.

For upload failure after `stopped`:

- the official Nextcloud recorder does not appear to send a later compensating `failed`
- D-263 should follow that behavior initially rather than inventing a different lifecycle semantic
- but Cassini should log the failure clearly and leave room for retry policy later

## Adapter boundary implication

This spike strengthens the earlier protocol-fit conclusion:

- the Talk-facing adapter should own:
  - callback signing
  - `/store` upload signing
  - room-token lifecycle state
  - mapping "Talk room token" to "Cassini execution/job identity"
- `cassini-operator` should stay focused on:
  - running `cassini doctor --target record`
  - running `cassini record`
  - exposing stop/process status primitives
  - surfacing where the final `.mkv` ended up

That split is cleaner than teaching the generic operator API to speak Talk's callback/store protocol directly.

## Decisions locked by this spike

1. **Do not upload the portable `.opus` to Talk `/store`.**
2. **Use Cassini's final meeting `.mkv` as the Talk-uploaded recording file.**
3. **Treat Talk `/store` as a sidecar delivery obligation, not as Cassini's primary artifact contract.**
4. **Keep the richer Cassini build/portable/viewer pipeline downstream from the recording upload.**
5. **Match the official recording server ordering: `started` -> `stopped` -> `/store` upload.**

## What this changes in D-263

This removes one major ambiguity from the revised shape:

- D-263 does **not** need to solve "how to make Talk understand Cassini's portable artifact format"
- D-263 only needs to solve "how to make Talk receive one valid uploaded recording file while Cassini continues to own richer downstream outputs"

That makes the first cut much narrower and more realistic.

## Remaining open questions

1. **Room-token to call-URL mapping**
   - Talk start requests identify the room by token.
   - Cassini's existing recording path is still shaped around a full Talk call URL.
   - We still need to define where that URL is reconstructed for execution.

2. **Post-upload downstream trigger**
   - After `.mkv` upload, should Cassini immediately enqueue its own `build` step, or should that remain an operator-controlled follow-up in the first cut?

3. **Retention policy**
   - After successful Talk upload, which artifacts does Cassini retain locally by default, and for how long?
