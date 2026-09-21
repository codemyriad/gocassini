---
shaping: true
---

# Optional transcription and persistent models — Proposed slices

These are implementation increments for working Shape B in [README.md](README.md), not separate promised releases or an estimate. The affordance IDs and wiring come from that document. Complete the four slices before shipping the whole feature; intermediate demos can use controlled fixtures. No detailed per-slice implementation plans are needed for this research deliverable.

Implementation status: all four slices have code in D-797. See the [operator guide and validation record](implementation.md); platform image, AppAPI upgrade, and full disconnected deployment checks remain release gates.

## Sequence

| Slice | Mechanism | Shape parts | Demo |
|---|---|---|---|
| V1 | Publish playable recordings with transcription off | B1, B2 | Turn transcription off, record a call with no accessible model files, and open the published audio with an explicit skipped state. |
| V2 | 🟡 Install from Settings or copied files | B3, B4, B5, B6, B8 (raw import) | Download from dist.gocassini.com and watch progress to Ready. On a disconnected target, import a copied archive/directory plus VAD and show the same Ready state. |
| V3 | 🟡 Recover acquisition, prepare offline packs, and safely activate | B1, B3, B4, B6, B8 | Recover an interrupted download. Pack a model on a connected machine, manually transfer it, import offline, and enable transcription. Completion of either acquisition path leaves Off unchanged until activation. |
| V4 | 🟡 Ship model-free images with upgrade and air-gap proofs | B5, B7, B8 | Upgrade a configured installation and transcribe with zero model requests. On a never-connected installation, demonstrate both audio-only operation and transcription after local import. |

## V1: Off is a working configuration

Add the no-STT build mode before refactoring acquisition. Media preparation, packing, publishing, metadata, and viewer behavior form one slice. Define the explicit skipped/completed/failed status and the zero-word v1 compatibility body. Keep inference-library dependencies in the runtime, but prove no model file or VAD is opened when Off.

At this stage the On path can still use a preinstalled model fixture; the final explicit activation contract lands in V3. Do not advertise configure-time downloading until V2 exists.

| ID | Affordance added | Control | Wires out | Returns to |
|---|---|---|---|---|
| U1 | Off/On control, initially with preinstalled On fixture | save | N1 | — |
| U6 | Player with completed/skipped/failed transcription state | play/seek/render | N10 | — |
| U7 | Library metadata and text-availability state | render | — | — |
| N1 | Save transcription policy | request | S1 | U1, U2 |
| N6 | Snapshot execution mode and admit media/STT resources appropriately | queued build | N7 | — |
| N7 | Prepare audio, optional STT, finalize status | build | N8 | — |
| N8 | Pack and publish valid portable v1 | call | S4 | — |
| N9 | Load metadata, compatibility body, processing status | open/list | — | U6, U7, N10 |
| N10 | Audio playback | user/media event | S5 | U6 |
| S1 | Persisted execution policy | N1 | — | N1, N2, N6 |
| S4 | Published meeting and catalog | N8 | — | N9 |
| S5 | Playback state | N10 | — | N10, U6 |

U2/N2 are future connections until V2. The tables describe the complete wiring so later increments stay consistent.

Acceptance:

- Off bypasses STT doctor checks, device/model admission, the processing monitor's model memory threshold, backend resolution, model/VAD loading, and all model network access. Media resource limits still apply.
- A recorded call is published and playable with duration, participants, audio integrity, metadata, tags, and marks intact. The artifact has no recognition provenance, captions, summary, or timing claim.
- The current schema validates the zero-word compatibility payload. Old-reader playback and current-reader skipped-state rendering both work. A missing/corrupt transcript in an ordinary artifact still raises an integrity/format error.
- A completed silent transcript is distinguishable from skipped or failed transcription. Existing transcripts stay visible after turning the global switch Off.
- Search/library do not lose the recording. Summary/insights do not send textless meetings to a configured LLM; insight selection explains the unavailable text before a request is sent.

## V2: Download during configuration or import copied files

Add the catalogue and revision store behind a recorder model-store CLI, with versioned JSON progress. The operator creates a dedicated durable queue/worker and exposes ADMIN endpoints. The settings panel shows a real job from explicit selection through download, checksum verification, extraction, and recognizer readiness.

Add local-only `cassini models import` for an official archive or extracted directory and its VAD dependency. Reuse the catalogue, verifier, writer lock, staging/publish rules, and target-runtime probe. The CLI reports its own progress; Settings discovers the resulting receipt without a fake network job or a restart. Package input and `models pack` follow in V3.

Distribution is now live: embed its schema-v2 catalogue, pin model/VAD revisions, and acquire individual seekable Zstandard files with compressed/decompressed hashes. Keep simple revision directories and reuse matching local files without a blob/refcount service. All 12 artifact range/ETag checks and real Fast/Balanced CPU probes passed; image and deployment checks remain.

| ID | Affordance added | Control | Wires out | Returns to |
|---|---|---|---|---|
| U2 | Quality/device, model identity, sizes, readiness | select/render | N2 | — |
| U3 | Download model | click | N3 | — |
| U4 | Phase, bytes, last update, error | render | — | — |
| N2 | Read catalogue and installation/probe state | request/poll | — | U1, U2, U3, U4, U5 |
| N3 | Deduplicate and enqueue | request | S2, N5 | U4 |
| N5 | 🟡 Shared installer/probe and durable network worker | queue/startup/local import | S2 (managed jobs only), S3 | N11 (local import only) |
| S2 | Durable installation intents/status | N3, N4, N5 | — | N2, N4, N5 |
| S3 | Partial compressed files, installed revisions/VAD, receipts | N5 | — | N1, N2, N5, N6, N7 |
| U8 | 🟡 Import local archive/directory with explicit model/revision | command | N11 | — |
| U9 | 🟡 Import phases, errors, revision, and readiness | render | — | — |
| N11 | 🟡 Validate local input and invoke installer without a network client | command | N5 | U9 |

U5/N4 are future connections until V3. All storage is rooted in the existing configured persistent paths.

Acceptance:

- Start responds with a job/status reference without waiting for the download. UI percentage follows bytes; verification/unpacking/checking remain separately visible after download reaches 100%.
- Two requests for the same install share work. Different CUDA quality labels share the same fp32 revision, as do CPU Best and CUDA model bytes.
- Pinned compressed/decompressed file or VAD digest mismatch, invalid archive, missing external weights, source refusal, timeout, and disk exhaustion produce actionable failures and never a Ready model.
- Disk budgeting includes compressed and expanded bytes plus reserve. Probe admission respects RAM/VRAM without gating the earlier network transfer on inference memory.
- ADMIN route/method checks work through the real AppAPI proxy, including on a version-bumped update; non-admin users cannot start downloads.
- `CASSINI_DISALLOW_MODEL_DOWNLOAD` refuses network acquisition while local import, validation, probing, activation, and use remain available. Opening settings or choosing a tier alone causes no transfer.
- Import a copied archive plus VAD with no public network available. Repeat from an extracted directory. The catalogue's archive/per-file hashes and dependency checks apply; a copied completion marker alone never proves readiness. Required fp32 external weights and vocabulary files are checked when that revision lists them.
- Missing VAD succeeds only if the matching verified dependency is already installed. Missing files, wrong hashes/revisions, unsupported devices, and disk exhaustion fail locally without a fallback download, even with the global no-download switch unset. Invalid data never replaces an active model.
- The local command uses the actual persistent cache root and leaves service-readable ownership. Inventory shows a successful import as Ready without inserting a download job. Repeat/import-after-interruption reconciles published data and retained source files; downloads and imports share the writer lock.

## V3: Recover, pack for offline use, and activate deliberately

Finish the recovery behavior, range semantics, Cancel/Retry, and activation rules. Enable/Use accepts only a revision prepared for the selected runtime/device. A download finishing never changes the saved policy. Capture that policy at build admission and make meeting builds resolve installed files only.

Add `cassini models pack` and the `cassini.model-pack.v1` tar contract from B8. A connected preparation host reuses verified local files or downloads the selected catalogue model and VAD from the CDN. It verifies and writes a complete transferable package without probing a GPU or changing its own settings. The target's local import accepts the manually transferred package, checks it against its shipped catalogue, and probes on the actual target runtime.

| ID | Affordance added/extended | Control | Wires out | Returns to |
|---|---|---|---|---|
| U5 | Cancel / Retry | click | N4 | — |
| N4 | Cancel or resume job intent | request | S2, N5 | U4, U5 |
| U1 | Enable/Use prepared revision; Off remains independent | save | N1 | — |
| N1 | Validate prepared choice and pin active revision | request | S1 | U1, U2 |
| N5 | 🟡 Reconcile partial files, receipts, crashes, and probes | queue/startup/local import | S2 (managed jobs only), S3 | N11 (local import only) |
| N6 | Pin revision/device and resolve unavailable-model audio path | queued build | N7 | — |
| U8 | 🟡 Accept transferred package as well as raw local inputs | command | N11 | — |
| N11 | 🟡 Match package identity and all payload hashes to target catalogue | command | N5 | U9 |
| U10 | 🟡 Pack selected catalogue model and dependencies | command | N12 | — |
| U11 | 🟡 Download/packing progress, errors, and package path | render | — | — |
| N12 | 🟡 Acquire/reuse, verify, and atomically write package without inference | command | S6, S7 | U11 |
| S6 | 🟡 Transferable package | N12 | — | N11, after manual copy |
| S7 | 🟡 Preparation host's cache and staging | N12 | — | N12 |

Acceptance:

- Close the browser and reopen it: server-side work continues and the same job/progress appears. A polling failure is distinguishable from a failed download.
- Stop the process partway through a file; restart and recover from actual durable bytes. Verify nonzero-offset Range/If-Range, a server returning 200, changed validators, 416, and incorrect `Content-Range` with controlled fixtures. Verify successful resume through the actual CDN before release.
- Cancel remains cancelled after restart; Retry resumes compatible partial data. A crash after directory promotion but before the DB update recovers from the completion receipt without downloading.
- With the no-download policy set, startup does not resume pending network jobs. Local import still works and needs no catalogue request.
- Download finishes while Off: the app stays Off. Download B while A is active: A continues. Activation of B affects later admissions; an in-flight A build keeps A's revision and files.
- Enabled but missing/incompatible model publishes audio with an explicit configuration warning and no network fetch. A native inference failure after successful audio preparation also preserves a playable recording with failed-STT status.
- Changing hardware or the app/native runtime rechecks readiness rather than downloading identical weights or silently choosing a new revision. Keep the prepared model data if a probe must wait for resources.
- Cold `models pack` acquires the selected model and VAD and reports download/verification/packing progress. Warm packing verifies and reuses local extracted files with zero model requests, even when the compressed source archive is gone. With the no-download policy set, missing bytes fail locally.
- A CPU preparation environment with the target catalogue can pack the fp32 model for a CUDA target without a GPU. Import performs the CUDA readiness probe on the target; a source-host receipt is never accepted as target readiness.
- Pack produces one complete tar with identity, required model files, VAD, and notices. A failed/interrupted pack leaves no apparently complete final output and preserves reusable verified cache data. Disk checks account for download, extraction, and output space.
- Import a manually copied pack without network access and enable it in Settings. A tampered payload, self-consistent manifest with catalogue-mismatched hashes, missing dependency, or unsupported package/revision fails locally. Neither pack nor import changes enabled/active settings automatically.

## V4: Model-free distribution, upgrade reuse, and air-gap proof

Remove Parakeet and VAD fetch/copy steps from the CPU image and CUDA base. Update catalogue packaging, build assumptions, smoke tests, doctor/status wording, and install documentation. Keep the existing AppAPI and Compose persistent roots. Do not create migration helpers or a bridge image.

No new affordances are required: this increment changes packaging and lifecycle behavior behind the same UI.

| Existing IDs exercised | Visible demo | Mechanism changed |
|---|---|---|
| U1, U6, U7; N6–N10; S1, S4, S5 | Fresh small image records and plays with Off | Model-free packaging and media-only lifecycle |
| U2, U4; N2, N5; S2, S3 | New application image still shows installed revision and Ready status | Persistent paths, catalogue/revision stability, readiness reconciliation |
| U1, U6; N1, N6–N10; S1, S3, S4 | Enabled transcription works with the CDN blocked after upgrade | Installed-only resolution and retained runtime dependencies |
| U8–U11; N5, N11, N12; S3, S6, S7 | 🟡 Pack on a connected host, manually transfer, import and transcribe on a never-connected target | Shipped catalogue/runtime, complete dependency package, local validation and readiness |

Acceptance:

- No weights or VAD are present in either final image's layers. Native libraries and the correct CPU/CUDA runtime still load. `/init`, heartbeat, and ordinary health checks succeed with an empty store.
- Install once, change only the application version, and confirm zero model HTTP requests, not just zero received bytes. The same model revision remains selected. Test container recreation and compatible rollback with the retained volume.
- Repeat the persistence proof under real AppAPI and standalone Compose, including an explicitly mounted custom cache root.
- A fresh Off installation performs no model requests. Explicitly install and transcribe on CPU amd64, CPU arm64, and CUDA using the production catalogue/CDN.
- A genuinely new model revision is offered as a separate explicit revision installation; app upgrade does not initiate it. No automatic historical backfill occurs.
- Install the application image on a target that has never had public internet access. Initialization and audio-only recording/playback work before any model is present. Then import a transferred pack and transcribe, restart, and upgrade with zero attempted public DNS/HTTP requests for models, catalogues, or bootstrap dependencies. Local Nextcloud/browser access remains available; external LLM features are disabled or point to a local service.
- Repeat offline provisioning with manually copied raw files plus VAD. Test the no-network guarantee both with the policy flag set and unset. The docs cover container file transfer/mounting, service ownership, actual cache-root overrides, compatible app/catalogue versions, and CPU/CUDA selection; no offline upload UI is required.

## Deliberate exclusions

First-upgrade bundled-model preservation, automatic legacy-cache migration, legacy image extraction, provider selection/provisioning for the CDN, a general content-addressed file cache, automatic model updates, automatic backfill, and model deletion/garbage-collection UI are outside these slices. Explicit model packing and local import are ordinary provisioning and are in scope. The team's one existing installation may configure with a download or import once.
