# V0 (D-250) — Hosted demo data bucket

> Continuation of slice V0 ("Prep: summary template and dev demo data pull"). The original V0 stood up the `DEMO_DATA_URL` pull pattern with a gitignored env var; D-250 swaps the source from a hand-rolled, summary-patched location to a real curated R2 bucket so summaries are materialized in the data, not synthesized at pull time. The bucket name and public URL are operational config — they live in the team's secrets store and individual `.envrc` files, not in this repo.

## Linkage

- **Ticket:** [D-250](https://linear.app/code-myriad/issue/D-250/v1-contd-add-dedicated-demo-data-hosting) (In Progress, Chris)
- **Slice doc:** `planning/initiatives/mvp/slices.md` (row V0); also tickets.md (Ticket V0)
- **Per-slice shaping doc:** ticket is sufficiently shaped — scope is explicit in the D-250 description (checklist of four items)
- **Contract owner(s):**
  - `cassini build` (transcript + summary): `cassini-go-recorder/internal/cassini/build.go`
  - `export-static-meetings`: `cassini-viewer/scripts/export-static-meetings.mjs` (writes `catalog.json` v1)
  - Catalog schema: `cassini-viewer/src/viewer/catalog.ts:51` (`cassini.viewer.catalog.v1`)
- **Consumer(s):**
  - `cassini-viewer` runtime: `cassini-viewer/src/viewer/catalog.ts` (`fetchCatalog`), `cassini-viewer/src/App.svelte`
  - Pull script: `cassini-viewer/scripts/demo-data-pull.mjs`

## Pre-flight assumptions

> Verify each before coding. If one is wrong, the plan is invalid; revise before continuing.

- [x] `cassini build <mkv> --out <dir>.meeting` produces a directory bundle with `summary.md` when `OPENROUTER_API_KEY` (or equivalent `SUMMARY_MODEL`-targeted creds) is set — verified at `cassini-go-recorder/internal/cassini/build.go:39-55` and `cassini-go-recorder/internal/transcribe/transcribe.go` summary path.
- [x] `cassini-publisher/bin/export-static-meetings.sh` (wrapping `cassini-viewer/scripts/export-static-meetings.mjs`) produces a full static site with `catalog.json`, `index.html`, `assets/`, and `meetings/<id>/…` — verified at the script's `main()` flow.
- [x] The published `catalog.json` *already* carries `speakerCount`, `segmentCount`, `digestDurationMs` per entry — verified at [export-static-meetings.mjs:161-163](../../../../cassini-viewer/scripts/export-static-meetings.mjs#L161-L163). **Consequence: no code change to the publish step is needed for D-250.**
- [x] The "programmatic summary addition" called out in the D-250 description is the `seedAlternatingMeetingSummaries` function in [demo-data-pull.mjs:167](../../../../cassini-viewer/scripts/demo-data-pull.mjs#L167), already commented `TODO: replace this with a hosted demo data`. **Scope of the viewer-side change is narrower than the ticket text suggests** — only the pull script needs editing.
- [x] `DEMO_DATA_URL` is set per-developer in a gitignored `.envrc`; only `.envrc.example` and READMEs reference it textually. No code default to bake in.
- [x] The demo R2 bucket exists, is empty, and the curator has a scoped read-write rclone remote pointing at it (and a read-only one pointing at the upstream raw bucket).

## Open technical decisions

### D1. Where the curation manifest and its generator live

- **Recommendation:** the manifest itself lives **in the bucket** as `demo-manifest.json` alongside `catalog.json` — the bucket should be self-describing for anyone landing there cold. The generator script stays **out of this repo**; it's a one-off ops tool that touches private buckets, and the repo is public. Curators carry it in their working directory (or a separate internal ops repo); the build plan describes the workflow in plain English.
- **Alternative:** commit the generator alongside `cassini-viewer/scripts/export-static-meetings.mjs` for discoverability. Worth doing if curation becomes a routine team activity rather than an occasional one. Would require stripping bucket-name defaults from the script first.
- **Blast radius:** low (no consumer reads the manifest yet; the generator is a small re-creatable script).
- **Decided:** ☑ accepted — manifest in-bucket, generator kept local-only. Public-repo posture: no private bucket names leak via the repo or via committed defaults.

### D2. Build output form: `.meeting` directory vs. portable `.opus`

- **Recommendation:** directory bundles (`--out .../foo.meeting/`). Reason: `export-static-meetings` consumes directories cleanly, `summary.md` lands as a separate fetchable file, and the viewer's "summary lives in a file the browser GETs" path is the simple one. Portable opus is great for distribution but the bucket is the distribution layer here — we don't need a second container.
- **Alternative:** portable `.opus` (matches `ops/process-recordings.sh` default). Worth picking if the demo bucket should double as a download surface for "grab one meeting" use cases — not currently a goal.
- **Blast radius:** low — can re-run on the same raw inputs if we change our minds.
- **Decided:** ☑ accepted — directory bundles

### D3. How many meetings in the initial slice

- **Recommendation:** 6 — enough to exercise list pagination, mixed durations, multi-speaker rendering, and the "no-summary" UX (one meeting deliberately built without summary, OR one curated to omit it).
- **Alternative:** the original V0 used 2. Worth staying at 2 only if storage cost in the demo bucket matters (it doesn't — bucket is empty, plenty of raw exists upstream).
- **Blast radius:** low.
- **Decided:** ☑ accepted — 6 meetings. Specific mkvs picked during step 2 below.

### D4. Whether to delete `seedAlternatingMeetingSummaries` outright or behind a flag

- **Recommendation:** delete outright. It's explicitly marked TODO in the code; once the bucket has real summaries on every meeting (or deliberate omissions), keeping the function around just preserves a footgun. The "summary missing" path is the right way to test that fallback UI.
- **Alternative:** keep the function but make it no-op by default. No benefit — dead code is worse than removed code.
- **Blast radius:** medium (touches a script everyone uses).
- **Decided:** ☑ accepted — delete + remove the seed fixture file if nothing else references it

## Commit sequence

Each commit independently builds and tests green.

1. **Commit: add this build plan.** Adds `planning/initiatives/mvp/slices/V0-demo-data-prep/build-plan.md`. No behavior change to existing scripts. Per D1 the curation-manifest generator stays out of the repo.
2. **(Manual, no commit) Build the slice locally.** Pull selected mkvs from the raw recordings bucket, run `cassini build` per meeting with summaries enabled, publish via `export-static-meetings.sh`, generate `demo-manifest.json` with the local ops script, dry-run + `rclone sync` to the demo bucket, enable public hosting in the Cloudflare dash. Verify viewer loads against the public URL.
3. **Commit: clean up `demo-data-pull.mjs`.** Remove `seedAlternatingMeetingSummaries` and `loadSeededSummary` (and the lantern-fixture constant). Loosen `requiredManifestFiles` to `{audio, transcript}` — the snake_case `display_transcript` / `readable_transcript` keys it demanded are never written by `cassini build`, and the corresponding files are addressed by filename in the optional download list. Drop the now-stale "dummy data sourced from the lantern festival fixture" note from `cassini-viewer/README.md`.
4. **Commit (if needed): update README guidance.** The placeholder in `.envrc.example` is already generic (`https://example.com/cassini-demo-site`). If anything is needed beyond that, add a brief "where this bucket comes from" pointer in `cassini-viewer/README.md` linking back to this slice folder — but the actual URL stays in personal `.envrc` files, not the repo.
5. **(Smoke, no commit) End-to-end verification.** Fresh clone, set `DEMO_DATA_URL` to the public bucket URL, `pnpm demo-data:pull`, `pnpm dev`, confirm:
   - All meetings load
   - Summaries render on every meeting that should have one (and the fallback renders on the one that shouldn't)
   - No `seedAlternatingMeetingSummaries` reference remains anywhere

## Verification map

| AC bullet from ticket | Made true by | Verified by |
|---|---|---|
| Create R2 bucket | (pre-existing) | Bucket listing in Cloudflare dash |
| Enable static hosting (make public) | Step 2 (manual, dash) | `curl -sI <public-url>/catalog.json` → 200 |
| Fill the data into the bucket (subset of prod data) | Step 2 (manual, build+publish+sync) | `rclone lsd <demo-remote>:<demo-bucket>/`; `demo-manifest.json` present |
| Replace `DEMO_DATA_URL` (everyone does their own) | Commit 4 (`.envrc.example` + README guidance) | grep for the old placeholder; README diff |
| Remove programmatic summary addition | Commit 3 (drop `seedAlternatingMeetingSummaries`) | `git grep seedAlternatingMeetingSummaries` returns nothing; smoke step 5 |

## Out of scope (file separately if needed)

- The runtime hydration loop `hydrateCatalogMeetingMetadata` / `loadPortableMeetingSummary` in `App.svelte` is *not* the "programmatic summary" the ticket targets — it enriches portable `.opus` entries with speaker/segment counts at viewer load. It's only relevant for catalog entries that lack pre-populated counts. Since `export-static-meetings.mjs` already populates these fields for directory bundles, the loop becomes a no-op for D-250 outputs. Leaving the code in place; if we go all-in on directory bundles project-wide and drop portable .opus catalog support, that's a separate cleanup.
- Migrating the existing processed-assets bucket to live under the same hosting pattern as the demo bucket — different question.
- CI automation to refresh the demo bucket on a schedule — explicit non-goal per the ticket's "manually curated" framing.

## Process retro

> Fill in during the build, not at the end.

### What was unclear coming out of shaping/ticket?

- The phrase "programmatic summary addition" in the ticket body was ambiguous on first read — could have meant the pull-time `seedAlternatingMeetingSummaries` injection or the runtime `loadPortableMeetingSummary` hydration. The repo made it unambiguous (the pull-script function carries a literal `TODO: replace this with a hosted demo data` comment that maps 1:1 to the ticket), but the ticket text alone didn't disambiguate.

### What did this plan miss?

- _(filled during/after work)_

### What about the codebase wasn't in the ticket's "likely code areas"?

- The ticket didn't mention `cassini-publisher` / `export-static-meetings.mjs` at all, but it's where the catalog enrichment happens — and it's the reason the viewer-side change is much smaller than the ticket implies. Worth pointing at this explicitly in future tickets that touch the demo data flow.

### Suggestion for shaping skill / ticket template

- For tickets that span a captured pipeline (raw → build → publish → host → consume), include a single bullet pointing at each stage's entry-point file, even when stages are unchanged. Saves the build-plan step from having to re-derive the topology.

### Suggestion for build-plan template

- _(filled during/after work)_
