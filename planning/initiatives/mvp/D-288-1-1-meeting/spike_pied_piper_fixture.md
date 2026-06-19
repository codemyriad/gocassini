---
shaping: true
---

# Fixture Spike: Pied Piper synthetic meeting

## Context

The first continuation requirement is to replace the Lantern Festival showcase media used by initial D-288 with the Pied Piper synthetic meeting. The request asks us to use the existing fixture if it exists, and otherwise synthesize it from source using the existing cached generation path.

## Goal

Identify the existing Pied Piper fixture inputs, cached outputs, first two speakers, and the minimum implementation changes needed to make `cassini dev play` use this fixture instead of Lantern Festival.

## Questions

| # | Question |
|---|----------|
| **F1-Q1** | Does a Pied Piper synthetic meeting scenario and processed fixture already exist? |
| **F1-Q2** | How does the existing synthetic meeting generation path cache or reuse prepared media? |
| **F1-Q3** | Which participants should become the two synthetic Nextcloud users for the 1:1 continuation? |
| **F1-Q4** | What changes are needed for D-288 `cassini dev play` to use Pied Piper for `single` and `full` playback? |

## Acceptance

This spike is complete when we can describe the existing Pied Piper fixture location, whether it is already cached, which two speakers seed the private-meeting users, and what implementation changes are needed for the playback fixture swap.

## Findings

### F1-Q1 — Fixture exists

The source scenario exists:

- `harness/scenarios/synthetic-pied-piper.v1.json`

The processed fixture also exists locally:

- `harness/media/processed/synthetic-pied-piper-v1/manifest.json`
- `harness/media/processed/synthetic-pied-piper-v1/erlich.ivf`
- `harness/media/processed/synthetic-pied-piper-v1/erlich.ogg`
- `harness/media/processed/synthetic-pied-piper-v1/monica.ivf`
- `harness/media/processed/synthetic-pied-piper-v1/monica.ogg`
- equivalent `.ivf` / `.ogg` media for `richard`, `jack`, `gavin`, and `laurie`

The current generated manifest reports:

- `scenario_id`: `synthetic-pied-piper-v1`
- `title`: `Synthetic Pied Piper Review`
- `duration_seconds`: `172.0`
- six participants

### F1-Q2 — Existing cache behavior

The default synthetic meeting scripts already point at Pied Piper:

- `harness/bin/prepare-synthetic-meeting.sh`
  - default `SCENARIO=$TEST_DIR/scenarios/synthetic-pied-piper.v1.json`
  - default `OUTPUT_DIR=$MEDIA_DIR/processed/synthetic-pied-piper-v1`
- `harness/bin/stream-synthetic-meeting.sh`
  - same default scenario/output directory
  - default `PREPARE=1`
  - can skip preparation with `--skip-prepare`

The Python generator, `harness/bin/prepare-synthetic-meeting.py`, uses cached outputs when all of this is true:

1. `--force` is not passed.
2. `manifest.json` exists.
3. `cached_manifest_is_complete(manifest_path)` can resolve the required participant media files.

When the cache is complete, the generator prints the manifest path and exits without re-synthesizing audio.

Important nuance: the current checked/local `synthetic-pied-piper-v1/manifest.json` contains absolute paths from this developer machine, while the generator has since been updated to write portable relative paths. `stream-synthetic-meeting.sh` handles both absolute and relative `media_prefix` values. If regenerated, the manifest should become portable.

### F1-Q3 — First two speakers

The first two participants in `harness/scenarios/synthetic-pied-piper.v1.json` and in the processed manifest are:

| Order | ID | Display name | Role | Voice |
|---:|---|---|---|---|
| 1 | `erlich` | `Erlich Bachman` | `Incubator Visionary` | `am_onyx` |
| 2 | `monica` | `Monica Hall` | `Operations and Reality` | `af_sarah` |

These should seed the two synthetic Nextcloud users for the private 1:1 scenario.

### F1-Q4 — Playback changes needed

Initial D-288 hard-codes the Lantern Festival fixture in Go:

- `devPlayShowcaseScenarioRel = "harness/scenarios/showcase-lantern-festival.v1.json"`
- `devPlayShowcaseOutputDirRel = "harness/media/processed/showcase-lantern-festival-v1"`
- `devPlayShowcaseSingleID = "mira"`
- `devPlayDefaultSingleName = "Mira Chen"`
- launch output prints `media=showcase-lantern-festival-v1`

The fixture swap should change the default media to:

- scenario: `harness/scenarios/synthetic-pied-piper.v1.json`
- output dir: `harness/media/processed/synthetic-pied-piper-v1`
- single speaker ID: `erlich`
- single speaker display name: `Erlich Bachman`
- launch output media label: `synthetic-pied-piper-v1`

For full mode, `stream-synthetic-meeting.sh` already defaults to Pied Piper. The Go command can either keep passing explicit scenario/output paths or rely on script defaults; explicit paths are clearer and easier to test.

For single mode, `stream-video.sh` does not know how to synthesize a synthetic meeting from a scenario. It only verifies media files for the selected `--media-prefix`. Therefore, the implementation needs a fixture-ensure step before single-mode playback if the fixture may be missing.

Recommended mechanism:

1. Add a small fixture descriptor for Pied Piper in the Go `dev play` implementation.
2. Before launching either mode, ensure `manifest.json` and the selected participants' `.ivf` / `.ogg` media exist.
3. If missing, run `harness/bin/prepare-synthetic-meeting.sh --scenario <pied-piper-scenario> --output-dir <pied-piper-output-dir>` and rely on its cache check to avoid unnecessary generation.
4. Keep `--skip-prepare` on the final stream invocation once the fixture has been ensured.

## Spike conclusion

Pied Piper already exists and is already the default for the lower-level synthetic meeting scripts. The initial D-288 Go command is the part still pinned to Lantern Festival. The fixture replacement is a small, well-understood change, with one important implementation detail: single-speaker mode should ensure the Pied Piper fixture exists before calling `stream-video.sh` directly.
