# Cassini Meeting Archive Overwrite Hypothesis

Date: 2026-06-19
Rechecked: 2026-06-22 against current `main`

Status: still a plausible production hypothesis, not verified on the production AppAPI host. Current `main` still publishes from `<work-root>/current/*.meeting`; open PR #79 would owner-scope the archive and deliver selected built artifacts to Files, but it does not remove the need to preserve or intentionally replace the publish input invariant.

## Problem

Newly recorded meetings appear to replace the visible meeting archive: only the latest meeting is visible, whereas earlier versions showed multiple meetings.

## Current hypothesis

There are two related but separate publish flows.

### 1. Production Nextcloud ExApp flow

The production operator does **not** merge from the existing live published site. It rebuilds a full site from the operator's current meeting bundle directory:

```text
<work-root>/current/*.meeting
```

Then it atomically promotes the generated attempt site into the live site root:

```text
<site-root>/
```

In AppAPI/ExApp production, the expected persistent paths are:

```text
$APP_PERSISTENT_STORAGE/operator/jobs/current/*.meeting
$APP_PERSISTENT_STORAGE/site/published/catalog.json
```

If `operator/jobs/current/` contains only the latest `*.meeting`, then `cassini publish` will generate a catalog with only that meeting and promotion will replace the live site with that one-meeting catalog. In that case publish is behaving as implemented, but its input set has collapsed.

Relevant local code:

- `cassini-operator/internal/operator/publish_runtime.go` runs:
  `cassini publish <work-root>/current --out <attempt>.site`
- `cassini-go-recorder/internal/cassini/publish.go` scans the input directory for ready `.meeting` bundles.
- `cassini-operator/internal/operator/artifact_promotion.go` promotes the newly generated site over the live site root.

### 2. George / R2 branch-preview flow

The George daily recorder writes raw recordings locally and uploads them to R2:

```text
/mnt/data/cassini/recordings/daily-meeting-YYYY-MM-DD/recording.mkv
codemyriad:cassini-raw/daily/YYYY-MM-DD.mkv
```

The daily trigger currently runs:

```bash
gh workflow run deploy-preview.yml --repo codemyriad/gocassini --ref main
```

without a `run_processing` input. The observed latest run therefore used the UI-only path: `deploy-ui` succeeded and `deploy-gpu` was skipped.

When the GPU path is used, the workflow intentionally clears the branch-local raw/processed directories and exports a site from only the freshly processed set:

```yaml
rm -rf "$RAW_DIR"/* "$PROC_DIR"/*
npm run export:meetings -- --source-dir "$PROC_DIR"
rclone copy "${EXPORT_DIR}/" "codemyriad:cassini-processed/${ENCODED_BRANCH}/"
```

That means a processing run with `N=1` can publish a `catalog.json` containing only the latest processed meeting, even if older `.opus` files still exist in R2.

## Evidence gathered read-only on George

- CT 107 (`cassini-recorder`) timer is active and records daily meetings.
- CT 107 script uploads raw MKVs to:

```text
codemyriad:cassini-raw/daily/YYYY-MM-DD.mkv
```

- R2 raw daily bucket contained recordings through `2026-06-18.mkv`.
- R2 processed main bucket had `catalog.json` with 39 meetings and `meetings/` with 39 objects, but newer raw recordings after mid-May were not present as processed artifacts.
- Latest daily workflow dispatch on main showed `deploy-ui` success and `deploy-gpu` skipped.
- Local George backfill storage `/mnt/data/cassini/processed` had more `.opus` files than R2 `main/meetings`, so local processed artifacts and published R2 artifacts are not the same source of truth.

## Most likely production failure mode

The live Nextcloud ExApp publish input is probably missing older meeting bundles:

```text
$APP_PERSISTENT_STORAGE/operator/jobs/current/*.meeting
```

If only the newest bundle is present there, every successful publish will regenerate and promote a site/catalog containing only that newest meeting.

## Next read-only checks on the production Nextcloud/AppAPI host

Run against the actual Docker host for `cloud.codemyriad.io`:

```bash
docker inspect nc_app_gocassini --format '{{.Config.Image}}'
docker exec nc_app_gocassini sh -lc 'echo "$APP_PERSISTENT_STORAGE"'
docker exec nc_app_gocassini sh -lc 'find "$APP_PERSISTENT_STORAGE/operator/jobs/current" -maxdepth 1 -name "*.meeting" | sort'
docker exec nc_app_gocassini sh -lc 'python3 - <<PY
import json, os
p=os.path.join(os.environ["APP_PERSISTENT_STORAGE"], "site/published/catalog.json")
d=json.load(open(p))
print(len(d.get("meetings", [])))
print([m.get("id") for m in d.get("meetings", [])])
PY'
```

Interpretation:

- If `operator/jobs/current` has one `.meeting` and `catalog.json` has one meeting: the publish input collapsed.
- If `operator/jobs/current` has many `.meeting` bundles but `catalog.json` has one meeting: investigate `cassini publish` staging/export behavior.
- If `catalog.json` has many meetings but UI shows one: investigate viewer/catalog loading or route/cache behavior.
