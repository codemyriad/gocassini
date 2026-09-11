# Data processing & privacy

This page is the single reference for an administrator deciding whether Cassini
is acceptable for their instance: what data Cassini stores, where it lives,
what (if anything) leaves your infrastructure, and what happens on deletion.

Cassini records Nextcloud Talk meetings, transcribes them, optionally summarizes
them, and publishes a readable archive. Recording and transcription happen
entirely within your own infrastructure. The only steps that send data to a third
party are the ones that call a language model — the automatic meeting summary,
and an insight somebody asks for in the app. Both are optional, both are off
until you configure an LLM endpoint, and both send text, never audio.

## Summary

| Step                           | Where it runs                                             | Data leaves your infrastructure? |
| ------------------------------ | --------------------------------------------------------- | -------------------------------- |
| Recording the call             | Local (the Cassini container)                             | No                               |
| Transcription (speech-to-text) | Local (Parakeet / Silero VAD models)                      | No                               |
| Speaker labels                 | Local (from Talk signaling, not audio analysis)           | No                               |
| Meeting summary                | LLM endpoint — **only if one is configured**              | **Only if the endpoint is external** |
| Insight (a workflow run over selected meetings) | LLM endpoint — **only if one is configured**, and only when somebody asks | **Only if the endpoint is external** |
| Publishing the archive         | Nextcloud Files, on your servers                          | No                               |

**Without an LLM endpoint: nothing leaves your infrastructure.** The local
transcript is still produced and published; the summary is skipped, and the app
offers no way to run an insight. A self-hosted endpoint keeps both
on your own network too.

## What Cassini stores

A recorded meeting moves through capture → build → publish, and each stage writes
artifacts:

- **Recordings** — the raw multitrack capture of the call (`recording.mkv`), one
  audio track per participant.
- **Audio** — the processed meeting audio, ultimately the portable single-file
  `.opus`.
- **Transcripts** — a timestamped word-level transcript.
- **Captions** — a `captions.vtt` subtitle track.
- **Summaries** — an optional `summary.md`, produced only when the LLM step is
  enabled.
- **Insight runs** — one row per insight requested over a set of meetings: who asked,
  which meetings, which workflow, **the question text itself** where one was
  typed, the status, the failure message where one failed, and where the answer
  was written. The answer itself is an ordinary file in the asker's Nextcloud
  Files, not an artifact on the app volume; the row, question included, stays on
  the app volume until that volume is deleted.
- **Manifests** — internal bundle descriptors (`cassini.json`, `manifest.json`)
  recording each artifact's kind, state, and integrity hashes.
- **Logs** — per-attempt operator logs (`record.log`, `build.log`, `seal.log`,
  `publish.log`).
- **Operator database** — job and attempt history plus insight-run records,
  including any typed question. It does not store recording audio, transcripts,
  summaries, or insight answer bodies.

## Where it is stored

- **Working artifacts and the operator database** live on the app's own
  **AppAPI persistent volume** (`APP_PERSISTENT_STORAGE`), under the operator
  work root. They are not exposed to Nextcloud users and are not covered by
  Nextcloud's file access controls — they are internal to the Cassini container.
- **Published recordings** are written to **Nextcloud Files** by the default
  `nextcloud-files` publish sink, into a dedicated `cassini` service account's
  Files. **Which path depends on who can see recordings**, and the two are
  different places with different audiences:

  ```text
    anyone with a           CassiniNoACL/Recordings/
    Nextcloud account
                            the `cassini` account's own directory. Nothing is
                            mounted there and no other account has a mount of
                            it, so it appears in nobody else's Files; Cassini
                            reads it as that account and serves it through the
                            app to everyone who can open the app.

    meeting participants    Cassini/Recordings/
                            inside the `Cassini` Team folder, which every
                            account has a read mount of. What each account may
                            actually open is decided per recording by
                            Nextcloud's advanced file access controls.
  ```

  This is what people in your Nextcloud open and view, subject to the access
  controls below. One root holds the archive and the other is empty — except
  while a switch is copying between them, which is described below.

### Who can read a published recording

**One setting decides it**, in the Cassini app under **Operator › Settings ›
Who can see recordings**. Which answer is in force is reported by
`GET /operator/storage`, marked **Current** in that section, and shown to
everyone else as a chip beside the meeting list.

**Everyone with a Nextcloud account.** Every recording is readable by every
signed-in account that can open Cassini (never anonymously). Recordings live in
the dedicated `cassini` service account's own `CassiniNoACL/Recordings` — a
directory no other account has a mount of — and Cassini serves them as that
account, so there is no per-recording permission to enforce, and none is
claimed. It needs no extra Nextcloud apps, which is why it is what a new install
records and what an instance without those apps gets.

**Meeting participants.** Each private recording, in `Cassini/Recordings`
inside the `Cassini` Team folder, is readable **only by the people who had
access to the Talk room when it was published** — its attendee list, which
includes people who were invited but never joined, not only those present on the
call. This is enforced by Nextcloud's own advanced file access controls, not by
Cassini keeping a separate copy or its own permission list. Recordings of
**public** Talk rooms are readable by every signed-in account (never
anonymously). It requires the Team folders and Everyone Group apps and a Team
folder an administrator sets up (see
[Recording permissions](./exapp-nextcloud-recordings-permissions.md)).

**Switching to meeting participants does not retroactively restrict anything.**
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

**An upgrade never widens an existing archive.** When the app is enabled,
Cassini reads the two roots and keeps the audience the recordings already have:
an install whose `Cassini` Team folder holds recordings stays on **meeting
participants**, without asking and without moving anything. Only an install with
no participant-only archive to protect starts on **everyone with a Nextcloud
account**, and an empty archive has nobody to expose. Recordings already
published are unaffected and stay readable under the rules they were published
with; widening them afterwards is an administrator's deliberate act, and the
confirmation says how many recordings it is about to make visible to everyone.

An earlier build wrote `default` down permanently on the first healthy enable
whatever it found, with one latch to catch the shape where the mistake would
have been obvious. That made "every account can read every recording" a decision
about an organisation's meetings that nobody had taken. Reading the archive
before recording an audience is what replaced it.

**Recordings migrated from an older version are owner-only.** Installations that
published before recordings moved into Nextcloud Files migrate them with
`scripts/backfill-nc-files.sh`, run once by hand — a script that applies only
where recordings are visible to meeting participants, and refuses to run where
they are visible to anyone with a Nextcloud account, since the per-recording
rules it writes would mean nothing there. The audience a recording had
when it was published cannot be recovered afterwards, so migrated recordings are
readable only by the `cassini` service account, and access is granted from the
Files app. That script's `--public` flag instead makes **every** migrated
recording readable by every signed-in account — the pre-access-control behaviour.
It is a deliberate, irreversible widening: decide before running it.

Because published recordings live in Nextcloud Files, they follow **Nextcloud's**
retention, backup, and deletion — not Cassini's. Deleting a recording from
Nextcloud Files deletes that copy.

## What leaves your infrastructure, and when

Two steps can transmit data off your infrastructure. Both are the same act — one
call to the configured LLM endpoint — and neither happens unless an endpoint is
configured. Nothing is configured by default.

**1. The meeting summary**, produced automatically after a meeting is
transcribed locally. The transcript text is sent to the configured endpoint —
OpenRouter (`https://openrouter.ai/api/v1`) by default, or whatever
`LLM_BASE_URL` points at — and the summary comes back and is sealed into the
published meeting. Nobody asks for it; it is part of the pipeline, and it is
skipped when there is no endpoint or when the summary step is switched off.

**2. An insight**, when somebody in the Cassini app picks meetings and asks a
question of them. This is the first thing in Cassini that sends transcripts to a
model **on a person's command, from inside the app**, and it is worth stating
plainly rather than leaving to be discovered:

- **What is sent** is the same bundle the app's Prepare panel would hand that
  person to copy: the transcript and summary text of the meetings they picked,
  in order, plus the question they typed. It is assembled **as them** — a
  meeting they cannot open in Nextcloud is not in the bundle and cannot be asked
  about — so an insight can never widen what somebody may read.
- **Which endpoint it reaches.** The asker chooses, in Prepare, from the
  endpoints an administrator has registered — they cannot reach one that is not
  on that list, and the list they are shown carries only each endpoint's name.
  Choosing none runs it on the deployment's configured endpoint. A retry re-runs
  the endpoint that was chosen, so a run cannot quietly move to a different
  third party between the ask and the retry; the one exception is an endpoint
  removed in the meantime, which falls back to the configured one.
- **Who it is attributable to.** The call uses the endpoint and API key
  configured for the **instance**, not credentials belonging to the asker. At
  your LLM provider the request therefore arrives as this deployment, and the
  provider cannot distinguish which of your people asked it. Cassini's own
  records do: each run stores who created it. If your provider's terms or your
  own policy require per-person attribution to a third party, this feature does
  not give it to you.
- **Where the answer lands.** The document is written into the asker's **own**
  Nextcloud Files, under their account, and follows Nextcloud's access controls
  from there. It is not written beside the recordings — that folder is read-only
  to everyone but the `cassini` service account — and it is not shared with
  anyone by Cassini.

If the endpoint is external, its operator processes what it receives under its
own terms; review them before configuring it. Call audio and the recording itself
are **never** sent off your infrastructure for either step: only text, and only
the text of meetings the request is entitled to.

Controls:

- **Configure no LLM endpoint** — no external calls at all, from either step.
  Transcripts are still produced and published locally; summaries are skipped
  and the app offers no way to ask a question.
- **`LLM_BASE_URL`** — point summaries and insights at a self-hosted or
  alternative OpenAI-compatible endpoint instead of OpenRouter. A keyless
  self-hosted endpoint is enough; `OPENROUTER_API_KEY` is needed only when the
  endpoint requires one.
- **Which endpoints exist, in the app's AI providers settings.** Registering a
  provider is the act that says this deployment may talk to that endpoint, and
  it is what an insight needs — the whole of it. Whoever creates an insight
  chooses which registered endpoint answers it; a run that chooses none falls
  back to the endpoint the insight step names, then the summary step's, then any
  registered provider. Either way the rule to hold on to is the same: *every
  endpoint you register is one an insight may reach.* There is no switch that
  turns insights off while an endpoint is registered; removing the endpoint is
  how they are turned off.
- **Registering your first endpoint switches summarising on**, pointed at it.
  That is deliberate: an install that has just configured an endpoint and still
  publishes meetings without summaries has done the work and not got the
  feature. It happens **once**, on the save that takes a deployment from having
  no endpoint to having one — adding a second endpoint, or any later save, never
  switches it back on, so turning it off in Publish pipeline stays turned off.
  A deployment that wants a model for questions people ask by hand and nothing
  else can therefore register an endpoint and switch summarising off, and then
  nothing leaves except the questions people type.
- **A local model for insights, a hosted one for summaries (or the reverse).**
  The summary step and the insight step each resolve their own endpoint, so one
  can be pointed at a local model without the other. Which endpoint an insight
  will actually reach is reported by `GET <cassini>/operator/settings/llm` as
  `effective.insight`, with `inherited = true` whenever it is not the insight
  step's own — which is how an administrator checks it rather than inferring it.
- **`CASSINI_SUMMARY_DISABLED`** — keep the endpoint configured but stop
  summarising meetings. It means "publish meetings without a summary" and so
  does not disable insights; leave the endpoint unset if the intent is that
  nothing calls a model at all.

See [Summarisation & the privacy caveat](./README.md#summarisation--the-privacy-caveat)
and the [env-var reference](./exapp-talk-env-vars.md) for the full set of knobs.

### Checking it, without being an administrator

"No transcript is sent to an LLM unless an endpoint is configured" is a claim
the people whose meetings are being recorded should be able to check, and until
now only an administrator could: the AI settings are ADMIN-only, as they must be,
because they carry the endpoint and any optional key.

So `GET <cassini>/setup`, which any logged-in Nextcloud user may read, answers it
directly:

```json
{ "ok": true, "state": "provisioned", "features": { "summaries": false, "insights": false } }
```

- `features.summaries` — a recorded meeting will be summarised, so its transcript
  is sent to the configured endpoint. `false` means no transcript is sent for a
  summary, whoever recorded the meeting.
- `features.insights` — an insight workflow run over selected meetings will reach
  a configured endpoint, so that is possible on this deployment. `false` means
  no endpoint is configured, or none is switched on for a step to use.

Both are one bit. Neither reports the endpoint, the model, or the key, and no
other AI setting is readable without being an administrator. The Cassini app uses
these same two bits to explain itself rather than offering a control the reader
could not use.

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
- **Insight documents are ordinary Nextcloud files** in the account that asked
  for them. Deleting one deletes the answer; the run row on the app volume
  remains — with the question text on it — until the volume is deleted.
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
