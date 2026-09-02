# Source-Side Audio Capture

Date: 2026-08-31

Status: **administrator-enabled prototype.** Capture, intake and ingestion are
implemented and tested. A tiny native companion app now delivers the payload
through Nextcloud's sanctioned additional-scripts event; the offset half of the
timing model still needs the correlation refinement described below before it
can be trusted on clients whose clocks are not known to be synchronised.

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
encoding and before the network, while Talk's official recording is active.
It buffers locally and uploads as soon as Talk confirms recording stopped;
leaving the room is an idempotent fallback rather than the normal trigger.

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

Sample zero of the uploaded file is the instant `MediaRecorder` started, not the
first sampled anchor — anchors arrive one per fifty encoded frames and the first
comes after the encoder spins up, so anchoring on it placed every speaker late
by up to a second. The anchors fix the rate; the segment's start instant fixes
the offset.

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

Cassini's ExApp cannot add code to Talk by itself. The separately packaged
`cassini_capture` native app listens for Talk's public
`LoadAdditionalScriptsEvent`, scopes the shared event to authenticated Talk call
routes, injects the ExApp proxy base and administrator switch as initial state,
and loads `capture-payload.js` with `Util::addScript()`.

That ordinary script is emitted before Talk's bundle, so the outgoing transform
is attached synchronously in `addTrack` before negotiation. No Talk bundle is
intercepted or modified, no service-worker scope is claimed, and disabling the
companion stops injection on the next page load. See
[source-audio-capture-delivery.md](source-audio-capture-delivery.md).

Current Talk does not dispatch the event on its anonymous guest path, and the
upload endpoint deliberately requires a logged-in user. Source capture therefore
supports authenticated participants only.

## Two switches, both off by default

`CASSINI_SOURCE_CAPTURE` decides whether anything is collected at all.

The ExApp synchronizes this setting into AppAPI's ExApp config store, so the
companion can inject an authoritative answer before the payload runs. With it off the
payload does not instrument Talk, the ExApp assets are absent, and uploads are
refused.

A call already running still has JavaScript in memory. The payload therefore
asks the server again every thirty seconds at `operator/capture/enabled`; that
check fails closed — an unreachable or unclear answer means no recording.
Switching the setting off therefore stops a call in progress within about half
a minute rather than at the next page load. The upload endpoint refuses as a
second line, and a client that gets that refusal deletes its buffer rather than
keeping it for a retry.

This is the containment boundary for the known limitations below — consent
recorded per browser origin rather than per Nextcloud account, and uploads
without a per-participant quota. Both are acceptable for a deployment whose
operator chose to run this prototype; neither is acceptable for one that merely
upgraded.

`CASSINI_SOURCE_AUDIO_INGEST` decides whether collected audio reaches a
transcript. See below.

## Ingestion is off by default

Capture and intake only collect. Substituting a participant's own recording
into the transcript is a judgement about where somebody's words belong, and the
offset half of that judgement still carries client clock skew. So an
installation opts in deliberately:

```
CASSINI_SOURCE_AUDIO_INGEST=1
```

Without it the operator never passes `--source-audio` to the build, uploads
accumulate, and transcripts are built from the recorded tracks exactly as
before.

Selection is scoped to the recording: a capture must be from the same Talk room
**and** from a call whose wall-clock window overlaps this one. Matching on
participant id alone was wrong in two ways that both end with one meeting's
speech in another's transcript — a later unrelated capture hid the correct
older one, and two calls close in time each looked plausible.

A participant with several tracks in one recording (a rejoin, a stream
rotation) has their source render attached to exactly one of them; the others
are dropped from transcription, because the render spans the whole timeline and
transcribing both would emit every word twice.

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

The arithmetic and intake are covered by Go unit tests. The browser chain — an
ordinary script loaded before Talk, an encoded transform, OPFS and an upload —
only exists in a browser, and the harness has no
browser at all (its Talk publishers are pion Go clients).

`cassini-app/e2e/` therefore runs a real Chromium against a stub same-origin
Nextcloud: a script tag models the companion hook, a stub Talk bundle publishes
audio over a real `RTCPeerConnection`, and **a transform on the receiving side
drops a share of the encoded frames** — the lossy-uplink condition the feature
exists for. The tests assert that the loss is real, that the captured copy is
unaffected by it, that the anchors advance monotonically on the sender's clock,
that a mute spell is recorded, that joining alone creates no audio storage,
that confirmed recording-off uploads while the call stays connected, that a
reload resumes and preserves both durable intervals, and that nothing is
uploaded without consent.

```bash
npm run build:capture -w cassini-app
npm run test:e2e -w cassini-app
./scripts/build-capture-companion.sh --skip-js-build
```

It does not cover real Nextcloud, real Talk, real Janus, AppAPI's proxy and its
header forwarding, or the operator's Go handler. Those gaps are deliberate and
named in the CI job.

## Trying it

Three things have to be true before a single byte is captured, and they are
deliberately independent: an administrator enables collection, the companion
app is installed so the payload reaches Talk at all, and the participant opts
in themselves.

**1. Install the companion app.** The payload is delivered by `cassini_capture`,
a separate native Nextcloud app (see
[source-audio-capture-delivery.md](source-audio-capture-delivery.md)). The
ExApp does not contain it and installing the ExApp does not install it. A
release tag publishes a `cassini_capture.tar.gz`; from a branch, build one:

```bash
./scripts/build-capture-companion.sh          # -> build/artifacts/capture-companion/
# then unpack it into the Nextcloud custom_apps directory and:
occ app:enable cassini_capture
```

**2. Enable collection on the ExApp.** `CASSINI_SOURCE_CAPTURE=1`, set as a
deploy option at registration (`app_api:app:register … --env`). The ExApp
mirrors it into Nextcloud app config, which is what the companion reads while
building the call page's initial state — the operator logs
`source capture: synchronized companion initial state enabled=true` when that
lands. Add `CASSINI_SOURCE_AUDIO_INGEST=1` as well if you want the uploaded
audio to reach a transcript rather than only be stored.

**3. Opt in, per participant.** There is no UI for this yet. On any Nextcloud
page in that browser:

```js
localStorage.setItem("cassini.sourceCapture.consent", "granted")
```

Then join an **authenticated** Talk call (guest pages are not supported —
Talk does not dispatch the hook there), press Talk's **Record** control, talk,
and press **Stop recording**. Cassini starts only after Talk confirms the
recording active and uploads at the confirmed stop; leaving the room performs
the same teardown as a fallback. Uploads land under the
operator's `--capture-root` (`CASSINI_OPERATOR_CAPTURE_ROOT`, default
`<data>/capture`) as `<room>/<user>/<call-start-ms>/`, holding `capture.json`
and the segment files.

If nothing arrives, check in this order: the companion is enabled
(`occ app:list | grep cassini_capture`), the operator logged the synchronized
initial state, and `operator/capture/enabled` answers `{"enabled":true}` for a
logged-in user. The client fails closed at every one of those.

## Not done yet

- **Cross-correlation refinement of the offset** (see above). This is the gap
  that decides whether ingestion can be trusted on arbitrary clients, and the
  reason ingestion is off by default.
- **Account-scoped consent.** The opt-in lives in `localStorage`, which is
  per-origin, not per-Nextcloud-account: two people sharing a browser profile
  share the answer.
- **Rebuild on late upload.** An upload arriving after the meeting was published
  does not trigger a rebuild; only a manual rerun picks it up.
- **The published mix.** Ingestion changes the transcript only. The playable
  audio stays the recorded mix, which is what the viewer seeks against.
- **Attribution.** The cross-track attribution stage still measures the recorded
  tracks, not the ingested ones.
- **The opt-in UI**, and with it any consent copy worth shipping.
- **Verification of an upload against the recorder's own audio** (see above).
- **Retention and quota.** Uploads accumulate under the capture root. Nothing
  sweeps uploads whose meeting never materialised, and nothing rate-limits a
  participant: repeated uploads at the 512 MiB per-request cap can fill the
  volume even with ingestion disabled. This is the largest remaining
  operational risk of enabling capture at all.
- **Abrupt-page tail.** A reload or crash can lose the not-yet-checkpointed tail
  of the current MediaRecorder chunk (at most about two seconds). Completed
  chunks and their recovery sidecar survive in OPFS, are retried on the next
  Talk page, and an active Talk recording resumes as a new capture session.
- **Salvage after a write failure.** A worker error during finalization
  sacrifices the segments that were written correctly along with the one that
  was not. It only arises after an OPFS write or flush has already failed, so
  it costs a recording that was partly lost anyway — but the good half is
  recoverable in principle and is not recovered.
- **A disabled ExApp still loads the payload.** The companion is a separate
  native app and reads `source_capture_enabled` from AppAPI's ExApp config,
  which outlives disabling the ExApp, so the script tag keeps appearing on Talk
  call pages until the companion itself is disabled.

  It records nothing: the payload's own permission check goes through the AppAPI
  proxy, which a disabled ExApp does not serve, and an unanswered check counts
  as no. So the cost is a script tag, not audio — but that safety rests entirely
  on the check failing closed, which is a second line, not the first.

  Disabling the ExApp *does* now write `false` into that config before it stops.
  Until D-698 that write was issued in a goroutine after the lifecycle response,
  on a context that AppAPI cancelled by stopping the container, so it never
  landed; the stored value stayed `true` indefinitely. Verified against a real
  install. To back the feature out completely, disable `cassini_capture` as
  well.
- **Firefox raw-audio path.** `MediaStreamTrackProcessor` is Chrome/Safari only.
  The timing path (`RTCRtpScriptTransform`) works in all three engines; the
  current capture path uses `MediaRecorder`, which is universal.
- **E2EE calls.** A sender has one transform slot and Talk's end-to-end
  encryption already uses it (`src/utils/e2ee/JitsiE2EEContext.js`). Capture
  still records; it produces no RTP anchors.
- **Non-browser participants.** Mobile Talk apps have no equivalent hook.
