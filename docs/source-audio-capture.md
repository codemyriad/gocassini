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

## Timing: two clocks, and which one does what

The obvious way to align an upload is to cross-correlate it against the
recorder's copy of the same speaker. That fails exactly when the feature
matters: if the uplink was bad, the reference is full of holes.

The first design here tried to avoid correlation entirely by mapping the
client's RTP timestamps straight onto the recorder's, on the theory that both
are the sender's 48 kHz audio clock. **They are not.** Janus rewrites the
timestamps it relays to each subscriber — `janus_rtp_header_update` computes
`last_ts = (timestamp - base_ts) + base_ts_prev` and re-anchors on every SSRC
change or pause — so what the recorder logs sits in a per-subscriber space whose
offset from the sender is unknown and moves at those seams. Anchoring on it
would place a participant's words at a confidently wrong time. The recorder
still records its first RTP timestamp per stream, but as a diagnostic only.

So the two axes of the fit come from different places:

**Rate, from the client's own anchors.** Each anchor pairs a wall-clock instant
with an RTP timestamp on the participant's audio sample clock. The ratio between
them is that machine's sound-card drift — the dominant drift in this system,
tens to hundreds of milliseconds over a long meeting — and the fit solves it
rather than estimating it. This part is genuinely immune to loss: the anchors
describe frames the client **encoded**, so whether each one reached the server is
irrelevant. A test asserts that dropping six of every seven anchors, an 86% loss
rate, moves the fitted rate and offset by less than a millisecond.

**Offset, from wall clock.** The recorder records the wall-clock instant of each
stream's first packet (`first_packet_wall_ms`) against its position on the
meeting timeline. Both derive from the same monotonic clock, so that side is
exact.

### What that costs

The offset is only as good as the agreement between the participant's clock and
the recorder's, plus the encoder's roughly constant latency. With both machines
NTP-disciplined that is tens of milliseconds — comfortably inside a word. With a
badly synchronised client it is seconds, and that speaker's words would land at
the wrong time.

`PlausibleOffset` is a guard, not a fix: it rejects placements falling outside
the recording, which catches a clock wrong by hours but not one wrong by
seconds.

**The real fix is not implemented yet:** a single cross-correlation against any
stretch where the recorded track has intact audio. That is one constant, needing
a few good seconds anywhere in the call rather than a good reference throughout,
so it degrades far more gracefully than correlation-based alignment. Until it
exists, ingestion should be treated as trustworthy only where clients are known
to be time-synchronised.

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

## Testing

The arithmetic and the intake are covered by Go unit tests. The browser chain
cannot be — a service worker rewriting another app's bundle, an encoded
transform, OPFS and an upload only exist in a browser, and the harness has no
browser at all (its Talk publishers are pion Go clients).

`cassini-app/e2e/` therefore runs a real Chromium against a stub same-origin
Nextcloud: the Cassini page registers the worker, a stub Talk bundle publishes
audio over a real `RTCPeerConnection`, and **a transform on the receiving side
drops a share of the encoded frames** — the lossy-uplink condition the feature
exists for. The tests assert that the loss is real, that the captured copy is
unaffected by it, that the anchors advance monotonically on the sender's clock,
that a mute spell is recorded, and that nothing is uploaded without consent.

```bash
npm run build:capture -w cassini-app   # the worker serves the BUILT bundles
npm run test:e2e -w cassini-app
```

It does not cover real Nextcloud, real Talk, real Janus, AppAPI's proxy and its
header forwarding, or the operator's Go handler. Those gaps are deliberate and
named in the CI job.

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

- **Cross-correlation refinement of the offset** (see above). This is the gap
  that decides whether ingestion can be trusted on arbitrary clients.
- **Rebuild on late upload.** An upload arriving after the meeting was published
  does not trigger a rebuild; only a manual rerun picks it up.
- **The published mix.** Ingestion changes the transcript only. The playable
  audio stays the recorded mix, which is what the viewer seeks against.
- **Attribution.** The cross-track attribution stage still measures the recorded
  tracks, not the ingested ones.
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
