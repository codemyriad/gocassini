# Separating voices on a shared device

Cassini labels speakers by device: each Talk participant records on their own
audio track, and every word on that track is theirs. When several people sit
around one laptop, all of them come out as that laptop's participant. Someone
who can read the meeting can say **"Several people used this device"**. Cassini
then finds the different voices on that participant's own track and splits its
words between them, as "Meeting room laptop · Speaker 1", "… · Speaker 2" and so
on. People can name the voices, say that two of them are the same person, or
undo the split.

This is a proof of concept. Nothing runs automatically, and nothing recognises
a person: the voices are anonymous until someone types a name.

## What happens

1. **Diarize one track.** `cassini speakers diarize` decodes the chosen
   participant's own audio from the recording (`current/<job>.run`) and runs
   [Nemotron 3 Diarization](https://huggingface.co/nvidia/Nemotron-3-Diarization)
   on it, locally on the CPU with two threads. Nothing is re-transcribed. It
   takes about 1–2 s per minute of audio. The result is a list of turns: start,
   end and an anonymous voice number. It is computed once per participant and
   kept, so the voice numbers never move after someone has named them.
2. **Apply the edits.** `cassini speakers apply` rewrites the built `.meeting`
   bundle from its original transcript, the people's edits and the stored
   turns. It runs no model and never touches the audio.
3. **Republish.** The bundle goes through the usual seal and publish. The audio
   is not re-encoded, so the published file keeps its audio identity and every
   tag and mark stays attached.

## The edits document

People's choices are one document, `cassini.speaker-edits.v1`. It is the
desired state, applied to the original transcript every time, so applying it
twice changes nothing and removing an entry undoes it exactly.

```json
{
  "format": "cassini.speaker-edits.v1",
  "revision": 3,
  "splits": [{ "speakerId": "spk_meeting_room_laptop_…" }],
  "merges": [{ "from": "spk_meeting_room_laptop_…~3", "into": "spk_meeting_room_laptop_…~1" }],
  "labels": [{ "speakerId": "spk_meeting_room_laptop_…~1", "label": "Mira" }]
}
```

- `splits` lists the participants whose device was shared. A voice cannot be
  split again.
- `merges` says one voice is the same person as another voice **of the same
  device**. Voices of different devices are already different microphones;
  merging into a voice that is itself merged is refused.
- `labels` names speakers or voices: 1–64 characters, no control characters,
  no leading or trailing space. A label for a voice that does not exist (yet)
  is kept and has no effect, so a name survives undoing and redoing a split.

A split that finds fewer than two voices with words is **inconclusive**: the
device stays one speaker, and the report says so. It is not an error.

## Voice ids: the `~n` convention

Voice *n* of participant `spk_x` has the id `spk_x~n`. Participant ids are
`spk_<slug>_<hex>` and never contain `~`, so the device of any voice is the id
before the last `~`. *n* is the voice's rank by its first turn in the stored
diarizer output. It does not depend on the words, so a rerun of speech
recognition, or undoing and redoing the split, gives the same voice the same
id. A voice the diarizer found but no word landed on keeps its number and is
left out of the speaker list.

Until someone names it, a voice is called `<device label> · Speaker n`.

## What the bundle holds after a split

| File | Content |
| ---- | ------- |
| `transcript.words.v1.json` | The separated transcript. Its `speakers[]` lists the speakers that have words, with voices in place of the split device. |
| `transcript.raw-asr.words.v1.json` | The original transcript, byte for byte. It is the base of every later apply and is never overwritten. |
| `speaker-edits.json` | The edits document as applied. |
| `captions.vtt` | Regenerated from the separated transcript. |
| `summary.md` | Rewritten for the edited speakers when the bundle has a summary (one call to the summary model). |
| `summary.raw-asr.md` | The build's summary, byte for byte, kept the first time the summary is rewritten. |
| `manifest.json` | See below. `wordCount` is unchanged. |

The summary model is called before apply writes anything, and only when
`summary.md` is not already the one apply wrote for these exact edits: a
retried apply, or a replay onto a bundle that already has them, reports
`"summary":"unchanged"` and costs nothing. A rebuilt bundle has the build's
fresh summary and no record of a rewrite, so a rerun's replay rewrites it.

If no summary model is set up, or the call fails, `summary.md` is left as it
was, the reason goes to stderr (the operator's attempt log), and the apply
report says `"summary":"stale"`; the People panel shows that as a note.
Applying the same edits again tries again. Undoing every edit puts the build's
summary back.

`transcript.display.v1.json` and `transcript.readable.v1.json`, when present,
are removed: they carry their own copies of speaker labels, and the viewer
derives both from the words.

`manifest.json` changes in three places:

- `files.transcripts` lists `separated-voices` (the primary file, default) and
  then `raw-asr` (the original). Each carries a copy of
  `provenance.speechToText`; `separated-voices` adds `x-speakerDiarization`.
  Transcripts of additional models follow these two.
- `x-speakerDiarization` records the split: the backend
  (`sherpa-onnx <runtime> Nemotron diarization, CPU, 2 threads`), model name
  and SHA-256, `minDurationOn` / `minDurationOff`, the word assignment rule,
  `sourceSeparation: false` (voices are not separated from each other's sound,
  only their words), the edits revision and SHA-256, and per split the voice
  ids, turn count, number of voices found and whether it was inconclusive.
  Once apply has rewritten `summary.md` it also has `summary`:
  `{"rewritten": true, "model", "sha256", "editsSha256"}`, the SHA-256 of the
  summary it wrote and of the edits it was written for
  (`provenance.meetingSummary` still describes the build's summary, now
  `summary.raw-asr.md`). Counts, ids and hashes only. The bundle's copy also keeps `base`, the manifest
  members as the build wrote them; `base` is never packed.
- `speakerCount` counts each voice in place of its device. Participants who
  said nothing have no roster entry but stay counted, as the build counted them.
  The roster keeps the build's order, with each split device replaced in place
  by its voices in voice order.

An edits document that changes nothing (no applied split, no merge, no label
that changes a name) restores the bundle as the build wrote it: the original
transcript and captions are put back, the raw-asr file, `speaker-edits.json`
and `x-speakerDiarization` are removed, and `manifest.json` is byte-identical
to the build's. The one exception is a display or readable transcript from an
older build: removed by the first split, it stays removed, along with its
`files` key. The current build writes neither.

A bundle built without a transcript (transcription skipped or failed) has no
words to separate or attribute, so apply leaves it exactly as built; a rerun
that transcribes the meeting replays the edits then.

If apply is interrupted, the next apply finishes the job: the raw-asr copy is
written before the manifest changes and removed only after an undo has put the
manifest back, and while the manifest carries no `x-speakerDiarization` apply
reads the build's values from the manifest itself. An undo removes
`summary.raw-asr.md` last of all, so an undo that stopped part way still puts
the build's summary back and says so (`"summary":"restored"`).

## In the portable `.opus`

`cassini pack` needs no special handling. The packed file has:

- the default words transcript `separated-voices` and a second words transcript
  `raw-asr`, which the viewer offers as a variant;
- the global speaker list of the separated transcript, so voices are ordinary
  speakers to every reader (viewer, CLI, agents, search);
- `x-speakerDiarization` on the `separated-voices` speech-to-text provenance.

The `raw-asr` items still name the original device id, which is not in the
global speaker list once its words all went to voices. The device is kept out
of that list on purpose: every reader of `speakers[]` would count it as one
more person. Instead each voice names its device in an optional hint,
`"x-device": {"id", "label"}`, which `cassini pack` fills in from the `raw-asr`
transcript's own speakers. A reader that shows a non-default transcript
accepts speaker ids it does not find in the list, names such a device from
that hint (or, in older files, from a voice's default label without
` · Speaker n`), and lists it where its first voice is. The viewer counts and
lists the meeting's people the same way whichever transcript is shown: each
voice is a person, and a split device is not counted on top of its voices.

## Commands

```bash
# Which speakers a meeting has, which transcript is the default, which edits revision is applied.
cassini speakers show ./meetings/weekly.meeting
cassini speakers show "./Weekly Sync.opus" --json

# Diarize one participant's track. Exit 3 when no model or runtime is available
# (stderr "diarization-unavailable: …"), 4 when the recording has no stream for
# that participant ("speaker-not-found: …").
cassini speakers diarize ./runs/weekly.run --speaker spk_… --out ./turns/spk_….json

# Apply the edits in place. Turns are read from <turns-dir>/<speakerId>.json.
# Exit 5 when a split has no turns file ("turns-missing: <id>"). --json prints
# {"revision","splits":[{"speakerId","voices","inconclusive"}],"missing",
#  "inconclusive","merged","speakerCount","summary"}. A summary that could not be
# rewritten is reported "stale" with the reason on stderr.
cassini speakers apply ./meetings/weekly.meeting --edits ./edits.json --turns-dir ./turns --json
```

The turns file (`cassini.speaker-turns.v1`) holds the participant id, the
stream indexes used, the recording's file name and SHA-256, the model name,
SHA-256 and runtime, the settings, and the turns. No audio and no voice
embedding.

apply fails rather than publish if the bundle's audio changes while it runs.

## The operator API

The installed app reads and saves a meeting's speaker edits at
`annotations/meetings/<meetingId>/speakers`. It is a USER route, and visibility
is checked the same way as for marks: a meeting the caller cannot open answers
404.

- `GET` returns `{available, reason, revision, appliedRevision, state,
  lastError, doc, participants, report}`. `participants` is the original
  roster: the devices that can be split. `state` is `idle`, `applying`,
  `failed` or `unavailable`. When `available` is false, `reason` is one of
  `no-job` (a meeting with no operator job), `no-source-audio`,
  `no-transcript` or `diarization-unavailable`.
- `POST {expectRevision, doc}` stores the next revision and queues a `refine`
  attempt in one transaction. It answers 200 with the `GET` shape, or:
  - 409 `{"error":"revision-conflict","revision":n}`;
  - 409 `busy`, while the job is still building, sealing or publishing;
  - 409 `{"error":"unavailable","reason":…}`, for a meeting that cannot be
    edited at all;
  - 400 `{"error":"invalid","message":…}`;
  - 503 `diarization-unavailable`, when a new split needs a model this
    operator does not have.

A `refine` attempt runs no transcription. It copies `current/<job>.meeting`,
diarizes each newly split participant once from `current/<job>.run` (the
turns are stored write-once in the operator database and reused forever),
runs `cassini speakers apply`, then seals and publishes like any build.
`applied_revision` moves in the same transaction that records the publish.
A normal rerun replays the last applied edits after `cassini build`.

Marks: a refine re-seals the same `meeting.webm`, so the carried marks must
resolve against the new file. If they do not, the publish fails and the
published recording is left as it was. A rerun re-encodes the audio and
keeps the existing rerun rule.

## Installing the model for the proof of concept

The model is not yet in the model catalogue, so `cassini models install` does
not fetch it. Diarization needs:

- the Cassini sherpa-onnx runtime with Nemotron support (`v1.13.7-cassini.6`
  or later; the version string contains `.nemotron-diarization-v2`). Linux
  amd64 builds have it; stock libraries on macOS, Windows and Linux arm32 do
  not, and diarize exits 3 there;
- the decompressed version-2 Nemotron 3 Diarization INT8 ONNX export, in one
  of these places, checked in this order:
  1. `--model <path>` on `cassini speakers diarize`;
  2. `CASSINI_DIARIZATION_MODEL=<path>`;
  3. `<cache root>/models/nemotron-3-diarization-int8/model.int8.onnx`, where
     the cache root is `CASSINI_CACHE_ROOT` or `~/.cache/cassini`.

The model's SHA-256 is recorded with every turn set and in
`x-speakerDiarization`.

## Differences from Cassini for Android

Android runs the same model and the same word assignment rule ("largest union
overlap; nearest turn in gaps; one speaker per word", with the same
`minDurationOn` 0.3 s, `minDurationOff` 0.5 s, activity threshold 0.5 and two
CPU threads), so both produce the same split of one recording. They differ in
what they run on and how they name things:

- **Input.** Android diarizes a whole mixed recording, because a phone has one
  microphone. Cassini diarizes one participant's own track, chosen by a
  person, because every other participant is already separated by device.
- **Ids.** Android numbers anonymous speakers `spk_1`, `spk_2`, … by first
  spoken word. Cassini names voices `<participant>~n` by first diarizer turn,
  so the id stays with the voice across reruns of speech recognition.
- **Variants.** Android appends a new words variant for every pass and keeps
  every earlier one. Cassini keeps exactly two: the original (`raw-asr`) and
  the current separated transcript, rebuilt from the original on every apply.
- **Provenance.** Both write `x-speakerDiarization` on the speech-to-text
  provenance of the separated transcript; the members differ (Android records
  elapsed time and threshold, Cassini records the edits revision and the
  splits).
- **Names.** On Android, names belong to the document on the phone. In Cassini
  they are saved into the published recording's speaker list, visible to
  everyone who can open it.

## Privacy

Separation is local and on demand. See
[Data processing & privacy](./privacy.md).
