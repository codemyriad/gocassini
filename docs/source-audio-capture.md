# Source-Side Audio Capture

Date: 2026-08-31

Status: stage 1 (capture + intake) implemented; placement and rebuild not yet
implemented.

## The problem

Cassini's recorder joins the room as a subscriber and stores what the SFU
forwards. That audio is whatever survived the sending participant's uplink. When
someone's connection is bad, the words are not degraded — they are absent, and no
amount of server-side processing recovers them.

The recorder cannot fix this from where it sits. It answers an offer from the
MCU (`internal/talk/recorder.go`); it never publishes, so it has no lever on the
bitrate or the loss the sender's browser produced. Better audio has to be
captured at the source or not at all.

## The shape

The participant's browser records the track Talk is **sending**, before Opus
encoding and before the network, buffers it locally for the whole call, and
uploads it afterwards.

```text
  mic ─▶ Talk's media pipeline ─▶ sender track ─┬─▶ Opus ─▶ network ─▶ SFU ─▶ recorder ─▶ .mkv
                                                │                                         (lossy)
                                                └─▶ MediaRecorder ─▶ OPFS ─▶ upload ─▶ operator
                                                                                       (intact)
```

Two properties fall out of recording the *sender's* track rather than opening a
microphone of our own:

**Mute is honoured by construction.** Talk mutes with `TrackEnabler`
(`src/utils/media/pipeline/TrackEnabler.js`), which sets `enabled = false` on the
very track feeding the peer connection and re-forces that state if anything
changes it. A disabled `MediaStreamTrack` delivers silence to every sink. There
is no code path in the capture payload that can produce a hot mic, and none could
be added — the enforcement is Talk's, upstream of us, and it actively fights
attempts to flip it back.

**It is the same signal the SFU encoded**, one step earlier. That is what makes
verifying an upload against the server's own recording meaningful later.

## Timing: why this survives a bad network

The obvious way to align an uploaded track with the meeting is to cross-correlate
it against the recorder's copy of the same speaker. That fails exactly when the
feature matters: if the uplink was bad, the reference is damaged too.

So the client carries its own timing instead. A `RTCRtpScriptTransform` on the
outgoing audio sender sees every encoded Opus frame and reads its **RTP
timestamp** — the sender's own 48 kHz sample clock, and the same number the
recorder writes into its `.rtplog` for every packet that reached it.
`pkg/core/timeline` already maps that onto the meeting timeline.

Packet loss destroys the audio the server received. It does not corrupt the
timestamps on the packets that did arrive, and a handful of those anywhere in the
call is enough to place a whole segment. Drift needs no special handling either:
the RTP timestamp *is* the sender's capture clock, and sender-versus-recorder
drift is what the timeline estimator already corrects.

Correlation is therefore demoted from *the alignment mechanism* to *a
verification step* — run where the SFU audio is intact, skipped where it is not.

## How the code reaches Talk's page

Cassini is an AppAPI ExApp: no PHP runs inside Nextcloud, so there is no
supported way to add a script to another app's page. AppAPI's `ui/script`
registration is applied only by its own `TopMenuController`, for type
`top_menu`, on AppAPI's embedded page; the navigation entry it adds elsewhere is
a plain link with no JavaScript attached.

A service worker is the one remaining same-origin mechanism:

1. The operator serves `/ui/capture-sw.js` with `Service-Worker-Allowed: /`
   (`capture_assets.go`).
2. AppAPI's proxy forwards that header verbatim —
   `ExAppProxyController::createProxyResponse` copies every response header
   except its own auth set.
3. The Cassini page registers the worker at the Talk call scopes
   (`src/capture/register.ts`).
4. A worker's scope decides which **clients** it controls, not which URLs it may
   intercept. From a call page it sees every subresource that page requests,
   Talk's own bundle included, and serves that bundle with the capture payload
   appended.

Nextcloud core relies on the same header for its Files preview worker
(`apps/files/lib/Controller/ApiController.php`).

Two constraints worth knowing before touching this:

- **Never claim the `/` scope.** Core's Files worker is registered there, and a
  same-scope registration *replaces* rather than coexists. The Talk call scopes
  are narrower, so they win on call pages and leave core's alone.
- **Append, never patch.** Appending survives Talk upgrades that any textual
  patch of Talk's source would not, and any failure must return Talk's real
  bundle — a broken capture feature costs a transcript improvement, a broken
  bundle costs Talk.

This mechanism is **unsupported**: it modifies another app's JavaScript on the
user's origin. It is appropriate for a deployment whose operator controls it. It
is not appropriate for a public Nextcloud app store listing, which wants a small
PHP companion app doing the same job through `Util::addScript`. Only
`src/capture/sw.ts` and `capture_assets.go` are specific to the service-worker
route; the capture, timing and upload code is identical either way.

## Intake and trust

`POST /operator/capture/upload` (`capture_upload.go`), USER-level in
`appinfo/info.xml`. Two facts are decided by the server and never read from the
request body:

- **Who.** The AppAPI-authenticated caller is stamped as the owner. That id is
  the same value the recorder writes into each MKV audio track as
  `PARTICIPANT_ID` (both come from Talk's `userid`), so a later stage can join an
  upload to a track exactly rather than by matching names. The client's own
  `participantId` is recorded only so a mismatch can be logged.
- **Whether.** The caller must be a participant of the room, checked against
  Talk's participants API acting as that user.

What is deliberately *not* decided at intake is whether the audio is genuine.
That needs the recorder's copy of the same speaker to compare against, and it
belongs with the stage that does the placement. When that stage is written, note
that the rule cannot be "the local track may only sharpen the SFU track": where
the network ate a sentence, contradicting the SFU track is the entire point. The
SFU recording is an *attestation sample* — verify on the windows where it has
intact audio, require some minimum verified fraction, then accept the whole
track including the holes it fills.

## Trying it

Capture is off until a user opts in. The opt-in UI is not built yet; the control
is exposed on the Cassini page:

```js
// On the Cassini page in Nextcloud (registers the service worker):
await window.cassiniSourceCapture.enable()
// Then reload, join a Talk call, leave it, and the upload runs.
await window.cassiniSourceCapture.disable()  // unregisters, removes consent
```

Uploads land under the operator's `--capture-root`
(`CASSINI_OPERATOR_CAPTURE_ROOT`, default `<data>/capture`) as
`<room>/<user>/<call-start-ms>/` holding `capture.json` and the segment files.

## Not done yet

- **Placement and rebuild.** Nothing consumes an upload. The next stage maps the
  sidecar anchors through `pkg/core/timeline`, renders the audio onto the meeting
  timeline in the shape `ExtractSpeakerFloats` already returns, and re-runs the
  build.
- **The opt-in UI**, and with it any consent copy worth shipping.
- **Verification of an upload against the recorder's own audio** (see above).
- **Retention.** Uploads accumulate under the capture root; nothing sweeps
  uploads whose meeting never materialised.
- **Firefox raw-audio path.** `MediaStreamTrackProcessor` is Chrome/Safari only.
  The timing path (`RTCRtpScriptTransform`) works in all three engines; the
  current capture path uses `MediaRecorder`, which is universal.
- **E2EE calls.** A sender has one transform slot and Talk's end-to-end
  encryption already uses it (`src/utils/e2ee/JitsiE2EEContext.js`). Capture
  still records; it produces no RTP anchors.
- **Non-browser participants.** Mobile Talk apps have no equivalent hook.
