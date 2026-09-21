---
shaping: true
---

# Implementation and operator guide

Implemented on `feat/d-797-optional-transcription-models` for [D-797](https://linear.app/code-myriad/issue/D-797/make-transcription-optional-and-persist-models-with-downloads-and), assigned to Silvio and In Progress. Review and CI: [PR #323](https://github.com/codemyriad/gocassini/pull/323). Production rollout has not been performed.

## Configure transcription

Transcription starts **Off**, including when upgrading the team's existing installation. Recording, audio publication, playback, and participant metadata work with an empty model store. There is no automatic migration of old bundled models and no automatic backfill of old meetings.

1. Open Settings and choose the quality/device. CPU Fast is English-only; Balanced uses v3 int8; Best uses v3 fp32. CUDA uses v3 fp32.
2. Press **Download model**. The operator installs the pinned files from `dist.gocassini.com` and shows transferred/reused bytes and download, verification, unpacking, and runtime-check phases. Closing the page leaves the job running.
3. After Ready, press **Enable transcription** or **Use this model**, then **Save settings**. Installation alone does not enable transcription. Cancel and Retry preserve reusable data.
4. Turn transcription off and save to process future meetings as audio only. Installed weights remain available for later use.

If an enabled model is missing or cannot run, Cassini preserves audio and records a skipped/failed transcription outcome. Media and integrity failures remain build failures. Empty-text meetings cannot generate summaries or insights; mixed insight selections must explicitly exclude them.

## Persistent storage and upgrades

The AppAPI default root is `$APP_PERSISTENT_STORAGE/operator/models`. The standalone operator defaults to `<data-root>/models`; `--model-cache-root` or `CASSINI_CACHE_ROOT` can override it. Use the operator's resolved root for all terminal operations. A direct CLI invocation otherwise uses `CASSINI_CACHE_ROOT` or `~/.cache/cassini`.

Keep this directory on a mounted persistent volume and preserve it when replacing the container. Also retain the operator database (installation intent/progress) and `settings.json` (Off/On and the active revision). CPU and CUDA images contain native runtimes but no weights or VAD.

Models live at `models/<model-id>/<source-sha256>/`, with VAD at `vad/<vad-sha256>/`. The shipped catalogue pins both the model files and each model's VAD dependency. App versions never occur in weight paths. A new binary, native library change, or host reboot invalidates its small runtime-check receipt and rechecks local bytes without downloading them. Keep old catalogue entries when introducing a new revision; the active revision is explicitly pinned. There is no automatic model update or deletion.

## Terminal installation

```bash
cassini models list --json --cache-root /persistent/model-store
cassini models install parakeet-tdt-0.6b-v3-int8 \
  --cache-root /persistent/model-store --device cpu --progress-json
```

`--revision` pins an explicit source digest from `models list --json`. The catalogue is embedded in the binary; listing, validation, probing, and import do not retrieve a remote index. Network installation uses only the immutable URLs shipped with the app. `--progress-json` emits one version-1 event per line; progress appears on stderr without that option. `--json` emits a final identity/readiness result.

A successful installation includes VAD and a local load/execution check. Use `--no-probe` to stage bytes on a host that cannot run the target model, then run `cassini models probe <model> --revision <revision> --cache-root <root> --device cpu|cuda` on the target. A CUDA probe requires the CUDA image and visible NVIDIA hardware. Probes and transcription share a filesystem inference lock; the operator also applies its usual CPU, RAM, and VRAM admission policy. Bytes remain installed if a check fails or memory is insufficient.

A direct build is opt-in:

```bash
CASSINI_CACHE_ROOT=/persistent/model-store \
CASSINI_STT_MODEL=parakeet-tdt-0.6b-v3-int8 \
CASSINI_DISALLOW_MODEL_DOWNLOAD=1 \
  cassini build recording.mkv --out Meeting.opus --transcription on --device cpu
```

Use `--transcription off` (the default) for audio only. Builds never acquire missing models. Settings controls the operator's own build environment; installing through the CLI does not change that policy.

## Air-gapped preparation and import

Use the same Cassini version/catalogue on the connected preparation machine and the target. Packing validates bytes and does not require target GPU hardware or enable transcription.

```bash
# Connected preparation machine:
cassini models pack parakeet-tdt-0.6b-v3-int8 \
  --cache-root ./model-preparation-cache \
  --out ./parakeet.model-pack.tar
```

Manually transfer that tar and the appropriate application image/native runtime. For example, copy the tar onto the disconnected server, then place it in the container:

```bash
docker cp ./parakeet.model-pack.tar cassini:/tmp/parakeet.model-pack.tar
# Run in the target container, using its actual persistent cache root:
docker exec cassini sh -c 'cassini models import \
  --from /tmp/parakeet.model-pack.tar \
  --cache-root "$APP_PERSISTENT_STORAGE/operator/models" --device cpu'
```

For a custom or standalone deployment, replace the root with the operator's configured path. Run as the service user or leave imported files readable by that user and the store writable for future receipts/downloads. A read-only bind mount can replace `docker cp` for the source package. Source files are never modified.

Import is **unconditionally local**, even without `CASSINI_DISALLOW_MODEL_DOWNLOAD`. It checks package identity and every file against the target's shipped catalogue, includes VAD, and runs the target CPU/CUDA check. No copied readiness receipt or package-supplied digest can override the catalogue. Settings discovers the result on polling; enable it and save normally.

For a fully offline deployment, set `CASSINI_DISALLOW_MODEL_DOWNLOAD=1` in the operator configuration. This prevents new or recovered installation jobs from making model requests, while allowing local import, probe, activation, and reuse. External LLM features require a local endpoint or must remain disabled. Neither model command fetches an application image or runtime library.

### Import manually copied files

Instead of a pack, transfer a supported original `tar.bz2` or an extracted model directory, plus Silero VAD:

```bash
cassini models import \
  --model parakeet-tdt-0.6b-v3-int8 \
  --revision 5793d0fd397c5778d2cf2126994d58e9d56b1be7c04d13c7a15bb1b4eafb16bf \
  --from /mnt/transfer/parakeet-files \
  --vad /mnt/transfer/silero_vad.onnx \
  --cache-root /persistent/model-store --device cpu
```

The directory may contain the exact decompressed filenames or their official CDN `.zst` files. `--vad` also accepts the official `.zst` file. It may be omitted only if the exact pinned VAD is already verified locally. The fp32 revision requires `encoder.weights` and its listed vocabulary; the published v3 int8 revision does not contain a vocabulary. Import rejects missing files, unexpected package payloads, unsafe paths/links, unsupported revisions, and checksum mismatches without network fallback.

Repeating an import revalidates the source locally and preserves valid published directories. After interruption, retry with the same source. A damaged existing revision is reported with its path and must be moved aside explicitly before reinstalling; the installer never overwrites an active immutable directory.

## Validation and release checks

Implemented tests cover optional recording output, participant preservation, pinned catalogue validation, safe HTTP resume/restart, cancellation, compressed and expanded digests, pack/import tampering and traversal, no-download policy, local reuse, installation job recovery, duplicate requests, device readiness, and explicit activation. The recorder and operator full Go suites and application/viewer tests are run as part of this change.

Real CDN checks on 2026-09-21 downloaded, decompressed, and verified the Fast and Balanced models plus VAD. Both passed CPU load/execution probes. All 12 unique CDN artifacts returned correct nonzero byte ranges and strong digest ETags. A warm Fast pack and import into a separate local store passed with network acquisition disabled; import was also tested with unusable HTTP proxies and no global policy flag. An audio-only portable file passed integrity inspection with an empty store and invalid STT configuration. A fresh import and subsequent Fast transcription also succeeded inside an isolated Linux network namespace with no network interfaces providing internet access and the no-download policy unset. Balanced transcription matched the existing known-text smoke fixture exactly (similarity 1.000). Audio-only output also passed static publication.

The app test suite passes 539 tests and the viewer suite passes 905; both production builds pass. The full recorder, operator and Talk rotator suites pass under the Go race detector and `go vet`; all 12 first-run browser scenarios also pass. The standalone `tsc --noEmit` command reports 68 test/configuration diagnostics on both the unchanged baseline and this branch, with no new diagnostic messages; it is not a passing project check.

The updated image smoke checks require an empty model store, audio-only packing, explicit installation, warm offline reuse, known-text transcription, and real GPU use for CUDA. The PR runs CPU amd64/arm64 and CUDA image builds, NVIDIA transcription and installed Nextcloud compatibility checks. Talk tests install and enable models through the AppAPI ADMIN proxy and assert that preparation alone leaves transcription off. Consult the PR for their current results. An in-place production AppAPI upgrade and a never-connected full Nextcloud deployment remain release validation tasks; local tests do not establish those results.

At release time, follow CONTRIBUTING to bump the app manifest/image versions so AppAPI refreshes the new ADMIN model POST routes. No release version was changed during implementation. The operator database adds migration 0013 for durable model-install jobs.
