# Production archive backup (D-568)

The production Cassini archive lives in exactly one place — the ExApp Docker volume
on **george**, CT 112 (`cassini-exapp-gpu`). George has no host-level backups. This
document describes the off-host replica that fixes that, and how to restore from it.

## What runs where

| | |
|---|---|
| Script | `/usr/local/sbin/cassini-exapp-backup` (source: `deployment/cassini-exapp-backup.sh`) |
| Units | `cassini-exapp-backup.service` + `.timer` (source: `deployment/`) |
| Schedule | daily 03:30 UTC+randomised 20 min, `Persistent=true` (catches up after downtime) |
| Runs on | the **george host** as root — not inside CT 112, so a stopped container does not stop the backup |
| Reads | `/mnt/data/cassini-exapp/docker/volumes/nc_app_gocassini_data/_data` |
| Writes | Cloudflare R2 bucket `cassini-exapp-backups`, prefix `george-ct112` |
| Config | `/etc/default/cassini-exapp-backup` — **no secrets** |
| Credentials | `/root/.config/rclone/rclone.conf` (root-only, mode 0600), remote `r2` |
| Logs | `journalctl -u cassini-exapp-backup.service` |

### Safety contract

- **Additive only.** The script uses `rclone copy` exclusively — never `sync`,
  `delete`, `move`, `purge` or `cleanup`, in either direction. A file removed from
  production stays in R2.
- Production is opened **read-only**. The SQLite database is copied through
  SQLite's online-backup API with the source opened `mode=ro`, so a concurrent
  writer cannot produce a torn file and the backup cannot create `-wal`/`-shm`
  sidecars in the production directory.
- `flock` prevents overlapping runs.

## Layout in R2

```
cassini-exapp-backups/george-ct112/
  delivery/site/published/**        # the viewer's delivery path
  delivery/operator/**              # settings.json, app-state.json, jobs.sqlite3*, backups/
  sources/operator/jobs/current/**  # .meeting bundles the site is rebuilt from
  history/operator/jobs/runs/**     # per-attempt job history, text artefacts only
  snapshots/<UTC-ts>/               # point-in-time manifest + consistent SQLite copy
  LATEST                            # timestamp of the most recent snapshot
```

Each `snapshots/<ts>/` holds `jobs.sqlite3` (consistent copy), `jobs.counts.tsv`,
`published.sha256`, `published.manifest.tsv`, `catalog.json`, `cassini.json`,
`settings.json`, `app-state.json` and `summary.txt`. The bulk tiers accumulate the
*union* of everything ever seen; the snapshots record *which* files were live when.

## Scope: what is backed up, and what is not

The volume is ~57 GB. The backup is **~2.0 GB** (1478 objects).

**Included**

| Tier | Size | Why |
|---|---|---|
| `delivery/` | 771 MiB | Irreplaceable. `site/published` is what the viewer serves: `catalog.json` plus 84 **portable `.opus`** meetings, which embed the transcript alongside the audio (see `docs/portable-meeting-format.md`). Losing this loses the product. |
| `sources/` | 1.01 GiB | `operator/jobs/current` minus bulk media — the `.meeting` bundles (`manifest.json`, `meeting.webm`, `transcript.words.v1.json`). `cassini publish` rebuilds the whole site from this directory, so it is the regeneration path if `delivery/` is ever corrupted. Verified to cover **all 84** published meeting ids. |
| `history/` | 209 MiB | `operator/jobs/runs`, text artefacts only — per-attempt logs and manifests, including the 7 job ids that exist *only* in `runs/` (failed records, 17 KB total). |

**Deliberately excluded (~55 GB)**

- `*.rtplog` (10.3 GB) and `*.idx` (228 MB) — raw RTP capture streams and their
  indices. Pipeline intermediates; nothing downstream reads them once a job has
  produced its `.meeting` bundle.
- `*.mkv` (9.8 GB) — raw screen/AV capture. The audio that the product actually
  serves survives twice over in `meeting.webm` (sources) and the portable `.opus`
  (delivery); the video is not on the delivery path.
- Media inside `operator/jobs/runs/` — per-attempt `.opus` (12 GB) and `.webm`
  (2 GB), i.e. republished copies of artefacts already held in `delivery/`.
- `site/published.bak-*` (1.15 GB) — three June snapshots, verified to be strict
  **subsets** of the live site (62 of 84 meetings, zero unique ids).
- `.staging/` directories and `site/published.staging` — transient publish scratch.

Note that `operator/jobs/runs/<id>--attempt-001.run/` is a **byte-identical copy**
of `operator/jobs/current/<id>.run/` (spot-checked with `cmp`; no hardlinks). Roughly
half the 56 GB in `operator/jobs` is literal duplication — relevant to the retention
policy question in D-568.

Widening or narrowing scope is a one-line change to the exclude arrays at the top of
the script.

## Restore

All commands run on the george host as root (that is where the R2 credentials are).
**Never restore over the live volume** — restore to scratch, verify, then move.

```bash
export HOME=/root
D=r2:cassini-exapp-backups/george-ct112
R=/mnt/data/tmp/cassini-restore          # scratch, NOT the production volume
TS=$(rclone cat "$D/LATEST")             # or pick from: rclone lsf "$D/snapshots/"

mkdir -p "$R"
rclone copy "$D/snapshots/$TS"          "$R/snapshot"
rclone copy "$D/delivery/site/published" "$R/site/published" --transfers 8
```

Verify before trusting it:

```bash
# 1. the job database opens, is intact, and has the expected row counts
sqlite3 "$R/snapshot/jobs.sqlite3" 'pragma integrity_check;'          # -> ok
sqlite3 "$R/snapshot/jobs.sqlite3" 'select count(*) from jobs;'
cat "$R/snapshot/jobs.counts.tsv"

# 2. every published file matches the sha256 recorded at snapshot time
( cd "$R/site/published" && sha256sum -c "$R/snapshot/published.sha256" )

# 3. a restored meeting is a valid portable artifact (audio + embedded transcript)
pct exec 112 -- docker cp <file>.opus nc_app_gocassini:/tmp/probe.opus
pct exec 112 -- docker exec nc_app_gocassini /usr/local/bin/cassini inspect /tmp/probe.opus
```

Then put it back. Stop the ExApp container first so the operator is not writing:

```bash
pct exec 112 -- docker stop nc_app_gocassini
V=/mnt/data/cassini-exapp/docker/volumes/nc_app_gocassini_data/_data
mv "$V/site/published" "$V/site/published.pre-restore-$(date -u +%Y%m%d-%H%M%S)"
cp -a "$R/site/published" "$V/site/published"
cp -a "$R/snapshot/jobs.sqlite3" "$V/operator/jobs.sqlite3"
chown -R 100000:100000 "$V/site/published" "$V/operator/jobs.sqlite3"   # unprivileged LXC uid shift
pct exec 112 -- docker start nc_app_gocassini
```

`chown 100000` matters: CT 112 is an **unprivileged** LXC, so container root is uid
100000 on the host. Files restored as host root are unreadable to the ExApp.

To rebuild the site from `sources/` instead of restoring `delivery/` verbatim,
restore `sources/operator/jobs/current` and run `cassini publish <current> --out <site>`.

## Total loss of george

`delivery/` + the newest snapshot is enough to stand the viewer back up on any host:
it is a self-contained static site. `sources/` additionally lets you re-run the
pipeline. Neither depends on anything left behind on george.

## Known gaps

- The timer authenticates with the **account-wide** R2 remote (`r2`). It should get
  an R2 token scoped to `cassini-exapp-backups` with object write only; point
  `RCLONE_REMOTE` in `/etc/default/cassini-exapp-backup` at the new remote and
  nothing else changes.
- No alerting on failure. `systemctl status cassini-exapp-backup.timer` or an
  `OnFailure=` unit would close this.
- R2 has no object-versioning or lifecycle policy on this bucket. `copy`-only
  semantics mean the backup cannot delete, but a compromised key still could.
