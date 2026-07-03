# Glossary

This is a compact glossary for the terms used in these docs.

For a deeper media-focused glossary, see:

- [Audio & media glossary](../audio-glossary.md)

## Product and runtime terms

### Talk room
A Nextcloud Talk meeting URL that Cassini can join and record.

### Harness
The local development and test lab for Nextcloud Talk. It lets you bring up a local Talk stack, create rooms, and run smoke or fixture flows.

### Operator
Cassini’s long-running control-plane service. It persists jobs and attempts and runs record/build/publish through CLI subprocesses.

### Control panel
The browser UI used to start, stop, rerun, and inspect operator-managed jobs.

### Viewer
The browser UI used to read published meetings. It is read-only and static-site-friendly.

### Job
One logical unit of operator-managed work, usually tied to one meeting URL request.

### Attempt
One execution pass for a job. A job can have multiple attempts over time.

### `current/`
The operator’s canonical artifact library containing the latest successful reusable artifacts per job.

## Pipeline terms

### Record
Join a Talk room and capture reusable source media.

### Build
Turn captured media into a structured meeting artifact with audio, transcript, metadata, and optional summary outputs.

### Publish
Turn one or more ready meetings into a static viewer site.

### Rerun
Create a new attempt for an existing job. In the current operator model, reruns are downstream-only and start from the preserved canonical `.run`.

## Artifact terms

### `.run`
The durable output of the record stage.

### `.meeting`
Transient build scratch: an intermediate bundle directory the build stage stages before packing into a portable `.opus`. It is **not** a user-facing deliverable, and its internal manifests (`cassini.json`, `manifest.json`) are not a published contract. Scheduled for retirement.

### `.site`
The durable output of the publish stage: a static site ready for the viewer.

### Portable `.opus`
The one canonical, user-facing Cassini meeting format and the only durable, published contract. A single file containing playable Opus audio plus the embedded `org.cassini.portable-meeting/2` manifest.

### `cassini.json`
The top-level manifest of an internal `.run`/`.meeting`/`.site` bundle directory. An implementation detail of the build/publish plumbing, not a user-facing or published format. The `.meeting` bundle's `cassini.json` (and its sibling `manifest.json`) are transient scratch scheduled for retirement.

### `catalog.json`
The top-level site file that tells the viewer which meetings are available.

## Transcript and meeting-content terms

### Canonical transcript
The word-timed transcript, usually stored as `transcript.words.v1.json`. This is the timing source of truth.

### Readable transcript
An optional cleaned-up transcript representation meant to be easier for humans to read.

### Display transcript
An optional viewer-oriented transcript representation used for richer UI rendering.

### Summary
Optional markdown output describing the meeting in a more digestible form.

### Captions
Timed subtitle-like output, usually written as `captions.vtt`.

## Light media terminology

### Container
A file format that wraps media streams and metadata together. Examples in Cassini include MKV and WebM.

### Codec
The encoding used for the media itself. Cassini commonly works with Opus audio.

### MKV
Matroska container format. Cassini uses it for captured multitrack recording output.

### WebM
A browser-friendly container format. Cassini uses it for built meeting audio such as `meeting.webm`.

### Opus
A modern audio codec used widely in real-time communication and in Cassini’s portable `.opus` output.

## When to go deeper

If you find yourself asking questions like:

- what is RTP?
- what is the difference between container and codec?
- why does Cassini hash decoded PCM audio?

then go to the full media glossary:

- [Audio & media glossary](../audio-glossary.md)
