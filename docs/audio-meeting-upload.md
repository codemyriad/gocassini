# Create a meeting from an audio file

Cassini normally creates a meeting from a Talk call. The same meeting can also
be created from a recording you already have: upload the audio, and Cassini
transcribes it, writes the usual summary (when a model is configured) and
publishes it with audio and transcript in the portable meeting file.

This is the **Import** sheet's audio path. It sits beside importing an existing
Cassini meeting:

- Drop a portable `.opus` meeting or a transcription-only `.json` document to
  import it whole: the file already contains its transcript and summary.
- Drop an audio recording to have Cassini build the meeting here: it is
  transcribed and, when a model is configured, summarised.

## What happens to the upload

1. The browser puts the file in `.CassiniImports/` in your own Nextcloud Files
   and registers the upload with the operator. The upload's kind (`audio`) and
   sharing are fixed at registration.
2. The operator fetches the staged file **as you**, so it can only read what you
   can read, and removes the staging copy after the meeting lands.
3. The first audio stream is remuxed into the one-track Matroska input the
   build pipeline reads. The source codec is not preserved; it is losslessly
   transcoded to FLAC before the meeting's own Opus encode.
4. `cassini build` runs with the deployment's transcription and summary
   settings, under the same admission as a Talk recording's build: one build
   at a time, the speech model check, the free-memory floor, and the device and
   thread limits. When the optional voice separation model
   (`nemotron-3-diarization-int8`) is installed, it separates voices on the
   recording before writing the summary.
5. The resulting portable meeting is delivered to the recordings archive,
   shared with the audience you chose, indexed for search, and adopted into
   retention. It is filed in the catalog under **Uploaded meetings**.

The build waits for live recordings to go idle, and stops and starts again if
a recording begins while it runs, so transcription cannot take resources from
capture. It also waits out a shortage of memory or GPU, as a Talk build does.
The FLAC step in 3 runs before that wait. The build is bounded by the
recording's length and can be canceled from the Import sheet, also while it
waits.

## Requirements

- Transcription uses the same local speech model as a recording. With no model
  installed, transcription switched off, or a transcriber that crashes, the
  upload still becomes a meeting with audio, but has no transcript and
  therefore no summary. The meeting says why.
- A summary needs a configured summary model, as it does for recordings.
- The uploaded file must have a readable audio stream. The Import sheet takes
  `.mp3`, `.m4a`, `.m4b`, `.wav`, `.aac`, `.flac`, `.ogg`/`.oga`/`.opus`,
  `.webm`/`.weba`, `.aif`/`.aiff`, `.wma`, `.amr` and `.3gp`, and any other file
  the browser reports as audio. Uploads are limited to 4 GiB.
- An `.opus` or `.ogg` file that carries Cassini tags is a portable meeting. If
  it cannot be read, it is refused; it is never re-transcribed as plain audio,
  which would lose its transcript, speakers, tags, marks and summary.
- Only the first audio stream is used. Because an uploaded file has one mixed
  track rather than per-participant Talk tracks, Cassini separates voices
  automatically when the optional voice separation model
  (`nemotron-3-diarization-int8`, up to four speakers; see
  [Separating voices on a shared device](speaker-separation.md)) is installed,
  labelling them `Speaker 1`, `Speaker 2`, and so on in order of first spoken
  word. Without that model installed — or when only one voice speaks — the
  transcript has a single `Speaker 1` label.

## Privacy

Transcription stays on the host exactly as it does for a recording. If a summary
model is configured, the transcript text is sent to that endpoint to write the
summary — the same disclosure as a recorded meeting, described in
[privacy.md](privacy.md).

## Related

- Drop a completed portable meeting into the same **Import** sheet to import it.
- [Portable meeting format](portable-meeting-format.md)
- [Core pipeline](core-pipeline.md)
