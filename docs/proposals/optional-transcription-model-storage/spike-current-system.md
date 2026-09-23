---
shaping: true
---

# Model lifecycle spike: existing mechanisms and change points

## Context and goal

Separate optional transcription and model acquisition from application installation and upgrades. Determine what the current repository already supports, where a no-transcription path would break, and which external patterns apply.

Repository inspected at `4ec1539c6cc8ba3b8e90db5139c6b80185c6454d`, on 2026-09-21. That initial spike was source inspection and bounded HTTP probing. It describes the pre-change baseline. D-797 now implements the chosen shape; see the [implementation and validation record](implementation.md) for subsequent downloads, tests, and remaining release checks.

## Questions

| ID | Question |
|---|---|
| Q1 | Where do bundled and downloaded models live, and what survives updates? |
| Q2 | What calls acquire models, and what progress and recovery exist? |
| Q3 | What must change to record, publish, and play without speech recognition? |
| Q4 | How do settings, device admission, and quality map to actual files? |
| Q5 | What should be borrowed from Nextcloud, LocalAI, Ollama, and Hugging Face? |
| Q6 | How can existing installations preserve weights when the image stops carrying them? |
| Q7 | 🟡 How can a connected host prepare a package and an offline target verify and use manually supplied models without internet access? |

Acceptance: the answers identify existing mechanisms, concrete change points, and facts that still require a release experiment.

## Q1: Storage and image packaging

| Evidence | Finding | Consequence |
|---|---|---|
| [CPU Dockerfile](../../../deployment/Dockerfile.exapp), `model-fetcher`, `COPY --from=model-fetcher` | CPU image includes v3 int8 plus Silero under `/opt/cassini/cache`. | Removing only the default model environment variable does not reduce the image. Remove the fetch stage and model copy. |
| [CUDA base](../../../deployment/Dockerfile.exapp.cuda.base), [CUDA app image](../../../deployment/Dockerfile.exapp.cuda), [release workflow](../../../.github/workflows/publish-exapp-image.yml) | CUDA weights, VAD, and native libraries share a content-hash base, reused across commits. | Remove model files from the base as well as the application image contract; keep native libraries and CUDA runtime. |
| [operator `run.go`](../../../cassini-operator/internal/operator/run.go), `--model-cache-root` | Default writable cache is `$APP_PERSISTENT_STORAGE/operator/models`; explicit `CASSINI_CACHE_ROOT` or `--model-cache-root` wins. | Extend the existing root. Do not invent a second default model volume. |
| [model loader](../../../cassini-go-recorder/internal/transcribe/models.go), `EnsureModel` | Bundled root is checked before writable cache. Writable models currently land under `<cache-root>/models/<model-id>/`. | Current default layout has two `models` path components; it is existing behavior, not an instruction to create another nested root. |
| [Compose](../../../deployment/compose.yml) and [operator path defaults](../../../cassini-operator/internal/operator/run.go) | Standalone operator data can live on the named `/var/lib/cassini-operator` volume. | Preserve this fallback and document a persistent mount for explicit external cache roots. A manually unmounted cache cannot survive container replacement. |

AppAPI explicitly documents `APP_PERSISTENT_STORAGE` as persisting between ExApp updates. Cassini already uses it. Uninstall with data removal, deleting the volume, or moving to a different unpopulated volume is outside that guarantee. [Nextcloud deployment documentation](https://docs.nextcloud.com/server/latest/developer_manual/exapp_development/tech_details/Deployment.html)

Docker layers can be reused between images. A new application version does **not necessarily** cause another model transfer. Proving the amount of current waste would require comparing registry layer digests and pulls on representative installations; this spike did not do that. Persistent storage solves a stronger problem than image-layer caching: models become installation data, with optional acquisition and an independent lifetime. [Docker image layers](https://docs.docker.com/get-started/docker-concepts/building-images/understanding-image-layers/)

## Q2: Acquisition and recovery already present

[`models.go`](../../../cassini-go-recorder/internal/transcribe/models.go) already provides:

- A model catalogue and required filenames, including fp32 `encoder.weights` and optional `bpe.vocab`.
- A cache writer lock, completion marker, extraction into a sibling staging directory, and atomic rename after nonempty regular-file checks.
- HTTP timeouts, extraction size limits, a disk reserve, and protections against archive path traversal.
- A separate VAD acquisition path and an administrator's `CASSINI_DISALLOW_MODEL_DOWNLOAD` policy.

It does **not** provide a durable download job, byte progress, checksum verification, immutable artifact revisions, or retained byte-range checkpoints. `extractInto` streams the HTTP response through bzip2 into tar extraction. Progress is a line per extracted file. Normal failure removes staging; later lock holders clean abandoned staging. The completion marker records a URL, not the identity of every file.

[`BuildMeetingArtifact`](../../../cassini-go-recorder/internal/transcribe/transcribe.go) calls `EnsureModel` and `EnsureVAD` after audio mixdown. [`executeBuildCLI`](../../../cassini-operator/internal/operator/build_runtime.go) allows this acquisition inside an admitted meeting build. Model acquisition therefore consumes a build slot, and model-related resource admission happens before acquisition. A settings download should have its own lightweight worker; the later recognizer probe must still respect inference resource limits.

[`SettingsPanel.svelte`](../../../cassini-app/src/SettingsPanel.svelte) explicitly says the first recording downloads an absent model and shows approximate MB. It has no installation job/progress surface.

### Bounded origin probes

Requests used the URLs in the current catalogue. Each GET read one response byte and closed the connection; no model archive was downloaded or retained.

| Artifact | Observation on 2026-09-21 | Interpretation |
|---|---|---|
| v3 int8 archive on GitHub Releases | HEAD: 200, `Content-Length: 487170055`, byte ranges advertised. GET with `Range: bytes=0-0`: 206, `Content-Range: bytes 0-0/487170055`, length 1. | Basic range support demonstrated on this route. This is compressed archive size, about 487 MB, not installed footprint. |
| v3 fp32 archive on `assets.gocassini.codemyriad.io` | HEAD: 403. Range GET: 200, `Content-Length: 2421315109`, byte ranges advertised, no `Content-Range`. | GET is accessible from this environment, about 2.42 GB compressed. This response did not honor the range. HEAD failure alone would have been a false availability diagnosis. |

A client must accept 206 only with the expected offset, total length, and validator. A 200 response to a resume request must start that file afresh, never append it. These probes do not establish all CDN behavior or the cause of the fp32 result. Test nonzero-offset resume and interrupted transfers against the actual archive origin before claiming resume support.

**Subsequent requirement:** Silvio will host downloads on `dist.gocassini.com` and choose bunny.net or Cloudflare himself. The probes above describe today's origins only, not the planned CDN. The implemented catalogue now uses the live CDN's individual seekable-Zstandard files, with both compressed and expanded hashes. Original archive digests remain revision/source identities and verify raw offline archive input. Include the small VAD dependency on the same origin to avoid a hidden runtime fetch from GitHub. CDN provisioning is outside this research.

The repository has fp32/VAD checksums in [synthetic-boundary/models.sha256](../../../cassini-go-recorder/internal/transcribe/testdata/synthetic-boundary/models.sha256). These are useful seeds, not a complete production catalogue: this spike did not hash current remote archives or measure installed footprint. The selected design needs archive digests and download/installed sizes, plus per-file hashes/sizes so an offline target can validate a pack or extracted directory against its own catalogue. The live CDN supplies per-file validation metadata. The implementation uses these files without introducing a general blob store.

## Q3: Optional transcription crosses the file contract

| Change point | Current assumption | Required change |
|---|---|---|
| [`build.go`](../../../cassini-go-recorder/internal/cassini/build.go), [`doctor.go`](../../../cassini-go-recorder/internal/cassini/doctor.go) | Every build resolves STT policy and runs native-runtime/model checks. | Explicit audio-only build mode; resolve it before STT validation. Keep media-tool checks. |
| [`transcribe.go`](../../../cassini-go-recorder/internal/transcribe/transcribe.go) | Build always resolves a backend, acquires model/VAD, recognizes speech, and can summarize. | Split media preparation/finalization from recognition; bypass all STT, attribution, hints, captions, and summary work when skipped. |
| [`resource.go`](../../../cassini-operator/internal/operator/resource.go), [`build_runtime.go`](../../../cassini-operator/internal/operator/build_runtime.go), [`processing_policy.go`](../../../cassini-operator/internal/operator/processing_policy.go) | Admission and overlap policy assume a model/device and inference memory floor. | Audio-only admission retains FFmpeg/CPU/disk limits but bypasses GPU probing and model memory floors in both worker and monitor. |
| [`format.go`](../../../cassini-go-recorder/internal/transcribe/format.go) | Always declares transcript, captions, and STT provenance. | Declare only produced artifacts; record why STT did not run and omit claims of recognition. |
| [`portable_meeting.go`](../../../cassini-go-recorder/internal/cassini/portable_meeting.go), [`portable_resume.go`](../../../cassini-go-recorder/internal/cassini/portable_resume.go) | Reads transcript JSON unconditionally; uses it for speakers; resume requires it. | Preserve media identity and participants in the no-STT output. Make workspace reuse aware of requested processing mode/revision. |
| [portable schema](../../../spec/cassini-portable-meeting-manifest-v1.schema.json), [`manifest_transcripts.go`](../../../cassini-go-recorder/internal/portable/manifest_transcripts.go) | v1 requires at least one transcript descriptor, but allows a zero-word body. | Choose an explicit v1 compatibility payload or a larger format revision. Removing `transcripts` alone is invalid. |
| [`loadArtifact.ts`](../../../cassini-viewer/src/viewer/loadArtifact.ts), [`portable.ts`](../../../cassini-viewer/src/viewer/portable.ts), [`MeetingView.svelte`](../../../cassini-viewer/src/components/MeetingView.svelte) | Loaded artifact has a mandatory transcript/index; playback is gated by it. Empty segments show “No transcript loaded yet.” | Carry skipped state to the viewer and show an intentional no-transcription state with working audio. |
| [static exporter](../../../cassini-viewer/scripts/export-static-meetings.mjs), operator search/insight readers | Export rejects no transcript descriptor; text features consume transcript bodies. | Keep metadata/library visibility. No transcript content to index; explain text-feature unavailability without treating the recording as broken. |

**Recommended bounded compatibility mechanism:** keep a valid zero-word v1 body, label its descriptor `untranscribed`, add explicit `processing.transcription` status, and omit STT provenance. This is serialization only: no speech model, VAD, or transcription step runs. A fresh empty body must be distinguishable from an ASR run that detected no speech. The writer, Go wire structs, packer, exporter, and viewer must explicitly carry the new field; the schema being open does not make Go reserialization preserve it automatically.

Source inspection supports this approach; an end-to-end portable-file fixture, old-reader playback, and search/insight behavior still need testing. A genuinely absent `transcripts` array would require a separate format compatibility decision and broader reader changes.

## Q4: Configuration and execution identity

[`STTSettings`](../../../cassini-operator/internal/operator/settings.go) is persisted next to the job DB in `settings.json`. It currently has quality, device override, transcription vocabulary, search aliases, and auto/user metadata, but no enabled flag. Hardware changes can rederive an automatic tier. Model readiness must not implicitly turn transcription on or start an unrequested model download.

Current mappings are in [`policy.go`](../../../cassini-go-recorder/internal/transcribe/policy.go):

| Quality | CPU | CUDA |
|---|---|---|
| Fast | 110M English int8 CTC | v3 0.6B fp32 |
| Balanced | v3 0.6B int8 | v3 0.6B fp32 |
| Best | v3 0.6B fp32 | v3 0.6B fp32 |

Preserve these labels. Three GPU choices must not create three copies of the same model. The fp32 model bytes are also shared between CPU Best and CUDA; native runtime/device compatibility is separate from artifact identity.

The recorder and operator are separate Go modules. Operator `modelNeedsDownload` currently duplicates the marker name and performs a weaker cache check. Centralize inventory/installation in one recorder-side model-store implementation exposed through structured CLI commands, instead of adding another model catalogue to the operator.

The existing AppAPI [`/operator/settings` route](../../../appinfo/info.xml) covers subpaths but only permits GET/PUT. New POST actions need explicit ADMIN method/path declarations and route handlers. [Existing update research](../../exapp-update-constraints.md) shows route updates require a manifest version bump; new configuration should use persisted settings and working defaults, without adding a required deployment variable.

## Q5: External patterns verified

| Source | Verified behavior | Adaptation |
|---|---|---|
| [Nextcloud llm2](https://github.com/nextcloud/llm2/blob/main/lib/main.py) | `models_to_fetch` destinations use `persistent_storage()` and are passed to `set_handlers`. | Follow the storage convention. Cassini is Go and needs optional post-install configuration, so importing the Python SDK is unnecessary. |
| [AppAPI lifecycle](https://docs.nextcloud.com/server/latest/developer_manual/exapp_development/development_overview/ExAppLifecycle.html) | `/init` can report initialization progress and has a configurable timeout. | Complete app initialization without a model. Use Cassini settings jobs for optional, long-running acquisition. |
| [LocalAI gallery endpoints](https://github.com/mudler/LocalAI/blob/master/core/http/endpoints/localai/gallery.go), [operation status](https://github.com/mudler/LocalAI/blob/master/core/services/galleryop/operation.go) | Enqueues model application and returns job ID/status URL. Status includes phase, bytes, filename, error, and cancellation. | Persist the same useful concepts in Cassini's SQLite job table, independently of a browser request. |
| [Ollama download implementation](https://github.com/ollama/ollama/blob/main/server/download.go), [pull API](https://docs.ollama.com/api/pull) | Digest-based transfer sharing, partial-file metadata, range requests, progress, and rename on completion. | Begin with one sequential worker and one range per file. Cassini owns the job lifetime even after observers disconnect. No multipart downloader needed initially. |
| [Hugging Face cache design](https://huggingface.co/docs/huggingface_hub/guides/manage-cache) | Revision snapshots reference stored file blobs; unchanged files can be reused. | 🟡 Borrow independent revision identity. Defer its per-file blob store because Silvio asked for simplicity. |

These are design references, not dependencies to deploy alongside Cassini. External `main`/`master` links are mutable; claims above describe what was inspected on the research date.

## Q6: First-upgrade migration is out of scope

The new container sees its new image and mounted persistent volume. It cannot normally read the old image's filesystem. A startup “copy bundled models” routine in a model-free image is too late.

Silvio clarified that the team is the only user and no migration support is needed. Ship the model-free image directly. Configure the team's installation once and use the new persistent store thereafter. Do not build a bridge release, automatic legacy-cache migration, old-image exporter, or upgrade-sequence enforcement. Do not automatically delete old cache directories as part of this change. Explicit pack/import for new offline provisioning is a separate requirement, addressed below.

## Q7: Air-gapped provisioning and model packs

Silvio requires installation on a server with no outgoing internet access and accepts a terminal workflow for manually copied model files. He also suggested `cassini models pack` to prepare material for `cassini models import`.

| Evidence | Finding | Consequence |
|---|---|---|
| [`cli.go`](../../../cassini-go-recorder/internal/cassini/cli.go) | There is no supported `models pack`/`models import` command today. | 🟡 Both commands are proposed work, not existing instructions an administrator can already run. |
| [`models.go`](../../../cassini-go-recorder/internal/transcribe/models.go), model marker and `EnsureVAD` | Model cache validation relies on a completion marker and required files; VAD is acquired separately. | 🟡 Dropping arbitrary files into a volume is not a complete provisioning contract. Import must validate a known revision and all dependencies, publish a receipt, and check target-runtime readiness. |
| [AppAPI manifest](../../../appinfo/info.xml), `CASSINI_DISALLOW_MODEL_DOWNLOAD`; [operator configuration](../../../cassini-operator/internal/operator/run.go) | A no-download policy already exists and is declared for deployment. | 🟡 Retain it and apply it to new/resumed network jobs and pack acquisition. Local import must be unconditionally offline, including when the flag is unset. |
| [Model catalogue and required files](../../../cassini-go-recorder/internal/transcribe/models.go), [available checksum seeds](../../../cassini-go-recorder/internal/transcribe/testdata/synthetic-boundary/models.sha256) | Model identity, required files, and device mappings are already known locally, but the production hash catalogue is incomplete. | 🟡 Ship complete pinned hashes/sizes with the app; the target needs no online catalogue. Pack includes VAD, external weights, vocabulary when listed, and source/license notices. |

**Recommended mechanism:** `models pack` uses a compatible target catalogue on a connected host, reuses verified local files or downloads compressed files/VAD from `dist.gocassini.com`, and produces one versioned tar. Its manifest names the original model revision; it is not an authority to override the target's catalogue. Packing does not require target hardware or an inference probe. A CPU host can prepare fp32 model bytes for a CUDA target.

After manual transfer, `models import` verifies the package's identity and payload against the catalogue shipped in the target image, installs into the actual persistent model root, and probes in the target runtime. It constructs no network client and never fetches missing files. It also accepts the supported original archive or exact extracted files plus VAD for administrators who already have those. A copied receipt is not proof of readiness; Settings discovers a successful local receipt and uses the normal explicit activation flow.

Use a canonical extracted-file layout inside the pack so warm packing can reuse installed bytes without reacquiring a discarded archive. The format needs a version, bounded manifest, expected paths/types, hashes/sizes, and the same safe staging/atomic publication rules as online acquisition. No arbitrary model/runtime loader, target operator connection, automatic policy change, or remote catalogue refresh is introduced.

Application image/native-runtime transfer is handled by normal offline deployment. The target image must contain the catalogue, native libraries, and UI assets needed to start and operate without bootstrap downloads. Local Nextcloud connectivity remains available; external LLM configuration is a separate dependency and can remain off. The command contract and examples are specified in [B8](README.md#detail-b8-air-gapped-model-provisioning). The original spike did not execute pack/import. Subsequent D-797 checks exercised warm pack, isolated offline import, real CPU probes, and empty-store audio output; full disconnected Nextcloud deployment remains a release check.

## Remaining evidence before release

| Gate | Required experiment/output |
|---|---|
| G1: Artifact catalogue | 🟡 Pin archive, per-file, and VAD hashes/sizes, installed footprints, required filenames, and source/license notices at immutable `dist.gocassini.com` URLs. Ship these in the target catalogue for offline validation. Test GET, HEAD, validators, and nonzero range resume against Silvio's chosen CDN. |
| G2: Audio-only compatibility | Record/build/pack/publish/play with empty model storage and outbound model downloads denied; verify old and new readers, metadata, skipped-vs-silence status, and text-feature behavior. |
| G3: Persistent reuse | 🟡 Configure once, replace the container with a newer app, and measure zero model requests/bytes. Cover AppAPI and Compose persistence, explicit cache roots, and compatible app rollback. No old-image migration test is required. |
| G4: Runtime and recovery | 🟡 CPU amd64/arm64 and CUDA load probes; resume across restart; settings changed during install; identical concurrent requests; disk full and corruption; app upgrade with unchanged revision and explicit installation of a new revision. |
| G5: Pack/import and air-gap | 🟡 Cold pack downloads/validates all dependencies; warm pack reuses local files with no requests and no source GPU requirement. Manually transfer to a never-connected target, import, activate, transcribe, restart, and upgrade without public network requests. Repeat with raw archives/directories, flag unset/set, missing VAD/weights, catalogue mismatch, tampering, interrupted writes, and actual cache-root overrides. |

Q1–Q7 are answered. G1–G5 retain the release acceptance requirements; the implementation guide separates completed local/CDN checks from remaining deployment experiments.
