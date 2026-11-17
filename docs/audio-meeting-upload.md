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
   settings, exactly as it does for a Talk recording.
5. The resulting portable meeting is delivered to the recordings archive,
   shared with the audience you chose, indexed for search, and adopted into
   retention. It is filed in the catalog under **Uploaded meetings**.

A long upload waits for live recordings to go idle before it builds, so the
alternate creation path cannot take resources from capture. The build is also
bounded by the recording's length and can be canceled from the Import sheet.

## Requirements

- Transcription uses the same local speech model as a recording. With no model
  installed or transcription switched off, the upload still becomes a meeting
  with audio, but has no transcript and therefore no summary.
- A summary needs a configured summary model, as it does for recordings.
- The uploaded file must have a readable audio stream. Common containers are
  accepted (`.mp3`, `.m4a`, `.wav`, `.aac`, `.flac`, `.ogg`/`.opus`, `.webm`,
  and others ffmpeg reads). Uploads are limited to 4 GiB.
- Only the first audio stream is used. The file is treated as one microphone,
  not a multi-track Talk capture, so the transcript has a single speaker label.
  Cassini does not diarize an uploaded mix.

## Privacy

Transcription stays on the host exactly as it does for a recording. If a summary
model is configured, the transcript text is sent to that endpoint to write the
summary — the same disclosure as a recorded meeting, described in
[privacy.md](privacy.md).

## Related

- Drop a completed portable meeting into the same **Import** sheet to import it.
- [Portable meeting format](portable-meeting-format.md)
- [Core pipeline](core-pipeline.md)
