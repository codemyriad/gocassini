# CI performance investigation

The focused optimization pass preserves shape B's supported-major matrix,
real installed-product scenarios, protected check names and immutable release
qualification. It changes preparation, observability and housekeeping boundaries.

## Baseline

Measured from [image/product run 35523613323](https://github.com/codemyriad/gocassini/actions/runs/35523613323)
at `0398a92415ca283b360fcf09da295990fafe3fd2`, with `full-ci` enabled.

| Measurement | Baseline |
|---|---:|
| Image/product workflow elapsed, including scheduling | 13m55s |
| Sum of successful job execution time | 43m13s |
| Installed compatibility nc33 / nc34 / nc35 jobs | 6m03s / 6m29s / 6m33s |
| nc34 installed scenario | 4m52s |
| nc34 infrastructure pull, including extraction | 27.8s |
| nc34 Cassini artifact download + Docker load | 29s |
| CUDA-base job, already present in registry | 1m58s |
| Unnecessary CUDA-base disk cleanup within that job | 1m43s |
| GPU smoke + separate same-fixture quality build | 68s + 70s |
| Whole GPU smoke/quality/short-clip job | 3m54s |

The infrastructure pull's layer downloads completed within about 9.4 seconds;
extraction overlaps downloading and accounts for the later tail. These are log
boundaries, not a network-only profiler. The Cassini artifact was 602,930,990
compressed bytes. CPU consumers independently load it into fresh Docker daemons.

Every compatibility row logged a missing root `go.sum` and 38 module downloads.
npm and image build caches hit; GPU image pulls were already warm (about one
second). Go dependency/build caching is the clear omitted cache. A whole-Docker
cache would still pay transfer and extraction; it needs a separate benchmark.

## Implemented boundaries

- Cache host recorder CLI and Talk rotator using both module lockfiles.
- Resolve the CUDA base before conditionally reclaiming build space.
- Run one GPU smoke transcription and apply model/no-download/GPU/no-fallback
  and text-quality assertions to that same invocation's output. Keep standalone
  transcript verification available and keep short-clip regression unchanged.
- Record versioned scenario phase timings and log groups, including failure,
  observation and cleanup. Keep timings separate from qualification evidence.
- Move registry pruning and retained-tag verification into daily/manual
  housekeeping. Manual invocation defaults to dry run. Release qualification
  continues to require the complete successful image/product workflow.

The cheap CPU container tests still load their own images and keep their existing
check contexts. Consolidating those jobs is a separate follow-up. Compatibility
rows remain parallel and use fresh data volumes; no cached installed database
can conceal installation failures.

## Validation

Offline regressions exercise a deliberately wrong/empty transcript, timing on
an actual failing shell command with cleanup, registry retention across paginated
responses, protected index children, inspection failure, and both CUDA-base
presence decisions. The existing workflow conditions must clean before building
on a miss and skip both operations on a hit.

The implementation must also pass observed PR CI: all advertised baselines,
GPU-use and no-fallback assertions, unchanged quality floor, short clips, required
check contexts and existing release-evidence tests. Inspect cache restores and
phase artifacts rather than assuming a configured cache hit. Compare elapsed
workflow time and summed runner time separately against the baseline above;
cache warmth, VM speed and the single GPU runner's queue affect each sample.
Observed run links and measurements are retained in [PR #321](https://github.com/codemyriad/gocassini/pull/321).
This is a before/after comparison, not a percentile or cold-build benchmark.

The new scheduled housekeeping workflow cannot be dispatched until present on
the default branch. Its retention and dry-run behavior are covered offline;
registry inspection can additionally run locally in dry-run mode without deleting
versions. A missing-base path check does not claim a full cold CUDA build.
