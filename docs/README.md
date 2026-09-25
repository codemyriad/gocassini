# Cassini documentation

Cassini records a Nextcloud Talk meeting, turns it into a self-contained,
browser-readable meeting (audio + transcript + optional summary), and publishes
it for viewing — with no central app required to read the result.

Start with concepts, installation, or the reference pages:

1. **[Concepts](#1-concepts)** — what Cassini is and how the pieces fit
2. **[Install & configure](#2-install--configure)** — run it on Nextcloud (and locally)
3. **[Reference](#3-reference)** — exact contracts and everything else

The WIP notes identify gaps in installation and configuration coverage.

---

## Current behavior

- **Transcription is 100% local.** Speech-to-text runs in-process via
  sherpa-onnx / ONNX Runtime with NVIDIA **Parakeet** models and **Silero VAD**.
  There is **no third-party or remote transcription** — no audio and no
  transcript leaves the host for the transcription step.
- **Transcription runs on the GPU when there is one, and on the CPU when there
  is not.** The operator resolves the device before each build: CUDA when the
  image carries the CUDA runtime and an NVIDIA device is visible, CPU
  otherwise. A CPU build is correct but much slower, so the resolved device is
  reported in Cassini’s Operator section, in `/operator/status` and in the build log before
  any audio is decoded — it is never a silent substitution. An administrator can
  pin the device (`cpu` or `cuda`); pinning `cuda` on a host that cannot provide
  it blocks the build with an actionable message rather than quietly running on
  the CPU. Transient RAM/VRAM pressure is retried with exponential backoff;
  repeated pressure eventually becomes `build/blocked` instead of retrying
  forever.
- **Speaker labels come from signaling, not diarization.** Each participant is a
  separate RTP stream from the Talk **HPB (High Performance Backend) signaling
  server**. Display names arrive on signaling join/participants events, ride
  through the MKV/remux stream titles, and are read back at transcription time.
  No audio-inferred diarization is used.
- **Optional summaries and insights send text to a configured LLM endpoint.**
  Automatic summaries send the meeting transcript; user-requested insights send
  selected meetings' transcripts and summaries plus the question. The endpoint
  can be hosted or self-hosted. With no endpoint, neither operation calls a
  model and the local transcript is still published. See [privacy](./privacy.md).
- **Self-contained outputs.** A portable single-file `.opus` carries audio +
  transcript (integrity-hashed), and a separate
  **static-site export** (`catalog.json` + `meetings/`; the viewer SPA shell —
  `index.html` + `assets/` — is served from the image by default and embedded
  into the export only on `--rebuild-viewer`)
  can be served independently of Nextcloud. The viewer can also **embed**
  inside the Nextcloud page. When present, the summary is embedded in the
  portable `.opus` alongside the transcript.

---

## 1. Concepts

Start here if you are about to work with Cassini.

- **[Start here](./start-here.md)** — the shortest orientation: record → build → publish, and the two browser surfaces.
- **[Mental model](./mental-model.md)** — the smallest useful system picture and the file-driven pipeline.
- **[System architecture](./architecture.md)** — the modules, the language each is in, and the data contracts between them.
- **[Core pipeline](./core-pipeline.md)** — the stage-by-stage flow: **record → remux → transcribe → optional LLM summary → publish → view**.
- **[Portable meeting format](./portable-meeting-format.md)** — the single self-contained `.opus` meeting file (audio + embedded transcript, integrity-hashed).
- **[Audio & media glossary](./audio-glossary.md)** — containers, codecs, RTP, VAD/STT, timestamps, integrity.

### Why the architecture is split the way it is

- **Nextcloud Talk integration for private and public calls.** Recording is
  driven from the internal **HPB signaling server (required)**, which is also
  what gives speaker identities **without diarization**.
- **Separate capture and processing stages.** Their resource use can overlap.
  The [processing policy](./recording-priority.md) controls when background
  builds may run alongside recordings.
- **A complete, shareable bundle.** The portable `.opus` plus the static-site
  export and the viewer embed mean a meeting (transcript, and — where present —
  summary) can be read **without a central app**.

### Core flow

```text
Talk room ──▶ record (multitrack .mkv) ──▶ build ──▶ publish ──▶ view
                                            │
              remux speaker tracks ─────────┤
              transcribe (LOCAL: Parakeet/VAD)
              optional LLM summary (hosted or self-hosted endpoint)
              pack portable .opus / static site
```

---

## 2. Install & configure

### Production: Nextcloud AppAPI ExApp

- **[Installing Cassini as a Nextcloud ExApp](./exapp-install.md)** — the
  production install guide: deploy daemon (HaRP), image tag choice, app
  registration, Talk recording handoff (reversible), and the verification
  checklist.
- **[Env-var reference](./exapp-talk-env-vars.md)** — every variable the installed
  ExApp reads vs. what AppAPI injects, and the `deployment/` parity vars.
- **[Production deployment notes](./exapp-talk-production-deployment.md)** — deployment shape and operational notes.
- **[One-click install & update constraints](./exapp-update-constraints.md)** —
  what AppAPI's Install/Update buttons can and cannot deliver. Notably: deploy
  env is creation-time only, so a release adding a _required_ env var is a
  breaking change.
- **[Recording tutorial](./exapp-talk-recording-tutorial.md)** — a manual end-to-end validation walkthrough.
- **[Recording permissions](./direct-shares-cutover.md)** — built-in Nextcloud Files shares for each recording, the one-user cutover, and the metadata cache.
- **[Data processing & privacy](./privacy.md)** — what Cassini stores, where it lives, deletion/uninstall implications, and the optional LLM operations that can send text off your infrastructure.
- **[Troubleshooting](./exapp-talk-troubleshooting.md)** — install/access issues seen in practice.
- **[Trying the image locally](./exapp-test-locally.md)** — three tiers, from image-only checks to a production-shaped local install.
- **[Releasing Cassini](./release.md)** — maintainer guide: the version ladder, the local `prepare-release.sh` flow, and the GitHub + App Store publish workflow.

### CPU vs GPU image choice

- **Portable**: tag `X.Y.Z`. Multi-arch (`linux/amd64`, `linux/arm64`). Captures,
  transcribes and publishes on a host with no GPU (supporting x86_64 and 64-bit
  ARM servers). It bakes the model of its default tier (0.6B int8, "Balanced"). Fast
  and Best download once into the model cache on the persistent volume when an
  administrator selects them, so the image stays small and every tier still
  runs. Best on a CPU is slower than the meeting it transcribes, which is why
  Balanced is the default. Moving to the `-cuda` image later is a device change,
  not a data migration: use **Rerun** in Cassini’s Operator section to re-transcribe an
  existing recording on the GPU.
- **GPU/CUDA**: tag `X.Y.Z-cuda`. x86_64 only. CUDA-enabled sherpa-onnx + fp32 Parakeet, with
  `CASSINI_STT_DEVICE=cuda` baked in. Set the deploy daemon's **Compute device**
  to CUDA and AppAPI pulls the `-cuda` image automatically — the device is a
  property of the _daemon_, so a CPU and a GPU install differ by that one
  setting and nothing else. The GPU accelerates
  the **transcription (build) stage**; live capture itself remains CPU-bound.
  Requires the NVIDIA driver +
  Container Toolkit on the engine running the ExApp. See
  [GPU transcription (CUDA)](./exapp-install.md#gpu-transcription-cuda).

### Summarisation & the privacy caveat

Summaries are **off by default**. To enable them, set `LLM_BASE_URL` to any
OpenAI-compatible endpoint — a hosted provider or your own model server — plus
`LLM_MODEL`/`SUMMARY_MODEL` (default `openai/gpt-4o-mini`). Add
`OPENROUTER_API_KEY` when the endpoint needs a key; setting it alone defaults
`LLM_BASE_URL` to `https://openrouter.ai/api/v1`. A self-hosted endpoint with no
authentication works without a key. In an installed app, these values only seed
the first start — after that, summaries are configured in the app's Settings.

> **Privacy warning.** When summaries are enabled, the **full local transcript
> text is sent to the configured endpoint**. Only enable this if sending meeting
> transcripts off-host is acceptable for your deployment. Transcription itself
> never leaves the host. User-requested insights also send selected meetings'
> transcripts and summaries, plus the question, to a configured endpoint. Turning
> summaries off does not disable insights. With no endpoint, the local transcript
> is still published and neither operation calls a model.

### Local development stack

- **[The cassini CLI from a checkout](./cli.md)** — `./bin/cassini`: doctor, record, build, publish, serve, inspect, and the `cassini dev` harness namespace.
- **[Quick start](./quick-start.md)** — fastest end-to-end run on your machine (harness + deployment bundle).
- **[Running the local developer stack](./local-developer-stack.md)** — the two-stack topology and storage model.
- **[Operator stack](./operator-stack.md)** — jobs, attempts, workers, promotion.
- **[Configuration reference](./reference/configuration.md)** — deployment, operator, app, and viewer settings.

> `WIP` — install-time coverage of the standalone Talk **signaling/HPB** setup
> that Cassini records against is thin here; the ExApp guide states the
> requirement and the secrets, but end-to-end signaling bring-up is left to
> upstream Nextcloud Talk docs.

---

## 3. Reference

API, artifact, component and operational details.

### Reference (exact contracts)

- [Operator API](./reference/api.md) — HTTP + SSE surface.
- [Configuration](./reference/configuration.md) — all runtime knobs.
- [Artifacts and filesystem](./reference/artifacts-and-filesystem.md) — `.run` / `.meeting` / `.site` / `.opus` and operator layout.
- [Agent access to meeting recordings](./agent-meeting-access.md) — reading meetings from outside Nextcloud with `cassini meetings`, as a Nextcloud user.
- [Glossary](./reference/glossary.md) — Cassini + media terms.
- [Troubleshooting](./reference/troubleshooting.md) — common local-dev and runtime issues.

### Component pages

- [Component index](./components/README.md)
- [Control panel](./components/control-panel.md) — operator UI behavior.
- [Viewer](./components/viewer.md) — static meeting-reading UI + portable `.opus` and embed modes.
- [Harness](./components/harness.md) — local Talk lab and test harness.

### Module deep-dives (kept in place, linked here)

- [`cassini-go-recorder/docs/`](../cassini-go-recorder/docs/) — live capture, MKV/remux, and the **active** local transcription pipeline (`internal/transcribe/`).
- [`cassini-viewer/docs/`](../cassini-viewer/docs/) — the viewer package.
- [Historical Python transcriber](./history/python-transcriber.md) — retained
  architecture notes for the removed implementation. Active transcription lives
  in `cassini-go-recorder`; historical notes are not installation instructions.

### Proposals & operations notes

- [Branch previews](./branch-previews.md) — per-branch viewer deployments.
- [Repair the published archive](./runbooks/repair-published-archive.md) — the published meetings predate a retired format, so branch previews cannot be built from them (D-739).

---

## WIP gaps

Flagged so readers do not mistake intent for current behavior:

- **Group folders ACL inheritance is version-sensitive.** Limiting a private recording
  to the room’s audience is the audience that requires the Team
  folders and Everyone Group apps plus a Team folder Cassini can set up from Operator › Settings.
  It is worth validating traversal on your own instance — the runbook has a
  checklist.

## Fast paths

- Get it running end to end: [Quick start](./quick-start.md) → [Mental model](./mental-model.md) → [Operator stack](./operator-stack.md)
- Work on the media pipeline: [Mental model](./mental-model.md) → [Core pipeline](./core-pipeline.md) → [Artifacts and filesystem](./reference/artifacts-and-filesystem.md)
- Work on the browser apps: [Quick start](./quick-start.md) → [Control panel](./components/control-panel.md) → [Viewer](./components/viewer.md)
- Install on Nextcloud: [ExApp install](./exapp-install.md) → [Env-var reference](./exapp-talk-env-vars.md)
