# D-290 E2E testing - Slice 5 baseline

Before snapshot for Slice 5's CI efficiency work.

Generated on 2026-05-28 from the repository's latest 10 `pull_request`
workflow runs:

```bash
gh api -X GET 'repos/codemyriad/gocassini/actions/runs?event=pull_request&per_page=10'
gh api -X GET "repos/codemyriad/gocassini/actions/runs/${run_id}/jobs?per_page=100"
gh api -H 'Accept: application/vnd.github+json' "repos/codemyriad/gocassini/actions/jobs/${job_id}/logs"
```

Timings below come from the Actions jobs API (`completed_at - started_at`)
and step timings come from each job's `steps[]` payload. Log inspection is
only used to explain where time went, especially hidden image pulls.

Note: the latest available PR-event workflow run in GitHub was from
2026-05-21. This sample does not include a PR run for the local D-290
`e2e-install-prod-path` job.

## Sample window

| Run | Workflow | Branch / sha | Result | Wall time |
|---|---|---|---:|---:|
| [26239614865](https://github.com/codemyriad/gocassini/actions/runs/26239614865) | CI | `fix/ci-e2e-mute-timing-floor` / `9e2847fde349` | failure | 3:43 |
| [26239614867](https://github.com/codemyriad/gocassini/actions/runs/26239614867) | Deploy Preview | `fix/ci-e2e-mute-timing-floor` / `9e2847fde349` | success | 0:11 |
| [26232601149](https://github.com/codemyriad/gocassini/actions/runs/26232601149) | Deploy Preview | `cassini-appstore-dogfood` / `ee88fd25c47f` | success | 0:13 |
| [26232601150](https://github.com/codemyriad/gocassini/actions/runs/26232601150) | CI | `cassini-appstore-dogfood` / `ee88fd25c47f` | failure | 3:58 |
| [26232601151](https://github.com/codemyriad/gocassini/actions/runs/26232601151) | Build + publish ExApp image | `cassini-appstore-dogfood` / `ee88fd25c47f` | failure | 10:14 |
| [26231984424](https://github.com/codemyriad/gocassini/actions/runs/26231984424) | Build + publish ExApp image | `cassini-appstore-dogfood` / `9dc7d70f8f62` | cancelled | 11:08 |
| [26231984353](https://github.com/codemyriad/gocassini/actions/runs/26231984353) | Deploy Preview | `cassini-appstore-dogfood` / `9dc7d70f8f62` | success | 0:14 |
| [26231984359](https://github.com/codemyriad/gocassini/actions/runs/26231984359) | CI | `cassini-appstore-dogfood` / `9dc7d70f8f62` | failure | 4:07 |
| [26231859123](https://github.com/codemyriad/gocassini/actions/runs/26231859123) | Deploy Preview | `cassini-appstore-dogfood` / `2a97ec21eefe` | success | 0:15 |
| [26231859269](https://github.com/codemyriad/gocassini/actions/runs/26231859269) | Build + publish ExApp image | `cassini-appstore-dogfood` / `2a97ec21eefe` | cancelled | 3:15 |

Deploy Preview contributes only the `detect` job in these PR runs; the real
runtime baseline is CI plus Build + publish.

## Per-job timings

Skipped zero-duration jobs are omitted.

| Run | Workflow | Job | Result | Time |
|---|---|---|---:|---:|
| 26239614865 | CI | Unit tests (`harness/go-talk-rotator`) | success | 0:10 |
| 26239614865 | CI | Unit tests (`cassini-go-recorder`) | success | 0:35 |
| 26239614865 | CI | Unit tests (`cassini-operator`) | success | 0:21 |
| 26239614865 | CI | Integration (`ci-e2e.sh`) | success | 2:46 |
| 26239614865 | CI | Integration (`ci-e2e-mute.sh`) | failure | 2:58 |
| 26239614865 | CI | Integration (`ci-e2e-rejoin.sh`) | success | 2:48 |
| 26239614867 | Deploy Preview | detect | success | 0:06 |
| 26232601149 | Deploy Preview | detect | success | 0:07 |
| 26232601150 | CI | Unit tests (`harness/go-talk-rotator`) | success | 0:25 |
| 26232601150 | CI | Unit tests (`cassini-operator`) | success | 0:29 |
| 26232601150 | CI | Unit tests (`cassini-go-recorder`) | success | 0:36 |
| 26232601150 | CI | Integration (`ci-e2e-mute.sh`) | failure | 3:00 |
| 26232601150 | CI | Integration (`ci-e2e-rejoin.sh`) | success | 3:11 |
| 26232601150 | CI | Integration (`ci-e2e.sh`) | success | 2:46 |
| 26232601151 | Build + publish | Validate `appinfo/info.xml` | success | 0:21 |
| 26232601151 | Build + publish | Build ExApp image | success | 3:29 |
| 26232601151 | Build + publish | Build CUDA ExApp image | success | 3:12 |
| 26232601151 | Build + publish | Smoke transcription on GPU | success | 2:31 |
| 26232601151 | Build + publish | E2E Talk roundtrip (CUDA / GPU) | failure | 2:36 |
| 26232601151 | Build + publish | Smoke transcription (CPU) | success | 1:03 |
| 26232601151 | Build + publish | E2E Talk roundtrip (CPU) | failure | 4:48 |
| 26232601151 | Build + publish | E2E container HTTP plane | success | 0:51 |
| 26232601151 | Build + publish | E2E install against Nextcloud + AppAPI | success | 2:13 |
| 26232601151 | Build + publish | Smoke test image | success | 0:53 |
| 26231984424 | Build + publish | Validate `appinfo/info.xml` | success | 0:20 |
| 26231984424 | Build + publish | Build CUDA ExApp image | success | 4:19 |
| 26231984424 | Build + publish | Build ExApp image | success | 2:48 |
| 26231984424 | Build + publish | E2E Talk roundtrip (CPU) | success | 4:55 |
| 26231984424 | Build + publish | Smoke transcription (CPU) | success | 1:00 |
| 26231984424 | Build + publish | E2E container HTTP plane | success | 0:48 |
| 26231984424 | Build + publish | E2E install against Nextcloud + AppAPI | success | 2:03 |
| 26231984424 | Build + publish | Smoke test image | success | 1:38 |
| 26231984424 | Build + publish | E2E Talk roundtrip (CUDA / GPU) | failure | 2:36 |
| 26231984424 | Build + publish | Smoke transcription on GPU | cancelled | 2:31 |
| 26231984353 | Deploy Preview | detect | success | 0:07 |
| 26231984359 | CI | Unit tests (`cassini-operator`) | success | 0:26 |
| 26231984359 | CI | Unit tests (`harness/go-talk-rotator`) | success | 0:13 |
| 26231984359 | CI | Unit tests (`cassini-go-recorder`) | success | 0:48 |
| 26231984359 | CI | Integration (`ci-e2e.sh`) | success | 3:07 |
| 26231984359 | CI | Integration (`ci-e2e-mute.sh`) | failure | 2:41 |
| 26231984359 | CI | Integration (`ci-e2e-rejoin.sh`) | success | 3:03 |
| 26231859123 | Deploy Preview | detect | success | 0:08 |
| 26231859269 | Build + publish | Validate `appinfo/info.xml` | success | 0:16 |
| 26231859269 | Build + publish | Build ExApp image | cancelled | 2:23 |
| 26231859269 | Build + publish | Build CUDA ExApp image | cancelled | 2:48 |

## Longest steps

Top distinct slow steps from the jobs API:

| Rank | Step | Jobs / examples | Observed time | Notes |
|---:|---|---|---:|---|
| 1 | `Run Talk-driven e2e (CPU)` | E2E Talk roundtrip (CPU), runs 26231984424 and 26232601151 | 3:19-3:22 | Includes the scripted 75s recording window, Talk room setup, record/upload/build/publish, and transcript check. |
| 2 | `Build and push CUDA image` | Build CUDA ExApp image | 1:50-2:37 in completed samples | Dockerfile stages mostly cache-hit, but final CUDA image materialization still dominates. |
| 3 | `Run recorder integration test against local Nextcloud` | CI integration matrix | 2:06-2:30 | Includes stack bring-up, hidden compose pulls, bootstrap, recorder+publisher run, and teardown. |
| 4 | `Run Talk-driven e2e (GPU)` | E2E Talk roundtrip (CUDA / GPU) | 2:18-2:19 | Same 75s recording floor as CPU; faster STT path, but still gated by fixed scenario duration and Talk orchestration. |
| 5 | Image build/load handoff steps | `Build image (local load)`, `Load ExApp image`, `Save image tarball`, `Upload image artifact` | up to 1:38 for one `Load ExApp image`; 0:40-0:53 artifact upload | PR CPU image is saved, uploaded, downloaded, and `docker load`ed once per downstream CPU job. This is repeated overhead, not test logic. |

Secondary but visible costs:

- `Free disk on ubuntu-latest` in CUDA image builds: 0:53-1:25.
- `Run install e2e against a fresh Nextcloud + AppAPI`: 1:18-1:25 as a step, with 0:29 of hidden image pull in the sampled successful run.
- `Run CUDA transcribe smoke` and `Run dual-variant transcript verify`: each about 1:08 in the sampled GPU smoke jobs.

## Hidden compose pulls

The main UI honesty problem is not that images are pulled; it is that some
pulls are inside a broad test step rather than their own step.

| Location | Evidence | Hidden time | Slice 5 note |
|---|---|---:|---|
| CI integration matrix (`harness/bin/up.sh` -> `compose up -d`) | Job 77222246300 logs show `[test] Starting Docker Compose stack` at 16:40:20Z, `signaling/db/nextcloud/... Pulling` at 16:40:21Z, and `nextcloud Pulled` at 16:40:57Z. | about 0:36 | Split `docker compose -f harness/compose.yml --profile full pull` out before `Run recorder integration test...` if we want the CI workflow to show pull vs. test time. |
| Existing install e2e (`harness/bin/ci-e2e-install-exapp.sh`) | Job 77197766968 logs show `[install-e2e] starting Nextcloud + db` at 14:37:57Z, `nextcloud Pulling`/`db Pulling` at 14:38:01Z, and `nextcloud Pulled` at 14:38:30Z. | about 0:29 | Split `docker compose -f harness/compose.yml pull nextcloud db` out of the install step. |
| Talk roundtrip jobs | Workflow already has `Pre-pull compose stack images`. On GitHub-hosted CPU it took 0:34-0:38; on `george` it took about 0:03 because the daemon cache was warm. | visible today | Keep this pattern for any new full-prod-path job. |
| `ci-e2e-talk-record-roundtrip.sh` phase 3 | The script still runs `docker pull "$IMAGE_REF" >>"$LOG_DIR/docker.log" 2>&1 || true` inside the test phase. | usually small/no-op after `load-exapp-image` | Remove or surface this if it ever becomes non-trivial; it is currently hidden by log redirection. |

## Cache and layer patterns

### Docker Buildx

- CPU image uses `cache-from: type=gha` and `cache-to: type=gha,mode=max`.
- CUDA image uses the separate `scope=cuda`.
- Sampled successful builds imported the GHA cache quickly (about 0.1-0.7s)
  and most expensive logical stages were `CACHED`: Go module download, Go
  builds, `npm ci`, viewer/control-panel builds, model fetch, frpc fetch, and
  apt install layers.
- The remaining cost is mostly final image materialization/export. In the
  CPU sample, the final-stage copy/materialization around
  `COPY --from=control-panel-builder ...` consumed about 0:30-0:41, then
  docker image export/cache export added about 0:33-0:38. In the CUDA sample,
  the analogous final-stage materialization took about 1:39 before a short
  push/cache export.
- Practical implication: the cache is working for source/build layers, but a
  PR still pays to materialize a large runnable image, especially CUDA. Slice 5
  should measure whether image size, final-stage ordering, or artifact/registry
  handoff is the better lever.

### CPU image handoff

- PR CPU builds use `load: true`, then `docker save` to `/tmp/exapp-image.tar`,
  then `actions/upload-artifact`.
- Downstream CPU jobs restore the artifact and run `docker load`.
- In the sample, `docker load` alone ranged from about 0:24 to 1:09. The
  `Load ExApp image` step reached 1:30 in one smoke job.
- Because this happens independently in smoke, transcribe, container e2e,
  install e2e, and Talk CPU, the repeated handoff cost is one of the clearest
  non-test overheads.

### CUDA and `george`

- CUDA PR builds push to GHCR and downstream GPU jobs pull from the registry.
- On `george`, the first SHA pull may download a few layers; later jobs for the
  same SHA report `Image is up to date`.
- Compose stack pulls on `george` were effectively warm in sampled jobs:
  14:40:26Z to 14:40:28Z for the full Talk stack in job 77197710076.
- The GPU transcription/test jobs use the bundled model under
  `/opt/cassini/cache` (`parakeet-tdt-0.6b-v3`); no runtime model download was
  visible in the sampled logs.

### Go/action caches

- `actions/setup-go@v5` cache hits were primary-key hits in sampled unit and
  integration jobs.
- Restore sizes ranged from about 50 MiB to 225 MiB, but this is not a top
  bottleneck compared with compose pulls, image handoff, and Talk e2e runtime.

## Baseline targets for after Slice 5

Compare the optimized run against these before numbers:

| Metric | Before |
|---|---:|
| Longest Build + publish workflow wall time | 10:14 completed-failing sample; 11:08 cancelled sample |
| Longest CI workflow wall time | 4:07 |
| Longest job | E2E Talk roundtrip (CPU), 4:55 |
| Longest single step | `Run Talk-driven e2e (CPU)`, 3:22 |
| Hidden compose pull inside CI integration step | about 0:36 |
| Hidden compose pull inside install e2e step | about 0:29 |
| Explicit full-stack compose pre-pull on GitHub-hosted runner | 0:34-0:38 |
| Explicit full-stack compose pre-pull on `george` | about 0:03 |
| CPU image `Load ExApp image` step | 0:38-1:30 |
| CPU image artifact upload step | 0:40-0:53 |
| CUDA image build step after disk cleanup | 1:50-2:37 |

The biggest Slice 5 candidates are:

1. Keep explicit compose pulls and add them where still hidden.
2. Reduce repeated CPU image artifact download/load cost, or switch same-repo
   PR CPU downstream jobs to a registry handoff if package permissions allow it.
3. Revisit the fixed 75s Talk recording window once coverage is stable; it is
   the dominant irreducible cost in both CPU and GPU Talk jobs.
4. Trim or split final image materialization costs, especially the CUDA image,
   after confirming whether final-stage cache ordering or image size is the
   limiting factor.
