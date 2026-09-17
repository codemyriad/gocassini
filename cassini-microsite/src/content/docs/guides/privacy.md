---
title: Privacy and data processing
description: What Cassini stores, where it lives, what leaves your infrastructure and when, and what happens on deletion.
source: docs/privacy.md
copied: "2026-09-17"
---

This page is the single reference for an administrator deciding whether Cassini
is acceptable for their instance: what data Cassini stores, where it lives, what
(if anything) leaves your infrastructure, and what happens on deletion.

Cassini records Nextcloud Talk meetings, transcribes them, optionally summarises
them, and publishes a readable archive. Recording and transcription happen
entirely within your own infrastructure. The only steps that send data to a
third party are the ones that call a language model — the automatic meeting
summary, and an insight somebody asks for in the app. Both are optional, both
are off until you configure an endpoint, and both send text, never audio.

## Summary

| Step | Where it runs | Data leaves your infrastructure? |
| --- | --- | --- |
| Recording the call | Local (the Cassini container) | No |
| Transcription (speech-to-text) | Local (Parakeet / Silero VAD models) | No |
| Speaker labels | Local (from Talk signalling, not audio analysis) | No |
| Meeting summary | A language-model endpoint — **only if one is configured** | **Only if the endpoint is external** |
| Insight (a question asked of selected meetings) | A language-model endpoint — **only if one is configured**, and only when somebody asks | **Only if the endpoint is external** |
| Publishing the archive | Nextcloud Files, on your servers | No |

**Without an endpoint configured, nothing leaves your infrastructure.** The
local transcript is still produced and published; the summary is skipped, and
the app offers no way to run an insight. A self-hosted endpoint keeps both on
your own network too.

## What Cassini stores

A recorded meeting moves through capture → build → publish, and each stage
writes artifacts:

- **Recordings** — the raw multitrack capture of the call (`recording.mkv`), one
  audio track per participant.
- **Audio** — the processed meeting audio, ultimately the portable single-file
  `.opus`.
- **Transcripts** — a timestamped word-level transcript.
- **Captions** — a `captions.vtt` subtitle track.
- **Summaries** — an optional `summary.md`, produced only when the language-model
  step is enabled.
- **Insight runs** — one row per insight requested over a set of meetings: who
  asked, which meetings, which workflow, **the question text itself** where one
  was typed, the status, the failure message where one failed, and where the
  answer was written. The answer itself is an ordinary file in the asker's
  Nextcloud Files, not an artifact on the app volume; the row, question included,
  stays on the app volume until that volume is deleted.
- **Manifests** — internal bundle descriptors recording each artifact's kind,
  state, and integrity hashes.
- **Logs** — per-attempt operator logs.
- **Operator database** — job and attempt history plus insight-run records,
  including any typed question. It does not store recording audio, transcripts,
  summaries, or insight answer bodies.

## Where it is stored

**Working artifacts and the operator database** live on the app's own AppAPI
persistent volume, under the operator work root. They are not exposed to
Nextcloud users and are not covered by Nextcloud's file access controls — they
are internal to the Cassini container.

**Published recordings** are written to **Nextcloud Files**, into a dedicated
`cassini` service account's Files. Which path depends on who can see recordings,
and the two are different places with different audiences:

```text
  anyone with a           CassiniNoACL/Recordings/
  Nextcloud account
                          the `cassini` account's own directory. Nothing is
                          mounted there and no other account has a mount of it,
                          so it appears in nobody else's Files; Cassini reads it
                          as that account and serves it through the app to
                          everyone who can open the app.

  meeting participants    Cassini/Recordings/
                          inside the `Cassini` Team folder, which every account
                          has a read mount of. What each account may actually
                          open is decided per recording by Nextcloud's advanced
                          file access controls.
```

One root holds the archive and the other is empty, except while a switch is
copying between them.

### Who can read a published recording

**One setting decides it**, in the app under **Operator › Settings › Who can see
recordings**. The full model is in
[Who can see a recording](/docs/guides/who-can-see-a-recording); the summary
matters here.

**Everyone with a Nextcloud account.** Every recording is readable by every
signed-in account that can open Cassini (never anonymously). Recordings live in
the `cassini` service account's own directory — one no other account has a mount
of — and Cassini serves them as that account, so there is no per-recording
permission to enforce, and none is claimed. It needs no extra Nextcloud apps,
which is why it is what a new install records.

**Meeting participants.** Each private recording is readable **only by the people
who had access to the Talk room when it was published** — its attendee list,
which includes people who were invited but never joined, not only those present
on the call. This is enforced by Nextcloud's own advanced file access controls,
not by Cassini keeping a separate copy or its own permission list. Recordings of
**public** Talk rooms are readable by every signed-in account, never anonymously.

**Switching to meeting participants does not retroactively restrict anything.**
Recordings that already existed are copied into the Team folder readable by every
signed-in account: Cassini does not guess who was in a past meeting. Narrowing
them is a deliberate act, per recording, from the Files app. Switching the other
way carries every recording into the private tree with no access rules at all, so
afterwards everyone who can open Cassini can read every recording, including the
ones that had been restricted to a call's participants.

**A switch copies first and deletes afterwards.** Recordings are copied into the
destination, checked as complete, and only then is the old root emptied — so at
no point does the mode Cassini reports name a place the archive is not. Nothing
is exposed early either: while recordings are being copied into the Team folder,
that folder is held readable by the service account alone, and is opened up again
only once every recording inside it states its own audience.

**An upgrade never widens an existing archive.** When the app is enabled, Cassini
reads the two roots and keeps the audience the recordings already have. Only an
install with no participant-only archive to protect starts on "everyone with a
Nextcloud account", and an empty archive has nobody to expose. Widening an
existing archive is an administrator's deliberate act, and the confirmation says
how many recordings it is about to make visible to everyone.

**Recordings migrated from an older version are owner-only.** Installations that
published before recordings moved into Nextcloud Files migrate them with a script
run once by hand. The audience a recording had when it was published cannot be
recovered afterwards, so migrated recordings are readable only by the `cassini`
service account, and access is granted from the Files app. That script's
`--public` flag instead makes **every** migrated recording readable by every
signed-in account — a deliberate, irreversible widening.

Because published recordings live in Nextcloud Files, they follow **Nextcloud's**
retention, backup and deletion, not Cassini's. Deleting a recording from
Nextcloud Files deletes that copy.

## What leaves your infrastructure, and when

Two steps can transmit data off your infrastructure. Both are the same act — one
call to the configured endpoint — and neither happens unless an endpoint is
configured. Nothing is configured by default.

**1. The meeting summary**, produced automatically after a meeting is transcribed
locally. The transcript text is sent to the configured endpoint, and the summary
comes back and is sealed into the published meeting. Nobody asks for it; it is
part of the pipeline, and it is skipped when there is no endpoint or when the
summary step is switched off.

**2. An insight**, when somebody in the app picks meetings and asks a question of
them. This is the first thing in Cassini that sends transcripts to a model on a
person's command, from inside the app, and it is worth stating plainly:

- **What is sent** is the transcript and summary text of the meetings they
  picked, in order, plus the question they typed. It is assembled **as them** — a
  meeting they cannot open in Nextcloud is not in the bundle and cannot be asked
  about — so an insight can never widen what somebody may read.
- **Which endpoint it reaches.** The asker chooses, from the endpoints an
  administrator has registered; they cannot reach one that is not on that list,
  and the list they are shown carries only each endpoint's name. Choosing none
  runs it on the deployment's configured endpoint. A retry re-runs the endpoint
  that was chosen, so a run cannot quietly move to a different third party
  between the ask and the retry.
- **Who it is attributable to.** The call uses the endpoint and API key
  configured for the **instance**, not credentials belonging to the asker. At
  your provider the request therefore arrives as this deployment, and the
  provider cannot distinguish which of your people asked it. Cassini's own
  records do: each run stores who created it. If your provider's terms or your
  own policy require per-person attribution to a third party, this feature does
  not give it to you.
- **Where the answer lands.** The document is written into the asker's **own**
  Nextcloud Files, under their account, and follows Nextcloud's access controls
  from there. It is not written beside the recordings, and it is not shared with
  anyone by Cassini.

If the endpoint is external, its operator processes what it receives under its
own terms; review them before configuring it. Call audio and the recording itself
are **never** sent off your infrastructure for either step: only text, and only
the text of meetings the request is entitled to.

### Controls

- **Configure no endpoint** — no external calls at all, from either step.
  Transcripts are still produced and published locally; summaries are skipped and
  the app offers no way to ask a question.
- **`LLM_BASE_URL`** — point summaries and insights at a self-hosted or
  alternative OpenAI-compatible endpoint. A keyless self-hosted endpoint is
  enough; a key is needed only when the endpoint requires one.
- **Which endpoints exist**, in the app's AI providers settings. Registering a
  provider is the act that says this deployment may talk to that endpoint, and it
  is the whole of what an insight needs. *Every endpoint you register is one an
  insight may reach.* There is no switch that turns insights off while an
  endpoint is registered; removing the endpoints is how they are turned off.
- **Registering your first endpoint switches summarising on**, pointed at it.
  That is deliberate: an install that has just configured an endpoint and still
  publishes meetings without summaries has done the work and not got the feature.
  It happens **once in the deployment's life**, so turning summarising off keeps
  it off. A deployment that wants a model only for questions people ask by hand
  can therefore register an endpoint and switch summarising off. What leaves it
  then is each insight somebody asks for.
- **A local model for insights, a hosted one for summaries (or the reverse).**
  Each step resolves its own endpoint, so one can be pointed at a local model
  without the other. See
  [AI providers, summaries and insights](/docs/guides/ai-providers).
- **`CASSINI_SUMMARY_DISABLED`** — keep the endpoint configured but stop
  summarising meetings. It does not disable insights; leave the endpoint unset if
  the intent is that nothing calls a model at all.

### Checking it, without being an administrator

"No transcript is sent to a language model unless an endpoint is configured" is a
claim the people whose meetings are being recorded should be able to check, and
the AI settings are administrator-only, as they must be, because they carry the
endpoint and any key.

So `GET /operator/setup`, which any logged-in Nextcloud account may read, answers
it directly:

```json
{ "ok": true, "state": "provisioned", "features": { "summaries": false, "insights": false } }
```

- `features.summaries` — a recorded meeting will be summarised, so its transcript
  is sent to the configured endpoint. `false` means no transcript is sent for a
  summary, whoever recorded the meeting.
- `features.insights` — a question asked of selected meetings will reach a
  configured endpoint. `false` means no endpoint is configured, or none is
  switched on for a step to use.

Both are one bit. Neither reports the endpoint, the model or the key, and no
other AI setting is readable without being an administrator.

## What is not sent anywhere

- **Transcription is entirely local.** Speech-to-text runs in-process using local
  Parakeet models and Silero VAD. No audio and no transcript leaves your
  infrastructure for transcription.
- **Speaker labels are not inferred from audio.** They come from Talk's
  signalling server, on participant join events, so no voice analysis is done.
- **No telemetry or analytics.** Cassini does not phone home — it reports nothing
  about you or your meetings.

## Deletion and uninstall

- **Attempt history is pruned by policy.** Per-attempt working artifacts under
  the operator volume are removed according to `CASSINI_ARTIFACT_RETENTION`
  (default `sealed`); the delivered copy in Nextcloud Files is the durable one.
- **A delivered attempt's staging copy is removed once Nextcloud accepts it**, so
  the full recording does not linger on the app volume outside the Nextcloud
  access model.
- **Insight documents are ordinary Nextcloud files** in the account that asked
  for them. Deleting one deletes the answer; the run row on the app volume
  remains — with the question text on it — until the volume is deleted.
- **Published recordings persist in Nextcloud Files** independently of Cassini.
  Removing or disabling the app does not delete them; they are managed as
  ordinary Nextcloud files.
- **Uninstalling the app keeps its data by default.**
  `occ app_api:app:unregister gocassini` removes the container but **keeps** its
  persistent volume. Adding `--rm-data` also deletes that volume, discarding the
  operator database and every working artifact on it. Either way, recordings
  already published to Nextcloud Files are unaffected.

## Related

- [Who can see a recording](/docs/guides/who-can-see-a-recording) — the
  per-recording access-control model in detail.
- [AI providers, summaries and insights](/docs/guides/ai-providers) — what to
  configure and what each step then sends.
- [`docs/exapp-talk-env-vars.md`](https://github.com/codemyriad/gocassini/blob/main/docs/exapp-talk-env-vars.md)
  — every variable, including the language-model knobs.
- [`docs/reference/artifacts-and-filesystem.md`](https://github.com/codemyriad/gocassini/blob/main/docs/reference/artifacts-and-filesystem.md)
  — artifact types, the operator layout, and retention.
