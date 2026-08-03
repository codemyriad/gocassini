# Cassini documentation

Cassini records a Nextcloud Talk meeting, turns it into a self-contained,
browser-readable meeting (audio + transcript + optional summary), and publishes
it for viewing — with no central app required to read the result.

This page is the curated front door. It is ordered by what a new reader most
likely needs, in three tiers:

1. **[Concepts](#1-concepts)** — what Cassini is and how the pieces fit
2. **[Install & configure](#2-install--configure)** — run it on Nextcloud (and locally)
3. **[Reference & grab-bag](#3-reference--grab-bag)** — exact contracts and everything else

> Some install/configuration areas are still thin; those are flagged **`WIP`**
> below rather than filled with invented detail.

---

## Capability facts (read first)

These are the load-bearing facts about what Cassini does today. Everything else
in these docs should be read in light of them.

- **Transcription is 100% local.** Speech-to-text runs in-process via
  sherpa-onnx / ONNX Runtime with NVIDIA **Parakeet** models and **Silero VAD**.
  There is **no third-party or remote transcription** — no audio and no
  transcript leaves the host for the transcription step.
- **Two transcription tiers, both local:** a **CPU** image (default
  `parakeet-tdt-0.6b-v3-int8`, `CASSINI_STT_DEVICE=cpu`) and a separate
  **GPU/CUDA** image (fp32 `parakeet-tdt-0.6b-v3`, `CASSINI_STT_DEVICE=cuda`).
  The device auto-detects when set to `auto` (CUDA if `/dev/nvidia*` is present,
  otherwise CPU).
- **Speaker labels come from signaling, not diarization.** Each participant is a
  separate RTP stream from the Talk **HPB (High Performance Backend) signaling
  server**. Display names arrive on signaling join/participants events, ride
  through the MKV/remux stream titles, and are read back at transcription time.
  No audio-inferred diarization is used.
- **Summarisation is WIP and the only third-party step.** LLM transcript cleanup
  and meeting summaries are optional and run **only** when `OPENROUTER_API_KEY`
  is set. When enabled, the **full transcript text is sent to that third party**
  (OpenRouter or a compatible endpoint) — see the
  [privacy caveat](#summarisation--the-privacy-caveat). No key means both steps
  are silently skipped and the raw local transcript is still published.
- **Self-contained outputs.** A portable single-file `.opus` carries audio +
  transcript + readable transcript (integrity-hashed), and a separate
  **static-site export** (`catalog.json` + `meetings/`; the viewer SPA shell —
  `index.html` + `assets/` — is served from the image by default and embedded
  into the export only on `--rebuild-viewer`)
  is fully viewable with no server or central app. The viewer can also **embed**
  inside the Nextcloud page. Nuance: the summary currently ships as a
  static-site sidecar and is **not yet embedded** in the portable `.opus`
  (see [WIP gaps](#wip-gaps)).

---

## 1. Concepts

Start here if you are about to work with Cassini.

- **[Start here](./start-here.md)** — the shortest orientation: record → build → publish, and the two browser surfaces.
- **[Mental model](./mental-model.md)** — the smallest useful system picture and the file-driven pipeline.
- **[System architecture](./architecture.md)** — the modules, the language each is in, and the data contracts between them.
- **[Core pipeline](./core-pipeline.md)** — the stage-by-stage flow: **record → remux → transcribe → optional LLM cleanup/summary → publish → view**.
- **[Portable meeting format](./portable-meeting-format.md)** — the single self-contained `.opus` meeting file (audio + embedded transcript, integrity-hashed).
- **[Audio & media glossary](./audio-glossary.md)** — containers, codecs, RTP, VAD/STT, timestamps, integrity.

### Why the architecture is split the way it is

- **Nextcloud Talk integration for private and public calls.** Recording is
  driven from the internal **HPB signaling server (required)**, which is also
  what gives speaker identities **without diarization**.
- **Modular, built for performance.** Live capture, remux, and transcription are
  separated so recording does not compete with the Talk service itself.
- **A complete, shareable bundle.** The portable `.opus` plus the static-site
  export and the viewer embed mean a meeting (transcript, and — where present —
  summary) can be read **without a central app**.

### Core flow

```text
Talk room ──▶ record (multitrack .mkv) ──▶ build ──▶ publish ──▶ view
                                            │
              remux speaker tracks ─────────┤
              transcribe (LOCAL: Parakeet/VAD)
              optional LLM cleanup + summary (OpenRouter, third-party)
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
- **[Recording tutorial](./exapp-talk-recording-tutorial.md)** — a manual end-to-end validation walkthrough.
- **[Recording permissions](./exapp-nextcloud-recordings-permissions.md)** — opt-in per-participant access control (`CASSINI_NC_ACCESS_CONTROL`): Team folders + virtual Everyone Group prerequisites, automatic ACL provisioning, and permission management.
- **[Troubleshooting](./exapp-talk-troubleshooting.md)** — install/access issues seen in practice.
- **[Trying the image locally](./exapp-test-locally.md)** — three tiers, from image-only checks to a production-shaped local install.
- **[Releasing Cassini](./release.md)** — maintainer guide: the version ladder, the local `prepare-release.sh` flow, and the GitHub + App Store publish workflow.

### CPU vs GPU image choice

- **CPU** (default): tag `X.Y.Z`. Runs the int8 Parakeet model on CPU. Fine for
  most installs; live capture is CPU-bound regardless of transcription device.
- **GPU/CUDA**: tag `X.Y.Z-cuda`. CUDA-enabled sherpa-onnx + fp32 Parakeet, with
  `CASSINI_STT_DEVICE=cuda` baked in. Set the deploy daemon's **Compute device**
  to CUDA and AppAPI pulls the `-cuda` image automatically. The GPU accelerates
  the **transcription (build) stage** only. Requires the NVIDIA driver +
  Container Toolkit on the engine running the ExApp. See
  [GPU transcription (CUDA)](./exapp-install.md#gpu-transcription-cuda) and, for
  Docker-in-LXC hosts, [Proxmox NVIDIA passthrough](./proxmox-jellyfin-nvidia.md).

### Summarisation & the privacy caveat

Summaries and readable-transcript cleanup are **off by default**. To enable them,
set `OPENROUTER_API_KEY` (optionally `LLM_BASE_URL`, default
`https://openrouter.ai/api/v1`, and `LLM_MODEL`/`SUMMARY_MODEL`, default
`openai/gpt-4o-mini`).

> **Privacy warning.** When these are enabled, the **full local transcript text
> is sent to the configured third party** (OpenRouter or a compatible endpoint)
> for cleanup and summarisation. Only enable this if sending meeting transcripts
> off-host is acceptable for your deployment. Transcription itself never leaves
> the host; only this optional post-processing step does. Set
> `CASSINI_SUMMARY_DISABLED` to keep readable cleanup while turning summaries
> off. With no key, the raw local transcript is still published.

### Local development stack

- **[Quick start](./quick-start.md)** — fastest end-to-end run on your machine (harness + deployment bundle).
- **[Running the local developer stack](./local-developer-stack.md)** — the two-stack topology and storage model.
- **[Operator stack](./operator-stack.md)** — jobs, attempts, workers, promotion.
- **[Configuration reference](./reference/configuration.md)** — deployment, operator, control-panel, and viewer knobs.

> `WIP` — install-time coverage of the standalone Talk **signaling/HPB** setup
> that Cassini records against is thin here; the ExApp guide states the
> requirement and the secrets, but end-to-end signaling bring-up is left to
> upstream Nextcloud Talk docs.

---

## 3. Reference & grab-bag

Kept because it helps a contributor, installer, or user. Read on demand.

### Reference (exact contracts)

- [Operator API](./reference/api.md) — HTTP + SSE surface.
- [Configuration](./reference/configuration.md) — all runtime knobs.
- [Artifacts and filesystem](./reference/artifacts-and-filesystem.md) — `.run` / `.meeting` / `.site` / `.opus` and operator layout.
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
- `cassini-transcriber/docs/` — **legacy**. The `cassini-transcriber` Python
  package has been **removed**; only these docs remain, for historical
  reference. Active transcription lives in `cassini-go-recorder`.

### Proposals & operations notes

- [Multi-transcription portable format — proposal](./proposals/multi-transcription-format.md) and [build plan](./proposals/multi-transcription-format-plan.md).
- [Branch previews](./branch-previews.md) — per-branch viewer deployments.
- [Proxmox Jellyfin NVIDIA passthrough](./proxmox-jellyfin-nvidia.md) — host GPU setup pitfalls.

---

## WIP gaps

Flagged so readers do not mistake intent for current behavior:

- **Summarisation is WIP.** It works, but `summary.md` is **not yet embedded**
  in the portable `.opus` and is **not** in `manifest.json`. It *is* included in
  the static-site bundle as an optional sidecar. A local/privacy-focused
  summariser (avoiding the third-party OpenRouter step) is a separate future
  effort.
- **The portable `.opus` viewer** renders transcript + metadata; it does not yet
  render an embedded summary from `.opus` (published artifact directories do).
- **Per-recording access control is opt-in in production.** The local harness
  enables it by default, but enforced grants still require the documented Team
  folders + Everyone Group apps and instance-specific ACL validation.

## Fast paths

- Get it running end to end: [Quick start](./quick-start.md) → [Mental model](./mental-model.md) → [Operator stack](./operator-stack.md)
- Work on the media pipeline: [Mental model](./mental-model.md) → [Core pipeline](./core-pipeline.md) → [Artifacts and filesystem](./reference/artifacts-and-filesystem.md)
- Work on the browser apps: [Quick start](./quick-start.md) → [Control panel](./components/control-panel.md) → [Viewer](./components/viewer.md)
- Install on Nextcloud: [ExApp install](./exapp-install.md) → [Env-var reference](./exapp-talk-env-vars.md)
</content>
</invoke>
