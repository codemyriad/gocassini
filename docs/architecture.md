# Cassini System Architecture

Top-level overview of the Cassini system. For component internals, follow the deep-dive links into each component's `docs/` folder.

## What Cassini is

Cassini records a Nextcloud Talk meeting and turns it into a portable, self-describing artifact that can be reviewed in a browser or shipped as a single file. The product surface is a single CLI:

```bash
./bin/cassini doctor    # validate the environment
./bin/cassini record    # join a Talk room and capture media
./bin/cassini build     # turn a recording into a meeting artifact bundle
./bin/cassini publish   # render artifacts into a static site
./bin/cassini serve     # serve a published site or inspect an artifact
./bin/cassini inspect   # look inside any primary Cassini artifact
./bin/cassini meetings  # read published recordings back out of Nextcloud as a user
```

The CLI is implemented in `cassini-go-recorder/cmd/cassini` and dispatches into the in-tree subsystems described below. From the user's perspective the inputs are a Talk URL or an existing recording; the outputs are a portable `.opus` file and/or a published static site.

## End-to-end flow

```text
  Talk URL
     │
     ▼
  cassini record    ──▶  multitrack .mkv  ──▶  cassini build   ──▶  meeting bundle  ──▶  cassini publish  ──▶  static site
                                                  (transcribe)        (audio + transcript +
                                                                       summary + manifest)
                                                                          │
                                                                          ▼
                                                                       cassini serve
                                                                          │
                                                                          ▼
                                                                      cassini-viewer (browser)
```

Each arrow corresponds to a durable artifact written to disk. For portable `.opus` outputs, failures are recoverable: rerun the same command and Cassini resumes from the last good artifact in the output's workspace. Directory-bundle outputs do not resume — they require an empty (or absent) `--out` directory on every run.

## Components

| Component | Language | Role | Deep-dive |
|---|---|---|---|
| [`cassini-go-recorder/`](../cassini-go-recorder/) | Go | Live capture (Nextcloud Talk → multitrack `.mkv`), offline remux, post-recording transcription/summary pipeline, the `cassini` CLI | [`cassini-go-recorder/docs/architecture-overview.md`](../cassini-go-recorder/docs/architecture-overview.md) |
| `cassini-transcriber/` | Python | **Removed legacy** post-recording transcription pipeline. The package itself is gone; only `cassini-transcriber/docs/` remains for historical reference. Active transcription lives in `cassini-go-recorder/internal/transcribe`. | [`cassini-transcriber/docs/architecture-overview.md`](../cassini-transcriber/docs/architecture-overview.md) |
| [`cassini-publisher/`](../cassini-publisher/) | Shell | Static-site exporter: turns processed meeting bundles into a hosted library | [`cassini-publisher/README.md`](../cassini-publisher/README.md) |
| [`cassini-readable/`](../cassini-readable/) | Shell | Readable-transcript build script, kept around as a debug surface | [`cassini-readable/README.md`](../cassini-readable/README.md) |
| [`cassini-viewer/`](../cassini-viewer/) | Svelte | Browser app for one meeting artifact at a time — playback, transcript, search, summary panel | [`cassini-viewer/docs/architecture-overview.md`](../cassini-viewer/docs/architecture-overview.md) |
| [`harness/`](../harness/) | Mixed | Local development stack: synthetic meetings, fixture generation, smoke tests | [`harness/README.md`](../harness/README.md) |

The recorder is the architectural center of gravity: live capture, the multitrack MKV, and the post-recording transcription pipeline all live there. Other components consume what the recorder produces.

## Key data contracts

These are the artifact contracts that components share. Treat them as the stable boundary; component internals can change as long as they keep producing/consuming these.

| Contract | Owner | Spec |
|---|---|---|
| Multitrack meeting MKV (Cassini MKV-v1) | recorder | [cassini-go-recorder/docs/mkv-format.md](../cassini-go-recorder/docs/mkv-format.md) |
| `.rtplog` packet log + session artifact | recorder | [cassini-go-recorder/docs/formats.md](../cassini-go-recorder/docs/formats.md) |
| Meeting artifact bundle (`meeting.webm`, `transcript.words.v1.json`, optional `transcript.readable.v1.json`, `captions.vtt`, optional `summary.md`, `manifest.json`) | recorder (`internal/transcribe`) | [cassini-go-recorder/docs/transcription-pipeline.md](../cassini-go-recorder/docs/transcription-pipeline.md) |
| V0 summary template (Markdown sections the summary generator must fill) | recorder (`internal/transcribe/templates/summary.v0.md`) | the file itself |
| Portable `.opus` meeting file | recorder (`internal/portable`) | [docs/portable-meeting-format.md](portable-meeting-format.md) |
| Viewer-loaded transcript JSON (`transcript.words.v1`, `transcript.readable.v1`) | recorder (produced) / viewer (validated) | [cassini-viewer/schema/](../cassini-viewer/schema/) |

## Cross-cutting reference

Material that spans components and isn't owned by any single one:

- [Audio glossary](audio-glossary.md) — concepts that recur across recording, remux, transcription, and viewer (containers, codecs, RTP, timestamps, VAD/STT, captions, integrity).
- [Portable meeting format](portable-meeting-format.md) — the `.opus`-as-meeting-container contract.
- [Operations notes](proxmox-jellyfin-nvidia.md) — infra-level setup notes for deployment hosts.

## Reading order for new contributors

1. **System level (this doc)** — what each component does and how they hand off.
2. **Recorder deep-dive** — [cassini-go-recorder/docs/architecture-overview.md](../cassini-go-recorder/docs/architecture-overview.md). The recorder is where most of the complexity lives.
3. **Format contracts** — read [mkv-format.md](../cassini-go-recorder/docs/mkv-format.md) and [transcription-pipeline.md](../cassini-go-recorder/docs/transcription-pipeline.md) before reading any internal code; the artifacts are how components agree on reality.
4. **Viewer** — [cassini-viewer/docs/architecture-overview.md](../cassini-viewer/docs/architecture-overview.md). Smallest component, easiest entry point if you want to ship a UI change.
5. **Audio glossary** — [docs/audio-glossary.md](audio-glossary.md). Skim when you hit a term you're not sure about.

## What this doc deliberately does not cover

- Component internals — see each component's `docs/architecture-overview.md`.
- Live recording packet truth, drift correction, or remux planning — see [cassini-go-recorder/docs/](../cassini-go-recorder/docs/) (`formats.md`, `muxing.md`, `timelines.md`, `architecture-migration-status.md`).
