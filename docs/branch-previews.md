# Branch previews

Per-branch deployments of the Cassini viewer live at
`https://view.meetings.codemyriad.io/<encoded-branch>/`. Slashes in branch
names are encoded as `--`, so `feat/foo-bar` becomes `feat--foo-bar`.

The pipeline runs in `.github/workflows/deploy-preview.yml`. There are two
publish paths:

- **UI-only preview** — rebuilds the viewer with main's processed meetings
  as the source. Cheap, runs on a GitHub-hosted runner, parallel across
  branches. Triggered automatically when viewer/workflow files change.
- **Processing preview** — builds a GPU `cassini-bin`, downloads the latest
  N raw recordings, runs STT on the self-hosted GPU runner, then
  publishes per-branch meeting artifacts alongside the viewer. **Opt-in
  only.** Serialized on the single GPU.

## Opting into a processing preview

Push or merge a commit whose HEAD message contains a `[preview]` marker:

- `[preview]` — process the **latest 3** recordings (default).
- `[preview:N]` where N is a positive integer — process the latest N. Use
  small N during iteration; the run takes roughly 1–6 minutes per recording
  depending on duration and model.
- `[preview:0]` — same as no marker (UI-only).

Only the HEAD commit's message is scanned. Add the marker as an empty
`--allow-empty` commit if you do not want to amend an earlier commit:

```sh
git commit --allow-empty -m "[preview:3] trigger branch preview"
git push
```

The marker is also recognised on `push` events to `main`, so you can refresh
main's deployed processed artifacts by landing a commit with `[preview:N]`
(or by dispatching the workflow manually — see below).

## Manual trigger

Use `workflow_dispatch` to run the pipeline against a branch without pushing
a marker commit:

```sh
gh workflow run deploy-preview.yml --ref <branch> -F run_processing=3
```

`run_processing` accepts:

- `false` or empty — UI-only preview, no GPU run.
- `true` — process the latest 3 recordings.
- A positive integer — process the latest N.
- Anything else — the detect job fails fast with a clear error.

## Fork PRs

Pull requests opened from forks **never** trigger this workflow. The
self-hosted GPU runner must not execute commits from untrusted sources. The
`detect` job rejects fork PRs unconditionally. Run previews on a branch in
the canonical repository.

## Runner prerequisites

The processing path runs on a self-hosted runner labelled `george`. That LXC
must provide:

- NVIDIA driver + CUDA 12.x + cuDNN 9.x runtime libraries reachable via
  `ldconfig` (`libcublasLt.so.12`, `libcudnn.so.9`,
  `libnvidia-ptxjitcompiler.so.*`). The `cassini-go-recorder/scripts/
  build-cassini-bin-gpu.sh` script bundles only sherpa-onnx and onnxruntime
  GPU shared objects; system CUDA libs are the runner's responsibility.
- `nvidia-smi`, `ffmpeg`, `ffprobe`, `node`, `npm`, `git`, `rclone` on
  `PATH`.
- An rclone-resolvable remote at `codemyriad:` reading the env vars set by
  the workflow (`R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_S3_ENDPOINT`
  GitHub Actions secrets).

These setup steps live in the operator runbook, not in this repo.

## Per-run cleanup

Each processing run wipes the branch's `RAW_DIR` and `PROC_DIR` under
`/mnt/data/cassini/previews/<encoded-branch>/` before downloading and
processing. This keeps the published catalog matched exactly to the latest
N selection — a previous run of `[preview:5]` followed by `[preview:3]`
publishes 3 meetings, not 5.

## Concurrency

- `george-gpu` — workflow-wide. Only one GPU run executes at a time across
  all branches (single GPU, ~8 GB VRAM budget).
- `branch-ui-${{ github.ref }}` — per branch. UI-only previews from
  different branches run in parallel; rapid pushes to the same branch queue
  in order.

## Validation matrix

When changing `deploy-preview.yml`, walk through these cases on the PR
branch before merging. Each row is one Actions run.

| # | Trigger | Setup | Expected outcome |
|---|---------|-------|------------------|
| 1 | `workflow_dispatch` | `gh workflow run deploy-preview.yml --ref <pr-branch> -F run_processing=1` | 1 latest recording processed; viewer published; both transcripts visible (if branch enables multi-tx) |
| 2 | `workflow_dispatch` | `run_processing=2` | 2 latest recordings processed |
| 3 | `workflow_dispatch` (legacy bool) | `run_processing=true` | maps to default 3 |
| 4 | `workflow_dispatch` (skip) | `run_processing=false` | no GPU run; UI-only preview |
| 5 | `workflow_dispatch` (bogus) | `run_processing=abc` | detect job fails with clear error |
| 6 | PR commit | head commit message `[preview]` | 3 meetings processed |
| 7 | PR commit | head commit message `[preview:1]` | 1 meeting processed |
| 8 | PR commit | no marker | UI-only preview |
| 9 | Fork PR | (requires an actual fork) | detect job rejected before any deploy |
| 10 | Large N | `run_processing=99` with <99 available | processes all available, no error |
| 11 | Browser load | after #1 | `view.meetings.codemyriad.io/<pr-branch>/` loads; transcript switcher works if multi-tx |

## What this does not do

- **Branch cleanup on delete.** Branch outputs accumulate in R2; clean up
  manually via `rclone delete codemyriad:cassini-processed/<encoded-branch>/`
  or wait on a follow-up `cleanup-preview.yml`.
- **Skip-if-same-code cache.** The workflow writes `.processing-hash` but
  does not read it as a skip gate. Identical processing code re-pushed
  reprocesses fully.
- **Sherpa-onnx tarball cache between runs.** The GPU build downloads
  ~200 MB on each clean-checkout deploy. A future `actions/cache@v4` step
  can address this.
- **Mid-PR commit markers.** Only the HEAD commit's message is scanned;
  markers on earlier commits in the PR are ignored.
