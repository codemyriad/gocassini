# Data processing & privacy

This page is the single reference for an administrator deciding whether Cassini
is acceptable for their instance: what data Cassini stores, where it lives,
what (if anything) leaves your infrastructure, and what happens on deletion.

Cassini records Nextcloud Talk meetings, transcribes them, optionally summarizes
them, and publishes a readable archive. Recording and transcription happen
entirely within your own infrastructure. Exactly one step sends data to a third
party. It is optional, off by default, and enabled only when you set an API key.

## Summary

| Step                           | Where it runs                                             | Data leaves your infrastructure? |
| ------------------------------ | --------------------------------------------------------- | -------------------------------- |
| Recording the call             | Local (the Cassini container)                             | No                               |
| Transcription (speech-to-text) | Local (Parakeet / Silero VAD models)                      | No                               |
| Speaker labels                 | Local (from Talk signaling, not audio analysis)           | No                               |
| Transcript cleanup + summary   | Third-party LLM — **only if `OPENROUTER_API_KEY` is set** | **Yes, when enabled**            |
| Publishing the archive         | Nextcloud Files, on your servers                          | No                               |

**Without an LLM key: nothing leaves your infrastructure.** The raw local
transcript is still produced and published; only the transcript cleanup/summary is
skipped.

## What Cassini stores

A recorded meeting moves through capture → build → publish, and each stage writes
artifacts:

- **Recordings** — the raw multitrack capture of the call (`recording.mkv`), one
  audio track per participant.
- **Audio** — the processed meeting audio, ultimately the portable single-file
  `.opus`.
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
- **Published recordings** are written to **Nextcloud Files** by the default
  `nextcloud-files` publish sink, into a dedicated `cassini` service account's
  Files. **Which path depends on the storage mode**, and the two are different
  places with different audiences:

  ```text
    default mode            CassiniNoACL/Recordings/
                            the `cassini` account's own directory. Nothing is
                            mounted there and no other account has a mount of
                            it, so it appears in nobody else's Files; Cassini
                            reads it as that account and serves it through the
                            app to everyone who can open the app.

    access-controlled mode  Cassini/Recordings/
                            inside the `Cassini` Team folder, which every
                            account has a read mount of. What each account may
                            actually open is decided per recording by
                            Nextcloud's advanced file access controls.
  ```

  This is what people in your Nextcloud open and view, subject to the access
  controls below. One root holds the archive and the other is empty — except
  while a mode switch is copying between them, which is described below.

### Who can read a published recording

**This depends on the storage mode**, chosen by an administrator in the Cassini
app's **Setup** tab. The mode in force is reported by `GET /operator/storage`
and shown on that tab.

**Default mode.** Every recording is readable by every signed-in account that
can open Cassini (never anonymously). Recordings live in the dedicated `cassini`
service account's own `CassiniNoACL/Recordings` — a directory no other account
has a mount of — and Cassini serves them as that account, so there is no
per-recording permission to enforce, and none is claimed. This mode needs no
extra Nextcloud apps, which is why it is what an instance without them gets.

**Access-controlled mode.** Each private recording, in `Cassini/Recordings`
inside the `Cassini` Team folder, is readable **only by the people who had
access to the Talk room when it was published** — its attendee list, which
includes people who were invited but never joined, not only those present on the
call. This is enforced by Nextcloud's own advanced file access controls, not by
Cassini keeping a separate copy or its own permission list. Recordings of
**public** Talk rooms are readable by every signed-in account (never
anonymously). This mode requires the Team folders and Everyone Group apps and a
Team folder an administrator sets up (see
[Recording permissions](./exapp-nextcloud-recordings-permissions.md)).

**Switching to access control does not retroactively restrict anything.**
Recordings that already existed are copied into the Team folder readable by
every signed-in account: Cassini does not guess who was in a past meeting.
Narrowing them is a deliberate act, per recording, from the Files app. Switching
the other way carries every recording into the private tree with no access rules
at all — a copy there is outside any Team folder, where per-file rules do not
exist — so afterwards everyone who can open Cassini can read every recording,
including the ones that had been restricted to a call's participants.

**A switch copies first and deletes afterwards.** Recordings are copied into the
destination, checked as complete, and only then is the old root emptied — so at
no point does the mode Cassini reports name a place the archive is not. If a
switch stops between those steps, both roots hold a copy; the app says so and
offers a one-click tidy-up, and the leftover copy keeps whatever audience it
already had. Nothing is exposed early either: while recordings are being copied
into the Team folder, that folder is held readable by the service account alone,
and is opened up again only once every recording inside it states its own
audience.

**Opting out empties the Team folder but leaves it in place.** It is not
deleted, and its group mappings are not touched. An emptied `Cassini` Team
folder is the normal end state of an opt-out, and switching back later is
immediate.

**An upgrade never widens an existing archive.** Nothing is inferred from the
instance: an install that has never recorded a storage mode starts in `default`.
If the `Cassini` Team folder is mounted and still holds recordings — what an
access-controlled installation looks like to a Cassini that has not been told —
the app **refuses to publish** and says so, rather than starting a fresh archive
in the private tree and leaving the old one unread. An administrator resolves it
by turning access control on in the Setup tab, or by setting
`CASSINI_STORAGE_MODE=access_controlled` and re-enabling the app. No recording
changes audience while that refusal stands.

**Recordings migrated from an older version are owner-only.** Installations that
published before recordings moved into Nextcloud Files migrate them with
`scripts/backfill-nc-files.sh`, run once by hand — a script that applies to the
access-controlled mode only, and refuses to run in the default mode, where the
per-recording rules it writes would mean nothing. The audience a recording had
when it was published cannot be recovered afterwards, so migrated recordings are
readable only by the `cassini` service account, and access is granted from the
Files app. That script's `--public` flag instead makes **every** migrated
recording readable by every signed-in account — the pre-access-control behaviour.
It is a deliberate, irreversible widening: decide before running it.

Because published recordings live in Nextcloud Files, they follow **Nextcloud's**
retention, backup, and deletion — not Cassini's. Deleting a recording from
Nextcloud Files deletes that copy.

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
post-processing.

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
- **Published recordings persist in Nextcloud Files** independently of Cassini.
  Removing or disabling the Cassini app does not delete them; they are managed as
  ordinary Nextcloud files.
- **Uninstalling the app keeps its data by default.**
  `occ app_api:app:unregister gocassini` removes the Cassini container but
  **keeps** its persistent volume. The operator database and any un-pruned
  working artifacts stay until the volume is deleted. Adding **`--rm-data`** also
  deletes that volume, discarding the operator database and every working
  artifact on it (raw recordings and job history included). Either way, recordings
  already published to Nextcloud Files are unaffected.

## See also

- [Recording permissions](./exapp-nextcloud-recordings-permissions.md) — the
  per-recording access-control model in detail.
- [Env-var reference](./exapp-talk-env-vars.md) — every variable, including the
  LLM knobs.
- [Artifacts and filesystem](./reference/artifacts-and-filesystem.md) — artifact
  types, the operator layout, and retention.
