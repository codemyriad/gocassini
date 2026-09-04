# Source-Side Audio Capture

Date: 2026-08-31

Status: **prototype, on by default on this branch.** Capture, intake and
ingestion are implemented and tested, and both switches run unless an
administrator sets them to `0`. A tiny native companion app now delivers the payload
through Nextcloud's sanctioned additional-scripts event; the offset half of the
timing model still needs the correlation refinement described below before it
can be trusted on clients whose clocks are not known to be synchronised.

What this collects and who decides it is described below: "Two switches, both on
by default" for the containment boundary, "Intake and trust" for what the server
settles about an upload and what it deliberately does not, and "What that costs"
for the limits of the timing model. Read those before running this branch on an
instance with real participants on it.

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
encoding and before the network, while Talk's official recording is active *and*
that browser is actually in the call. It buffers locally and uploads as soon as
Talk confirms recording stopped; leaving the room is an idempotent fallback
rather than the normal trigger.

Being in the call is a condition in its own right, and it is the only one of the
three that says anything about this browser: Talk's confirmed recording says the
*room* is being recorded, and the administrator switch says the *installation*
collects. So the recorder waits for the publishing connection to reach
`connected` with a live track. The device preview, the lobby, and a page whose
join never completed are all states in which nothing is recorded — which matters
because they are exactly the states a participant is in when they do not believe
they are in a meeting.

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

The recording remains the record, and both it and the upload stay on disk to
compare. Nothing in ingestion writes to `recording.mkv` or to the tracks decoded
from it: the splice below renders each participant onto a separate file, and the
published mix and the transcript are both made from that render. When a
transcript says something the meeting does not, the audio to check it against is
still there.

## What the published audio contains

The published mix carries the same splice the transcript was made from. Wherever
a participant's upload could be placed, the published audio holds the upload;
everywhere else it holds the recorded track, unchanged. So every word in the
transcript can be heard in the meeting people play back — which is the point:
a transcript quoting words that are inaudible in the recording is worse than a
mix that is a little cleaner than the call was.

There is one render and two consumers, so the two cannot disagree about where a
word is:

1. Each recorded track is decoded onto the meeting timeline at 48 kHz, exactly
   as the mixdown has always done.
2. A participant with a usable upload gets their tracks summed into one render
   and the upload laid over it, window by window, with a 15 ms linear crossfade
   at each edge so the handover does not click.
3. The encoder mixes that render in place of their recorded tracks, and the
   speech recogniser is handed a 16 kHz resample of the very same file
   (`_work/sourceaudio/source-<speaker>.wav`).

`manifest.provenance.sourceAudio` records `mix_spliced`, the `windows` each
placed segment covers — with the capture and the segment they came from, since
segment numbering restarts in every capture — the `crossfade_ms` and the
`render_hz`. The published `.opus` carries none of it: the portable v1 manifest
is frozen and has no field for it.

A speaker whose upload was refused, or absent, keeps their recorded audio in the
mix exactly as before, and a build with no usable upload publishes the audio it
would have published without the feature at all. `CASSINI_SOURCE_AUDIO_MIX=0`
turns off the published splice on its own, leaving the transcript spliced — a
rollback for a deployment that dislikes how the mix sounds, without giving up
ingestion. Like the two switches beside it, it takes any boolean, and a value
that is neither true nor false lands off and says so.

Three consequences worth knowing. The published audio is a different file from
what an unspliced build would produce, so the meeting's identity — the hash of
its Opus essence — changes when a rebuild adds audio. The render is
per-participant, so a rejoined participant's several tracks collapse into one
mix input; the mix counts them once, not twice. And where two of a participant's
segments abut — a microphone change mid-call — the published audio passes
through the recorded track for the length of two fades, about thirty
milliseconds of the same speaker as the SFU heard them. Suppressing the fades
there would put a step in instead, which is the click they exist to remove.

The splice holds no timeline in memory: it works on the file a chunk at a time,
so the Go heap it needs is a few tens of kilobytes however long the meeting is.
That is a straight win over the render this replaced, which held whole-timeline
`float32` buffers — 460 MB each at 16 kHz for a two-hour meeting, and it needed
several.

Temporary **disk** pays for it, and the trade is worth stating in numbers. Three
kinds of file live under `TMPDIR` while a meeting is built, each a full-timeline
48 kHz mono WAV for a two-hour call:

| file | size (2 h) | how many at once |
| --- | --- | --- |
| a decoded recorded track, one per stream | ~690 MB | every stream, until the splice replaces it |
| a participant's render | ~690 MB | one |
| the decoded upload being overlaid (`segment.wav`) | up to ~700 MB | one |

So the peak is roughly **the decoded tracks plus two more files of the same
size**: about 4.2 GB while a four-speaker two-hour meeting with an upload is
mixed. It does not grow with the number of participants who uploaded — a
spliced speaker's render takes the place of the tracks it was built from and
they are deleted as it does, and with `CASSINI_SOURCE_AUDIO_MIX=0` the render is
deleted outright once the recogniser's copy of it exists.

That is about 1.4 GB more than a build needed before this change, and not
because the tracks got bigger. The mixdown decoded the same tracks before; it
deleted them when it returned, and ingestion ran afterwards, from the MKV, into
memory. Now ingestion runs between the decode and the encode, so the tracks are
still there while the render and the decoded segment are made. The cost moved
from RAM to disk. A host whose `TMPDIR` is a small tmpfs will run out on a long
meeting sooner than it used to; the operator image points `TMPDIR` at its data
volume for exactly this reason.

The bundle pays for one more file per spliced speaker, unchanged by this branch:
the 16 kHz transcription input under `_work/sourceaudio/`, about 230 MB for a
two-hour meeting, which lives as long as the bundle does.

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

## Two switches, both on by default

This branch exists to run the feature end to end, so both switches are on when
nothing sets them. Each is an explicit opt-out: set it to `0` (or `false`) to
turn that half off. A value neither of those nor a recognised true also lands
off, and the operator logs it — a switch nobody can read must not be the one
that starts collecting microphones.

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

This switch is the whole containment boundary, and it is the only one Cassini
has. With it on — which is the default here — every authenticated participant of
every recorded call is captured; there is no per-participant control and no
answer of theirs is recorded anywhere. Telling the room is Talk's job — its
recording indicator, and its own `recording_consent` setting if the installation
needs each participant asked. So the default is right for a deployment whose
operator chose to run this branch and configured Talk accordingly; it is not
right for one that merely upgraded, and that deployment sets:

```
CASSINI_SOURCE_CAPTURE=0
```

`CASSINI_SOURCE_AUDIO_INGEST` decides whether collected audio reaches a
transcript. See below.

## Ingestion, and how to switch it off

Capture and intake only collect. Substituting a participant's own recording
into the transcript is a judgement about where somebody's words belong, and the
offset half of that judgement still carries client clock skew. That is a real
cost, and this branch pays it on purpose so the whole path gets exercised; an
installation whose clients are not known to be time-synchronised opts out:

```
CASSINI_SOURCE_AUDIO_INGEST=0
```

With that set the operator never passes `--source-audio` to the build, uploads
accumulate, and transcripts are built from the recorded tracks exactly as
before.

Left on, it costs nothing where nothing was collected. The build is handed a
capture root, finds no upload from this room and this call window, and
transcribes the recorded tracks — the same bundle, byte for byte, that it would
have produced with ingestion off.

Selection is scoped to the recording: a capture must be from the same Talk room
**and** from a call whose wall-clock window overlaps this one. Matching on
participant id alone was wrong in two ways that both end with one meeting's
speech in another's transcript — a later unrelated capture hid the correct
older one, and two calls close in time each looked plausible.

A participant with several tracks in one recording (a rejoin, a stream
rotation) has their spliced track attached to exactly one of them; the others
are dropped from transcription, because that track spans the whole timeline and
already contains their recorded audio, so transcribing both would emit every
word twice.

### The upload is spliced over the recorded track, not substituted for it

Each uploaded segment replaces the recorded audio over the window it actually
holds audio for, and nowhere else. Everywhere else — a reload gap, a late start,
a segment that cannot be placed or whose audio contradicts its own sidecar — the
recorded track stands.

That is what makes ingestion safe without a completeness threshold in front of
it. Substituting a participant's whole track meant every span an upload did not
cover became digital silence with the recorded audio already suppressed, so
words the recorder had heard perfectly well disappeared. A 90% coverage gate
guarded against that and has been removed with it: a threshold could only choose
*which* participants lost speech, and it refused exactly the people this feature
exists for — someone whose page reloaded on a bad connection, whose capture
therefore has a hole in it, and who was not in the call during that hole anyway.
A splice can never be worse than not ingesting at all, so there is nothing left
to refuse.

### A reload mid-recording is one capture, not two

People reload during meetings, most of all on the connections this feature is
for. The page on its way out seals its buffer and deliberately does not upload:
a request started during unload cannot finish, and whether it happened to land
would decide whether the next page found anything to resume.

The next Talk page settles every buffer in that browser. One for another room,
or for a recording Talk says is over, uploads as before — that is the retry path
a reload without a rejoin ends on. One for *this* room while a recording is still
active is held, and the capture that page starts **adopts** it: the same OPFS
directory, the same call start, segment numbering continuing past the highest
index already there. The reload becomes a segment boundary, which is a seam the
pipeline already understands because a mid-call microphone change produces one,
and the recorder places both sides from their own wall-clock windows. Holding is
bounded four ways: the same room, the same account, a minute of staleness, and a
sixty-second deadline after which the buffer is uploaded after all.

Browser storage belongs to the origin, not to the signed-in session, so on a
shared machine a buffer one person's dead page left behind is still there when
the next person signs in. It is neither resumed nor uploaded by them: the upload
endpoint stamps the *authenticated caller* as the owner, so sending it would
publish one person's voice under another's name. It waits for its own account to
open Talk on that machine again.

What a reload still costs is the tail of the current `MediaRecorder` chunk, at
most about two seconds. And if the storage read that makes this decision has not
finished within a second — which on a healthy browser it does in
milliseconds — the capture starts anyway rather than holding the participant's
microphone hostage to it. The reload then files two captures instead of one;
both reach the server and the recorder splices both, so what is lost is the
tidiness, not the audio.

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
only exists in a browser, so the fast browser suite isolates those mechanics
from the slower full-stack seam.

`cassini-app/e2e/` therefore runs a real Chromium against a stub same-origin
Nextcloud: a script tag models the companion hook, a stub Talk bundle publishes
audio over a real `RTCPeerConnection`, and **a transform on the receiving side
drops a share of the encoded frames** — the lossy-uplink condition the feature
exists for. The tests assert that the loss is real, that the captured copy is
unaffected by it, that the anchors advance monotonically on the sender's clock,
that a mute spell is recorded, that joining alone creates no audio storage,
that confirmed recording-off uploads while the call stays connected, that a
reload mid-recording is adopted into one capture holding both sides of the seam,
that a buffered capture whose recording is over uploads at the next page load,
that Talk's device preview constructs no `MediaRecorder` and writes no storage
even with the room's recording confirmed active, that a recorded call is
captured with nothing stored in the browser, and that the administrator switch
stops and discards a capture already running.

```bash
npm run build:capture -w cassini-app
npm run test:e2e -w cassini-app
./scripts/build-capture-companion.sh --skip-js-build
```

It does not cover real Nextcloud, real Talk, real Janus, AppAPI's proxy and its
header forwarding, or the operator's Go handler. Those gaps are deliberate and
named in the CI job.

The server half is `harness/bin/ci-e2e-installed-exapp-capture.sh`, which
`publish-exapp-image.yml` runs against the exact ExApp image: Nextcloud with
AppAPI/HaRP installs the image, the companion is packaged around the payload
that image carries and enabled on that Nextcloud, and the script then asserts
what a browser would have met — a real Talk call page and Talk's index carry
the payload script and an enabled initial state for a logged-in participant,
Files and Talk's guest page carry nothing, flipping the ExApp config flips the
state, the assets and permission poll answer through the proxy — and what the
payload's upload would have done: a participant's synthetic capture lands
byte-for-byte under the capture root owned by the authenticated user, a retry
replaces it, a non-participant gets 403, an anonymous caller is refused at the
proxy, a malformed sidecar gets the handler's 400 through the proxy, and a
two-segment capture lands whole. It needs no browser because it stands in for
the payload at the one point where its behaviour is fully specified, the
multipart POST in `uploadCapture`.

```bash
IMAGE_REF=cassini-exapp:e2e-v3-cpu-gpu ./harness/bin/ci-e2e-installed-exapp-capture.sh
```

The seam between those legs is
`harness/bin/ci-e2e-browser-call-capture.sh`. It installs the exact image and
its companion into the full harness stack, logs two real Chromium participants
into Nextcloud, and joins both to the real Talk/HPB call with fake media. Talk
starts an official audio recording, and both browsers must therefore capture:
Alice additionally changes microphone mid-call and then reloads her page and
rejoins, so the payload must rotate segments, survive the reload and resume the
same capture rather than filing a second one, while Bob's single-segment capture
is the same path without either complication. The leg accepts success only after each
participant's own browser-observed multipart response accounts for the same
byte-plausible segment set found under that participant's authenticated owner
path on the ExApp, with nothing left in either browser. The administrator, who
never joined, must own nothing.

```bash
IMAGE_REF=cassini-exapp:e2e-v3-cpu-gpu ./harness/bin/ci-e2e-browser-call-capture.sh
```

That leg's acceptance contract is also checked offline, with no Docker and no
browser, so an edit that quietly stops requiring one of those things fails in a
second rather than passing a stack run for the wrong reason:

```bash
./harness/bin/test-browser-capture-contract.sh
```

This real-browser leg is advisory in `publish-exapp-image.yml`, alongside the
server-only capture leg. The stub-browser test remains responsible for lossy
transport, reload, mute, and retry matrices; the server-only leg remains
responsible for proxy refusal and replacement matrices.

## Trying it

Two things have to be true before a single byte is captured, and they are
deliberately independent: collection is enabled on the ExApp — which on this
branch is the default — and the companion app is installed so the payload
reaches Talk at all. Installing the companion is still a deliberate act, and
nothing is captured without it. There is nothing per participant. Once both are
true, every authenticated participant of a recorded call is captured.

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

**2. Leave collection on, or turn it off.** `CASSINI_SOURCE_CAPTURE` is on when
unset, so a registration that says nothing about it collects.

The switch on the ExApp is not the whole story, though, and this is the one step
that catches people out. The companion does not read the ExApp's environment: it
reads `source_capture_enabled` out of AppAPI's ExApp config store, and the only
thing that writes there is the operator's enable-edge callback. AppAPI can mark
a freshly registered app enabled without delivering that callback, which leaves
the value missing — and the payload fails closed on a missing value, so every
Talk page is told capture is off while the ExApp says it is on. Confirm it:

```bash
occ app_api:app:config:get gocassini source_capture_enabled   # want: true
```

If it is missing or `false` while capture is meant to be on, disable and
re-enable the ExApp once (`occ app_api:app:disable gocassini` then
`occ app_api:app:enable gocassini`) and check again; the operator logs
`source capture: synchronized companion initial state enabled=true` when it
lands. `sandbox/wire-cassini.sh` does this for you.

There are two opt-outs, and which you want depends on what you are backing out
of.

To stop collecting, pass `CASSINI_SOURCE_CAPTURE=0` as a deploy option at
registration (`app_api:app:register … --env`). To go on collecting but keep what
is collected out of transcripts, leave capture alone and pass
`CASSINI_SOURCE_AUDIO_INGEST=0` instead.

To back the feature out completely, pass **both**. Capture off stops new
uploads, but it does not erase the ones already on disk: they stay until the
retention sweep reaches them (`CASSINI_CAPTURE_MAX_AGE_HOURS`), and while
ingestion is on, a build that runs in the meantime — a deferred job, or a rerun
of an older recording — will still transcribe from them. Disable the
`cassini_capture` companion as well, so Talk pages stop carrying the payload at
all.

On the demo sandbox, steps 1 and 2 are one command or one workflow dispatch:
`sandbox/wire-cassini.sh --image …`, or the `Deploy Sandbox` workflow with an
`image_tag`. Both switches are on there too; the environment is the opt-out. See
`sandbox/README.md`.

**3. Make a recording.** Nothing else is set up, in the browser or anywhere
else. Join an **authenticated** Talk call (guest pages are not supported — Talk
does not dispatch the hook there), press Talk's **Record** control, talk, and
press **Stop recording**. Cassini starts only after Talk confirms the recording
active and uploads at the confirmed stop; leaving the room performs the same
teardown as a fallback. Every signed-in participant of that call does the same,
in their own browser, under their own account. Uploads land under the
operator's `--capture-root` (`CASSINI_OPERATOR_CAPTURE_ROOT`, default
`<data>/capture`) as `<room>/<user>/<call-start-ms>/`, holding `capture.json`
and the segment files.

Whether the room is told, beyond Talk's own recording indicator, is Talk's
setting to make: `occ config:app:set spreed recording_consent --value 1` makes
Talk ask each participant before a recorded call starts. Cassini does not touch
it.

If nothing arrives, check in this order: the companion is enabled
(`occ app:list | grep cassini_capture`), the operator logged the synchronized
initial state, and `operator/capture/enabled` answers `{"enabled":true}` for a
logged-in user. The client fails closed at every one of those.

## Not done yet

- **Cross-correlation refinement of the offset** (see above). This is the gap
  that decides whether ingestion can be trusted on arbitrary clients, and the
  reason an installation whose clients are not time-synchronised should set
  `CASSINI_SOURCE_AUDIO_INGEST=0`.
- **Rebuild on late upload.** An upload arriving after the meeting was published
  does not trigger a rebuild; only a manual rerun picks it up.
- **Attribution.** The cross-track attribution stage still measures the recorded
  tracks, not the ingested ones. Now that the published audio is spliced too,
  this shows more: a word transcribed from an upload where the recorded track
  was silent — a dropout, a reload gap — is measured against silence and may be
  flagged low-confidence even though it is plainly audible in the published mix.
  Flagged words are excluded from the generated summary, and with
  `CASSINI_ATTRIBUTION_DROP=1` they are removed from the transcript outright, so
  on that setting a word somebody can hear in the meeting can be missing from
  what they read. Until attribution measures the spliced render, an installation
  running ingestion should leave dropping off.
- **Level matching at the seams.** The crossfade removes the click, not a
  loudness step: the browser capture has its own gain and the recorded track has
  whatever the SFU delivered, and nothing equalises the two.
- **Stale playback after a rebuild.** A late upload republishes the meeting as a
  new attempt, and the published audio now changes with it rather than only the
  transcript. The viewer's stale serving of a republished meeting is therefore
  audible — old audio against a new transcript — until the cache refreshes.
- **Verification of an upload against the recorder's own audio** (see above).
- **The second copy of ingested audio.** Captures themselves are now bounded and
  swept — see the capture quota and retention settings — but with ingestion on
  the rendered `_work/sourceaudio/source-<speaker>.wav` travels with the meeting
  bundle into `current/`, which retention never prunes. That copy is unbounded
  and outlives the capture it came from, expiring with neither the capture
  quota nor the capture sweep. Separately, nothing rate-limits a participant:
  the quotas bound how much may be held at once, not how often somebody may
  upload, so churn is unbounded even though volume is not.
- **Erasure.** The administrator switch stops a recording in progress and
  discards its buffer, and that is the only lever anyone has. A participant has
  none: they cannot see what they have uploaded, cannot ask for it back, and
  cannot have it deleted ahead of the sweep except by an administrator removing
  the directory by hand.
- **Silent outcomes at the client.** A capture the server refuses — the terminal
  status allowlist, 400/403/413/415/422 — is deleted from OPFS unsent, and so is
  one that has failed `MAX_UPLOAD_ATTEMPTS` times. Both are deliberate: keeping
  either would re-offer a meeting-sized body on every Talk page load forever,
  with no backoff. But the only trace either leaves is a `console.warn`. The
  participant is not told the recording was destroyed, and nothing server-side
  records that one was dropped before it arrived, so a deployment refusing every
  upload looks the same from the operator as one where nobody ever recorded.
- **Abrupt-page tail.** A reload or crash can lose the not-yet-checkpointed tail
  of the current MediaRecorder chunk (at most about two seconds). Completed
  chunks and their recovery sidecar survive in OPFS; the next Talk page in that
  room resumes them into the capture it starts, or uploads them if the recording
  is over.

  A segment sealed when the participant simply leaves is short of its window
  too, for a different reason. Nothing is dropped there: the capture stamps the
  segment's end only after the recorder has stopped and every chunk it produced
  has been written. But that stamp includes the stopping itself, and the start
  of the window includes the encoder spinning up, so the file holds a little
  less audio than the window it declares — by however long those two took.
  Measured on the browser-capture CI leg, between about half a second and a
  second and a half, and it grows with how loaded the machine is.

  The splice leaves out a segment holding under 90% of its declared window, so
  on a segment shorter than about twenty seconds that latency can be a tenth of
  the window and cost the segment its splice although nothing went wrong. The
  recorded track stands there instead and the build log says which segment and
  why — but the seconds either side of a departure are then the ones least
  likely to come from the participant's own microphone. Stamping the window from
  the audio actually recorded, rather than from the clock either side of it,
  would close it.
- **A disabled ExApp still loads the payload.** The companion is a separate
  native app and reads `source_capture_enabled` from AppAPI's ExApp config,
  which outlives disabling the ExApp, so the script tag keeps appearing on Talk
  call pages until the companion itself is disabled.

  It records nothing: the payload's own permission check goes through the AppAPI
  proxy, which a disabled ExApp does not serve, and an unanswered check counts
  as no. So the cost is a script tag, not audio — but that safety rests entirely
  on the check failing closed, which is a second line, not the first.

  Disabling the ExApp *does* now write `false` into that config. The write
  cannot happen inside the lifecycle request — like `UIRegistrar` it calls back
  into Nextcloud, which deadlocks a single-worker PHP setup — so it is issued
  after the response and shutdown waits a few seconds for it, and edges carry a
  sequence number claimed on arrival so a slow enable cannot overwrite a later
  disable. Until D-698 that write ran on a context AppAPI cancelled by stopping
  the container, so it never landed at all and the stored value stayed `true`
  indefinitely. It is a race the operator bounds rather than one it wins, so to
  back the feature out completely, disable `cassini_capture` as well.
- **Firefox raw-audio path.** `MediaStreamTrackProcessor` is Chrome/Safari only.
  The timing path (`RTCRtpScriptTransform`) works in all three engines; the
  current capture path uses `MediaRecorder`, which is universal.
- **E2EE calls.** A sender has one transform slot and Talk's end-to-end
  encryption already uses it (`src/utils/e2ee/JitsiE2EEContext.js`). Capture
  still records; it produces no RTP anchors.
- **Non-browser participants.** Mobile Talk apps have no equivalent hook.
