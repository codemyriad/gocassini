---
shaping: true
---

# D-263 — Test Strategy

This document defines how D-263 should be tested now that Cassini is integrating with Nextcloud Talk's native recording-backend lifecycle.

The important split is:

- local manual acceptance must prove the real browser, signaling, WebRTC media, recorder, operator, post-processing, and viewer path
- automated integration tests should prove the Nextcloud/Talk recording-backend lifecycle without depending on real browser media capture

Those are different risks. Keeping them separate makes failures easier to interpret.

## Target behavior

D-263 is successful when a local operator can run a complete native Talk recording flow:

1. Start the local Nextcloud/HPS/signaling stack and Cassini services manually.
2. Join a Talk room through a browser.
3. Start recording through Talk's native recording control, or start the room with Talk's automatic recording option enabled.
4. Stop recording explicitly, or leave the call and let room-empty auto-stop end the recording.
5. Cassini operator stops the recording cleanly.
6. Cassini produces a usable recording artifact and runs the downstream processing path.
7. The resulting meeting is visible through Cassini viewer.

The current failure:

```text
compose final output failed: no remuxable streams found in session artifact
```

belongs to this local manual acceptance path. It means the Talk lifecycle reached the recorder, but the real media capture/remux path did not produce usable packet streams. Automated Nextcloud integration tests should not hide this, but they also should not depend on solving browser media capture in order to validate the Talk backend protocol.

## Test layers

| Layer | Purpose | Uses real Nextcloud? | Uses real browser media? | Expected output |
|-------|---------|----------------------|--------------------------|-----------------|
| L1 operator unit/contract tests | Verify Talk backend request auth, start/stop parsing, callback signing, and store upload behavior. | No | No | Fast Go tests with fake Talk/operator dependencies. |
| L2 local Nextcloud integration test | Verify a real Nextcloud Talk instance can configure Cassini as recording backend, create a room, start recording, observe Cassini start, stop recording, and observe the job pipeline progress. | Yes | No | A completed Cassini job using a fake/noop recording executor or deterministic synthetic artifact. |
| L3 local manual media acceptance | Verify browser-to-HPS-to-recorder media capture and the full Cassini viewer path. | Yes | Yes | A real recording visible in Cassini viewer. |

## L2 automated integration scope

The D-263 automated integration test should validate the integration seam, not the entire media stack.

It should do this:

1. Boot or target the local Nextcloud Talk test stack.
2. Configure Talk's recording backend URL and shared secret to point at Cassini.
3. Configure or start Cassini operator in a test mode where the recording subprocess can be replaced by a fake/noop executor.
4. Create a Talk room through Nextcloud APIs.
5. Start recording through the Talk recording API or the closest supported server-side action that exercises the same backend lifecycle.
6. Assert Cassini accepted the signed Talk start request and created/runs a recording job for the correct room.
7. Stop recording through Nextcloud/Talk.
8. Assert Cassini accepted the matching stop request.
9. Assert the operator advanced the job through the expected lifecycle and issued the expected Talk callbacks.
10. Assert no media-specific failure is required for the test to pass.

The test should not require:

- launching a browser
- joining a WebRTC call as a human or browser automation participant
- receiving RTP packets
- producing a real remuxed media file from WebRTC streams
- validating Cassini viewer playback

## L2 recording executor shape

The clean automated-test seam is an explicit operator test executor for Talk-driven jobs.

That executor should:

- behave like a recorder process from the operator's perspective
- emit the same "recording has started" signal the operator uses to notify Talk
- stay running until the Talk stop request arrives
- on stop, produce a deterministic successful run artifact that lets the operator pipeline continue
- avoid joining the Talk call or subscribing to WebRTC media

This can be implemented as one of:

- an operator-only fake `cassini` binary in the integration harness
- a supported `cassini record --simulate` or future `--noop-live` mode wired only for tests
- an operator test configuration that swaps the record worker implementation while keeping the HTTP Talk backend surface real

The important rule is that the Talk HTTP surface must remain real. Only the media recorder should be substituted.

## L3 local manual acceptance scope

The local manual acceptance test remains the proof that Cassini really records Talk media.

It should verify:

1. Browser participant joins a real Talk call.
2. Talk native recording starts Cassini.
3. Cassini's recorder joins the same HPS/backend identity as the browser participant.
4. Cassini sees participant/session events.
5. Cassini receives offers and media packets.
6. Room-empty auto-stop works after all non-recorder participants leave.
7. Explicit Talk stop works.
8. Final remux succeeds.
9. Operator completes processing and publish.
10. The resulting meeting is visible in Cassini viewer.

Failures such as `no remuxable streams found in session artifact` are L3 failures. They should be debugged with HPS logs, recorder session artifacts, SDP/offer flow, and packet stream evidence, not by broadening the Nextcloud integration test.

## Acceptance criteria for D-263 testing

D-263 should be considered test-covered when:

- L1 unit/contract tests cover auth, start, stop, callback, and upload behavior
- L2 automated integration proves real Nextcloud can drive Cassini's Talk backend lifecycle and the operator pipeline can progress with a fake/noop recording executor
- L3 manual local acceptance proves the complete media path through Cassini viewer
- documentation clearly states that L2 is not a substitute for L3 media acceptance

## Open implementation decisions

| ID | Decision | Initial direction |
|----|----------|-------------------|
| D263-T1 | How should L2 start recording in Nextcloud without a browser? | Prefer the Talk server/API path that exercises the real recording backend start lifecycle. If unavailable, use a minimal server-side test helper documented as such. |
| D263-T2 | What fake/noop executor should L2 use? | Prefer an explicit test executor seam over overloading production recording behavior. |
| D263-T3 | What artifact should the fake/noop executor produce? | A deterministic successful Cassini run artifact sufficient for operator build/publish progression, not a claim of real media capture. |
| D263-T4 | Where should L3 evidence be checked? | Recorder session artifact and operator logs first; HPS logs when no offers/media packets are observed. |

## Harness command

The first L2 implementation is:

```bash
harness/bin/d263-nextcloud-lifecycle.sh
```

It expects the local harness stack to be running, then:

- launches a temporary `cassini-operator`
- configures Talk's recording backend URL and secret
- creates and activates a Talk room
- starts and stops Talk recording through Nextcloud's recording API
- replaces only the media worker with a fake `cassini` executable
- waits for the operator job to complete successfully

This command intentionally does not validate real browser media capture or
viewer playback.
