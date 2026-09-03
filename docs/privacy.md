# Data processing & privacy

This page is the single reference for an administrator deciding whether Cassini
is acceptable for their instance: what data Cassini stores, where it lives,
what (if anything) leaves your infrastructure, and what happens on deletion.

Cassini records Nextcloud Talk meetings, transcribes them, optionally summarizes
them, and publishes a readable archive. Recording and transcription happen
entirely within your own infrastructure. Exactly one step sends data to a third
party. It is optional, off by default, and enabled only when you set an API key.

One optional feature collects a class of personal data the rest of Cassini never
touches: source-side capture records a participant's microphone in their own
browser, before the network. It is off by default, it is an experimental
prototype, and if you are considering it, read
[Participant source-audio capture](#participant-source-audio-capture) before the
rest of this page.

## Summary

| Step                           | Where it runs                                             | Data leaves your infrastructure? |
| ------------------------------ | --------------------------------------------------------- | -------------------------------- |
| Recording the call             | Local (the Cassini container)                             | No                               |
| Source-side capture            | Local (the participant's browser, then the container)     | No                               |
| Transcription (speech-to-text) | Local (Parakeet / Silero VAD models)                      | No                               |
| Speaker labels                 | Local (from Talk signaling, not audio analysis)           | No                               |
| Transcript cleanup + summary   | Third-party LLM — **only if `OPENROUTER_API_KEY` is set** | **Yes, when enabled**            |
| Publishing the archive         | Nextcloud Files, on your servers                          | No                               |

**Without an LLM key: nothing leaves your infrastructure.** The raw local
transcript is still produced and published; only the transcript cleanup/summary is
skipped.

**Source-side capture is off by default**, and two separate things have to be
switched on before any browser records anything. Once they are, every
authenticated participant of every recorded call is captured. It is the only
step that runs code inside a participant's browser, and the only data Cassini
stores that no retention policy covers and no participant can have deleted — see
[Participant source-audio capture](#participant-source-audio-capture) before
turning it on.

## What Cassini stores

A recorded meeting moves through capture → build → publish, and each stage writes
artifacts:

- **Recordings** — the raw multitrack capture of the call (`recording.mkv`), one
  audio track per participant.
- **Audio** — the processed meeting audio, ultimately the portable single-file
  `.opus`.
- **Participant source audio** — only when source-side capture is enabled: what a
  participant's own browser recorded of the microphone track Talk was sending,
  as compressed audio segments plus a JSON sidecar describing their timing. With
  ingestion also enabled, a build renders each accepted upload into a
  full-length per-speaker WAV inside the job's meeting bundle.
- **Transcripts** — a timestamped word-level transcript and an optional
  human-readable transcript.
- **Captions** — an optional `captions.vtt` subtitle track.
- **Summaries** — an optional `summary.md`, produced only when the LLM step is
  enabled.
- **Manifests** — internal bundle descriptors (`cassini.json`, `manifest.json`)
  recording each artifact's kind, state, and integrity hashes.
- **Logs** — per-attempt operator logs (`record.log`, `build.log`, `seal.log`,
  `publish.log`).
- **Operator database** — job and attempt history: which meetings were recorded,
  when, their status, and the paths/hashes of their artifacts. This is metadata,
  not the content itself.

## Where it is stored

- **Working artifacts and the operator database** live on the app's own
  **AppAPI persistent volume** (`APP_PERSISTENT_STORAGE`), under the operator
  work root. They are not exposed to Nextcloud users and are not covered by
  Nextcloud's file access controls — they are internal to the Cassini container.
- **Participant source audio** lives on the same **AppAPI persistent volume**,
  under its own root — `CASSINI_OPERATOR_CAPTURE_ROOT`, by default
  `<persistent volume>/operator/capture` — as
  `<room token>/<user id>/<call start>/`. Like the working artifacts it is
  internal to the container and outside Nextcloud's file access controls.
- **Published recordings** are written to **Nextcloud Files**, under
  `Cassini/Recordings/`, by the default `nextcloud-files` publish sink. This is
  what people in your Nextcloud open and view, subject to the access controls
  below.

### Who can read a published recording

Each private recording is readable **only by the people who had access to the
Talk room when it was published** — its attendee list, which includes people who
were invited but never joined, not only those present on the call. This is
enforced by Nextcloud's own advanced file access controls, not by Cassini keeping
a separate copy or its own permission list.

**Public Talk room recordings are readable by every signed-in Nextcloud account** (never
anonymously). Access control is provisioned automatically; it depends on the Team
folders and Everyone Group apps being present (see
[Recording permissions](./exapp-nextcloud-recordings-permissions.md)).

**Recordings migrated from an older version are owner-only.** Installations that
published before recordings moved into Nextcloud Files migrate them with
`scripts/backfill-nc-files.sh`, run once by hand. The audience a recording had
when it was published cannot be recovered afterwards, so migrated recordings are
readable only by the `cassini` service account, and access is granted from the
Files app. That script's `--public` flag instead makes **every** migrated
recording readable by every signed-in account — the pre-access-control behaviour.
It is a deliberate, irreversible widening: decide before running it.

Because published recordings live in Nextcloud Files, they follow **Nextcloud's**
retention, backup, and deletion — not Cassini's. Deleting a recording from
Nextcloud Files deletes that copy.

None of this applies to participant source audio: it never enters Nextcloud
Files, so no Nextcloud account reaches it and no Nextcloud access control governs
it. See [Participant source-audio capture](#participant-source-audio-capture).

## Participant source-audio capture

This is the one feature that collects personal data the rest of Cassini never
sees: a participant's microphone as their own browser hears it, before Opus
encoding and before the network. It exists because the audio the recorder
receives through the SFU is only whatever survived that participant's uplink — on
a bad connection the words are not degraded, they are absent. See
[Source-side audio capture](./source-audio-capture.md) for how it works.

It is off by default, and it is an experimental prototype. What follows is what
an installation that turns it on actually does today, including the parts that
are not finished.

### Two switches, and no third

Nothing is captured unless both are true, and they are independent:

- **`CASSINI_SOURCE_CAPTURE=1`** on the Cassini ExApp. Off by default. With it
  off the browser assets 404 and the upload endpoint refuses, so nothing is
  collected and no storage is used.
- **The `cassini_capture` companion app is installed and enabled.** It is a
  separate native Nextcloud app; installing or updating Cassini does not install
  it, and without it no capture code reaches Talk's page at all.

There is no third switch, and in particular there is nothing per participant.
**With both of the above on, every authenticated participant of every recorded
call is captured** — everyone whose browser loads Talk's call page while Talk's
own recording is confirmed active. Cassini offers no per-participant control: no
opt-in, no opt-out, no dialog, and no answer of theirs recorded anywhere.

What informs the people in the room is Talk's, not Cassini's: Talk's recording
indicator, which everyone sees for the whole time capture can run, and Talk's
own recording-consent setting (`occ config:app:set spreed recording_consent
--value 1`), which makes Talk ask each participant before a recorded call. If
your installation needs participants to be told or to agree, configure that in
Talk. Cassini neither adds to it nor changes it.

Turning the administrator switch back off reaches a call already in progress. The
payload re-asks the server every thirty seconds and treats an unreachable or
unclear answer as no, so an in-flight recording stops within about half a minute,
that call's buffered audio is discarded rather than uploaded, and the endpoint
refuses whatever a stale client still tries to send.

### What is captured, and when

Cassini does not open a microphone of its own. It records the audio track Talk is
already **sending** — the same signal the SFU encodes, taken one step earlier.
Two consequences matter here:

- **Talk's mute is honoured at the source.** Muting in Talk sets `enabled =
  false` on that very track, and a disabled track delivers silence to every sink,
  so a muted participant is recorded as silence. There is no code path in the
  capture payload that can produce a hot mic.
- **Capture runs only while Talk's own recording is confirmed active** — that is,
  while Talk is already telling everyone in the room that the meeting is being
  recorded. Joining a call collects nothing. A recording a moderator requested
  but that Talk never confirmed collects nothing. Capture stops and uploads when
  Talk confirms the recording stopped.

Only signed-in participants are affected: Talk does not dispatch the injection
hook on its guest pages, and the upload endpoint requires an authenticated user.

### Where it goes

During the call the audio is buffered **in the participant's own browser**, in
the origin private file system for your Nextcloud origin, as compressed audio
segments — WebM/Opus, or MP4 on a browser that records only that — and a JSON
sidecar of timing anchors. It is deleted from there once the server has accepted
it, and also when the server refuses it outright. A delivery that merely failed
is kept for the next Talk page to retry, and given up on after five attempts.

The upload is a `POST` to `operator/capture/upload` through Nextcloud's AppAPI
proxy, so it travels as the signed-in Nextcloud user. Two things are decided by
the server and never read from the request body:

- **Who it belongs to** — the AppAPI-authenticated caller, not the participant id
  the client states. That is the same identity the recorder writes into each MKV
  audio track, which is what lets a build join an upload to a track.
- **Whether it is allowed** — the caller must be a participant of that room,
  checked against Talk acting as that user rather than believed.

It lands under the capture root as `<room token>/<user id>/<call start>/`. A
re-upload of the same call replaces the previous one.

### Who can read it

Nobody, through Nextcloud. Uploads never enter Nextcloud Files, so the
per-recording access controls that govern published recordings neither protect
them nor can be used to share them. They are readable by the Cassini container,
and by anyone with access to the persistent volume or to a backup of it — in
practice, the server's administrators.

The words reach further than the files do. With `CASSINI_SOURCE_AUDIO_INGEST=1`
they become transcript text, and that transcript is published to everyone the
recording is published to — including the sentences the network lost, which
exist in no other copy.

### How long it is kept

Two weeks by default, then it is deleted. A sweep runs when the operator starts
and after each published meeting, and removes captures older than
`CASSINI_CAPTURE_MAX_AGE_HOURS` (336 hours), abandoned part-uploads older than a
day, and copies set aside by a re-upload once the replacement is in place.

How much may accumulate before then is bounded too:
`CASSINI_CAPTURE_OWNER_QUOTA_MB` (2 GiB) per participant and
`CASSINI_CAPTURE_TOTAL_QUOTA_MB` (20 GiB) across the installation, with uploads
refused below `CASSINI_CAPTURE_MIN_FREE_DISK_MB` (4 GiB) of free disk.

Two things this does not do. `CASSINI_ARTIFACT_RETENTION` is a different
setting covering attempt artifacts under `runs/`, and does not apply here. And
deleting the published recording from Nextcloud Files does not touch the audio
it was built from: that is governed by the sweep above, and until it runs the
upload outlives the meeting a reader can see.

With ingestion enabled there is a second copy on the same volume: each accepted
upload is rendered into a full-length per-speaker WAV
(`_work/sourceaudio/source-<speaker>.wav`) inside the job's meeting bundle, which
is promoted into `current/` — the one directory retention never prunes. It is not
published to Nextcloud Files, and nothing deletes it when the meeting is deleted.

Uploads are bounded per participant as well as per request: the 512 MiB ceiling
applies to one request, `CASSINI_CAPTURE_OWNER_QUOTA_MB` (2 GiB) to a person,
and `CASSINI_CAPTURE_TOTAL_QUOTA_MB` (20 GiB) to the installation. What is not
bounded is a rate: nothing limits how often somebody may upload, only how much
may be held at once.

### What a participant can and cannot do

**Can:** what Talk already gives them. Talk's mute is honoured at the source, so
a muted participant is recorded as silence. Talk's recording indicator tells
them the call is being recorded, and with Talk's recording-consent setting on
they are asked before it starts. Not joining, muting, or leaving are the
controls; they are Talk's controls, and they are real.

**Cannot:** anything specific to Cassini. There is no per-participant switch to
turn capture off while staying in a recorded call, because Cassini does not have
one. Nor is there anything to do about audio already uploaded: no list of what
they have uploaded, no withdrawal path, and no way to have one deleted except by
asking an administrator to remove it from the volume.

**Is not told what became of it either.** A recording the server refuses —
capture switched off since, no longer a participant of that room, a payload it
rejects — is deleted from the browser unsent, and so is one whose delivery keeps
failing. Both are the right call; the alternative is a meeting-sized upload
re-offered on every Talk page load forever. But neither is announced anywhere a
participant would look, so nobody in the call can tell whether their audio
reached the server, was discarded, or is still waiting to be sent.

**So the administrator carries this decision alone.** Turning the switch on
captures everybody in every recorded call, and the only thing the room is told
is what Talk tells it. Configure Talk's recording consent, tell participants
about the feature yourself, and treat this as the experimental prototype it is.

## What leaves your infrastructure, and when

The only step that transmits data off your infrastructure is optional LLM
transcript cleanup and summarization. It runs **only when `OPENROUTER_API_KEY` is
set**, and it is unset by default.

When it is enabled, after a meeting is transcribed locally, the full local
transcript text is sent to the configured endpoint — OpenRouter
(`https://openrouter.ai/api/v1`) by default, or whatever `LLM_BASE_URL` points at
— to produce the readable transcript and the summary. That third party then
processes the transcript under its own terms; review them before enabling this.

Call audio and the recording are **never** sent off your infrastructure for the
sake of this step. Only the text transcript is transmitted, and only for
post-processing. That includes participant source audio: the audio itself is
never transmitted, but with `CASSINI_SOURCE_AUDIO_INGEST` on, the words it
recovers are part of the transcript text this step sends.

Controls:

- **Leave `OPENROUTER_API_KEY` unset** — no external calls at all; the raw local
  transcript is still published.
- **`LLM_BASE_URL`** — point cleanup/summaries at a self-hosted or alternative
  OpenAI-compatible endpoint instead of OpenRouter.
- **`CASSINI_SUMMARY_DISABLED`** — keep readable-transcript cleanup but skip the
  summary.

See [Summarisation & the privacy caveat](./README.md#summarisation--the-privacy-caveat)
and the [env-var reference](./exapp-talk-env-vars.md) for the full set of knobs.

## What is _not_ sent anywhere

- **Transcription is 100% local.** Speech-to-text runs in-process using local
  Parakeet models and Silero VAD (ONNX Runtime). No audio and no transcript
  leaves your infrastructure for transcription.
- **Speaker labels are not inferred from audio.** They come from Talk's signaling
  server (participant join events), so no diarization or voice analysis is done.
- **Participant source audio stays here too.** Uploads are stored and read only
  by the operator, and the transcription that consumes them is the same local
  one. No audio, recorded or captured, is sent anywhere for any purpose.
- **No telemetry or analytics.** Cassini does not phone home — it reports nothing
  about you or your meetings.

## Deletion & uninstall

- **Attempt history is pruned by policy.** Per-attempt working artifacts under
  the operator volume are removed according to `CASSINI_ARTIFACT_RETENTION`
  (default `sealed`); the delivered copy in Nextcloud Files is the durable one.
  See [Retention](./reference/artifacts-and-filesystem.md#retention).
- **A delivered attempt's staging copy is removed once Nextcloud accepts it**, so
  the full recording does not linger on the app volume outside the Nextcloud
  access model.
- **Participant source audio is swept on its own schedule.** The capture root
  is not covered by `CASSINI_ARTIFACT_RETENTION`; a separate sweep removes
  uploads older than `CASSINI_CAPTURE_MAX_AGE_HOURS` (two weeks by default) when
  the app starts and after each published meeting. Deleting the job, or the
  published recording in Nextcloud Files, does not bring that forward: an upload
  outlives the meeting a reader can see, until its own age expires.

  With ingestion enabled there is a second copy that this does **not** cover.
  The rendered per-speaker WAV in the job's meeting bundle is kept in the
  `current/` copy, which retention never prunes, so such an installation holds
  each participant's audio twice and only one of them expires.
- **Published recordings persist in Nextcloud Files** independently of Cassini.
  Removing or disabling the Cassini app does not delete them; they are managed as
  ordinary Nextcloud files.
- **Uninstalling the app keeps its data by default.**
  `occ app_api:app:unregister gocassini` removes the Cassini container but
  **keeps** its persistent volume. The operator database and any un-pruned
  working artifacts stay until the volume is deleted. Adding **`--rm-data`** also
  deletes that volume, discarding the operator database and every working
  artifact on it (raw recordings, participant source audio and job history
  included). Either way, recordings already published to Nextcloud Files are
  unaffected.

## See also

- [Recording permissions](./exapp-nextcloud-recordings-permissions.md) — the
  per-recording access-control model in detail.
- [Env-var reference](./exapp-talk-env-vars.md) — every variable, including the
  LLM knobs.
- [Artifacts and filesystem](./reference/artifacts-and-filesystem.md) — artifact
  types, the operator layout, and retention.
- [Source-side audio capture](./source-audio-capture.md) — how participant
  capture works, what it is for, and what is still missing.
