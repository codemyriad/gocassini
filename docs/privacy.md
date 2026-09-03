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
| Meeting summary                | LLM endpoint — **only if one is configured**              | **Yes, when enabled**            |
| Insight (a question asked of several meetings) | LLM endpoint — **only if one is configured**, and only when somebody asks | **Yes, when asked for**          |
| Publishing the archive         | Nextcloud Files, on your servers                          | No                               |

**Without an LLM endpoint: nothing leaves your infrastructure.** The local
transcript is still produced and published; the summary is skipped, and the app
offers no way to ask a question of a meeting. A self-hosted endpoint keeps both
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
- **Insight runs** — one row per question asked of a set of meetings: who asked,
  which meetings, which workflow, the status, and where the answer was written.
  The answer itself is an ordinary file in the asker's Nextcloud Files, not an
  artifact on the app volume.
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

That third party processes whatever it receives under its own terms; review them
before configuring an endpoint. Call audio and the recording itself are **never**
sent off your infrastructure for either step: only text, and only the text of
meetings the request is entitled to.

Controls:

- **Configure no LLM endpoint** — no external calls at all, from either step.
  Transcripts are still produced and published locally; summaries are skipped
  and the app offers no way to ask a question.
- **`LLM_BASE_URL`** — point summaries and insights at a self-hosted or
  alternative OpenAI-compatible endpoint instead of OpenRouter. A keyless
  self-hosted endpoint is enough; `OPENROUTER_API_KEY` is needed only when the
  endpoint requires one.
- **Per-step endpoints in the app's AI settings** — summaries and insights each
  resolve their own endpoint, so one can be switched off, or pointed at a
  local model, without the other.
- **`CASSINI_SUMMARY_DISABLED`** — keep the endpoint configured but stop
  summarising meetings. It means "publish meetings without a summary" and so
  does not disable insights; leave the endpoint unset if the intent is that
  nothing calls a model at all.

See [Summarisation & the privacy caveat](./README.md#summarisation--the-privacy-caveat)
and the [env-var reference](./exapp-talk-env-vars.md) for the full set of knobs.

### Checking it, without being an administrator

"Nothing leaves your infrastructure unless an endpoint is configured" is a claim
the people whose meetings are being recorded should be able to check, and until
now only an administrator could: the AI settings are ADMIN-only, as they must be,
because they carry the endpoint and its key.

So `GET <cassini>/setup`, which any logged-in Nextcloud user may read, answers it
directly:

```json
{ "ok": true, "state": "provisioned", "features": { "summaries": false, "insights": false } }
```

- `features.summaries` — a recorded meeting will be summarised, so its transcript
  is sent to the configured endpoint. `false` means no transcript is sent for a
  summary, whoever recorded the meeting.
- `features.insights` — a question asked of a set of meetings will reach a
  configured endpoint, so that is possible on this deployment. `false` means no
  endpoint is configured, or none is switched on for a step to use.

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
  remains, recording that the question was asked, until the volume is deleted.
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
