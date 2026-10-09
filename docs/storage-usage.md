# Storage usage

Open **Cassini → Operator → Storage** as an administrator. The page shows local
retained bytes, published Nextcloud bytes, category comparisons and date charts.
The **Retention** editor sits below the report on the same
page. See [retention behavior](container-retention.md) before changing policies.

The comparison covers all retained dates, including published Nextcloud files. Select a category to open its chart.
Expanding **Previous attempts** shows its four kinds; it starts expanded when the
saved policy sets each kind separately, and changes only the report.

```text
Storage totals
    |
    +-- Storage category comparison
    |       +-- Select category --> Retained bytes by lifecycle date
    |                                 +-- Time range
    |                                 +-- Focus or tap bar / data table
    |
    +-- Retention --> Explicit Save --> Next scheduled cleanup
```

Each chart has independent controls:

- **Time range:** all time, last 7/30/90 days, last year, or inclusive
  custom start and end dates. All time adapts to the available data.
- **Days per bar:** chosen from the range: daily, 7/30/60/90 days, or wider for
  very long ranges, so a chart never needs more than 60 bars. Buckets start at
  the selected range's start date; the final bucket ends at the selected end date.
- **Exact values:** hover, focus or tap a bar, or open **View chart data** for
  dates, raw bytes and file counts. Wide charts scroll inside the card. Ranges
  requiring more than 180 bars ask for a shorter range.

Chart controls do not save settings, delete files or trigger filesystem scans.
Changing the time range does not change the all-dates comparison total.

## What the dates and totals mean

This is a distribution of **files still present at calculation time**. Local
categories use UTC dates from the same lifecycle records as retention. It is not a record of
how full the disk was in the past, a growth forecast, or an estimate of how many
bytes a retention change will free.

| Category | Date used |
| --- | --- |
| Recordings | Recording completion, including a remaining capture duplicate |
| Published meetings (on this server) | Publication of that output version, including its retained seal/meeting copy |
| Failed recordings | Failed/interrupted attempt termination |
| Failed processing | Failed/interrupted attempt termination |
| Replaced output | The replacing successful publication |
| Failed publishing | Failed/interrupted attempt termination |
| Processing logs | Attempt termination |
| Other local files | No date inferred; work in progress and unmatched files remain visible |
| In Nextcloud Files | Successful publication in UTC; files without a job use `recordedAtLocal`, then `createdAtUtc` |

Unknown lifecycle dates remain in the category total and are reported separately
from the date chart. File modification times are never substituted. Policy
ownership alone does not imply a file can be deleted now: active jobs and other
retention safety checks still apply.

Local totals sum logical sizes of regular files under the operator's `current/`
and `runs/` folders. Each retained path is counted, including hardlinked or copied
aliases; this is not physical allocated disk usage. Symlinks are not followed.
Models, caches, database files and operator service logs are outside this total.

Published usage covers the current Nextcloud recordings root and any retained
legacy roots. These external bytes are shown separately because container-local
retention does not remove them. Measurement failures show a partial/unavailable
result; the page never treats a failed remote measurement as zero.

The Nextcloud chart uses the latest successful publication for the matching
destination and file format. An older copy in another root or format is not
assigned a newer publication's date. Files with no corresponding job use a valid
`recordedAtLocal` calendar date without timezone conversion, falling back to
`createdAtUtc` converted to UTC. Either field alone is sufficient. Files with
neither usable date, and shared metadata such as legacy catalogs, remain undated.

The scan uses the metadata index or matching lifecycle metadata first. For older
unindexed meetings it conditionally downloads the file and runs
`cassini inspect --meeting-times`; successful inspections are cached in memory by
path, Nextcloud file ID and ETag until the file changes or the operator restarts.
These reads share the calculation's timeout. Failed date reads preserve measured
bytes as undated and display an incomplete-breakdown warning; Recalculate retries
them. Failed root scans are excluded from chart totals and visibly mark the
measurement incomplete. Successful root totals always equal dated plus undated
bytes. Chart controls use the snapshot and never initiate downloads.

```text
Nextcloud file paths + sizes
    |
    +-- Matching job/destination/format --> Successful publication date (UTC)
    |
    +-- No job --> Indexed timestamps --> Inspect file if needed
    |                                      |
    |                         recordedAtLocal -> createdAtUtc -> undated
    |
    +-- Unmatched copy / shared metadata --> undated
                                               |
                             Published total + daily buckets --> Chart
```

## Refreshing and API

Opening Storage first reads the cached report, then recalculates it. **Recalculate**
refreshes it manually. The operator also rebuilds the index at fixed five-minute
boundaries. The page displays the calculation timestamp; changing chart controls
uses that snapshot. Reopening or recalculating fetches newer values. A failed
refresh leaves the last report visible with an error.

The existing ADMIN `/operator/storage/usage/details` endpoint supports GET for
the cache and POST to rebuild (adjust the operator prefix for your deployment).
The additive `categories` field contains totals, `undated_bytes`,
`undated_files`, and daily `{date, bytes, files}` entries. `category_error`
reports lifecycle lookup failures. Existing directory/format fields remain for
API compatibility; the UI uses categories.

The separate additive `published_category` has the same category shape and sums
successful current/legacy Nextcloud roots. It never contributes to local category
totals. `published_category_error` reports date lookup/inspection failures; remote
measurement failures remain on the corresponding `published` source. An older
operator that omits the field shows a date-data-unavailable message.

```text
Job / attempt lifecycle records --> Exact policy-owned paths
                                               |
current/ + runs/ regular files -----------------+--> Category/day totals
                                                       |
Nextcloud current + legacy roots ----------------> Cached report --> UI
```

Scans observe a live filesystem, so files can change during a calculation. The
report is informational and does not reserve files or drive deletion.

## Local verification

From the repository root, install the workspace dependencies using Node.js 22
and npm, then install Chromium for the browser checks:

```sh
npm ci
npx playwright install chromium
npm test --workspace=cassini-app
npm run build:all --workspace=cassini-app
cd cassini-operator
go test ./...
```

Vitest browser checks mount the real Svelte UI with synthetic operator responses;
they do not contact a running operator or real recordings. Storage screenshots
are written to gitignored `cassini-app/node_modules/.cache/vitest-screenshots/`.
The setup checks cover the real shell, shared retention editor, revision-based
completion, account setup, failed/stale saves, dismissal, admin gating, focus and
responsive layouts. Their screenshots are in
`cassini-app/node_modules/.cache/vitest-screenshots/`.
