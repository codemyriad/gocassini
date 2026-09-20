# The unified meeting archive

Cassini has recorded the same daily standup through four different capture
systems in six months. Their outputs live in three unrelated directory trees
under three different naming conventions, on one disk, with no snapshot and no
off-host copy. In August 2026 the oldest of those systems stopped and nobody
noticed for three business days.

This page describes the archive that consolidates all of it, the script that
builds and maintains it (`ops/cassini-archive-sync.sh`), and — just as
importantly — what it deliberately does **not** protect against.

> Every number on this page was measured on `george` as root on 2026-08-28.
> Where a figure needs a specific bundle to make sense, that bundle is named.

---

## 1. Why this exists

### The two eras, and the 6½ weeks they overlapped

```text
2026-02-19 ──────── 2026-06-10       hpb      Talk/Janus server-side recorder (mix only)
       2026-03-05 ── 2026-03-26      legacy   bare .mkv dropped in recordings/ (no raw)
       2026-03-27 ──────────── 2026-07-31     cron     CT 107, CodemyriadRecorder
                        2026-06-15 ────────────────── 2026-08-27 →   exapp   CT 112 / AppAPI, CassiniRecorder
                                   └── 6½ weeks, both running ──┘
                                                       ▲▲▲
                                                  08-03/04/05
                                                  nothing, anywhere
```

**The cron era.** From 2026-03-27 to 2026-07-31 a `cassini-record-daily.timer`
inside LXC container **CT 107** on `george` ran `/opt/cassini/bin/cassini-bin`
against the standup room on a fixed schedule (`TimeoutStartSec=7260`), writing
into `/mnt/data/cassini/recordings/daily-meeting-<date>/`. The recorder
identified itself as `CodemyriadRecorder`. A sibling container, **CT 100**, is
the one `ops/process-recordings.sh` was written to post-process from. The
`daily-meeting-*` naming convention has **no producer anywhere in this repo** —
it was a human convention, which is exactly why it is not the archive's identity
key.

CT 107 was destroyed with `vzdestroy` on **2026-08-05 08:54:51Z**. `/var/lib/vz/dump`
is empty and there is no `vm-107-disk-0` in `lvs`, so the unit's exact text is
gone. Only the unit name, the binary path and `TimeoutStartSec=7260` survive, in
`/root/.bash_history` and the sudo audit journal.

**The ExApp era.** From 2026-06-15 Cassini runs as a Nextcloud AppAPI ExApp in a
Docker container inside **CT 112**, driven by the operator, writing `.run`
bundles into the AppAPI-owned volume at
`/mnt/data/cassini-exapp/docker/volumes/nc_app_gocassini_data/_data/operator/jobs/current/`.
The recorder identifies itself as `CassiniRecorder`.

**They overlapped from 2026-06-15 to 2026-07-31.** On **19 dates** — 06-15,
06-18, 06-26, 06-30, 07-01, 07-02, 07-03, 07-06, 07-07, 07-08, 07-09, 07-14,
07-20, 07-24, 07-27, 07-28, 07-29, 07-30, 07-31 — **two bots sat in room
`mczuc3mb` at once** and produced two genuinely independent captures: different
recorder, different join time, different duration, different dropped streams.
On 2026-07-31 the cron bot started at `10:30:12.614802877Z` and the ExApp bot at
`10:30:01.201785395Z`, eleven seconds apart.

These are **not duplicates**. Nobody has compared their content — participant
sets, dropped streams, the seconds the later-joining bot missed. The archive
keeps both, links them with `concurrent_with`, and makes no quality judgement.

### The hole

The last cron-era capture is `daily-meeting-2026-07-31`, stamped 13:01 CEST.
Nothing has been written into `/mnt/data/cassini` since. CT 107 kept running for
five more days before it was destroyed, and the ExApp was mid-migration.

Business days between 2026-06-15 and 2026-08-28 with **no raw capture on either
side**: `2026-07-16`, `2026-08-03`, `2026-08-04`, `2026-08-05`, `2026-08-10`,
`2026-08-14`.

**2026-08-03, 08-04 and 08-05 are the handover hole** — three consecutive
business days, unrecoverable from either corpus. No log explains it. Nothing on
`george` reported it, because there is nothing on `george` that reports anything:
`grep -rl OnFailure /etc/systemd/system/` is empty, postfix is loopback-only with
no relayhost, and there is no metrics exporter. The same blindness is still live:
`cassini-ingest-batch.service` inside CT 112 has been `failed` with
`ExecMainStatus=2` since **2026-08-05 10:42:32Z** — three weeks — and nothing
said so.

So the archive is two things at once, and the second matters as much as the first:

1. an additive, never-mutating, never-deleting reflink mirror of every capture;
2. **a detector** — it writes a health file every run, ships an off-host
   heartbeat, and fails the run when no new capture has appeared in three
   weekdays even if the ingest itself worked perfectly.

---

## 2. What "raw data before mixing" actually means

This is the part worth being precise about, because "the recording" means three
different things at three points in the pipeline and only the first one is
recoverable into the other two.

### The three tiers

| tier | what it is | where it lives | per-speaker? |
|---|---|---|---|
| **raw** | one `.rtplog` per RTP stream — the depacketised packet log exactly as it arrived from the Talk HPB, plus a `.idx` sidecar of `(recvMonoNS, fileOffset)` pairs for seeking | `session/streams/<streamID>.rtplog` (+`.idx`) | **yes**, one file per participant stream |
| **multitrack mix container** | `recording.mkv` — every stream remuxed into one Matroska file as **separate tracks**, speaker names riding in the stream titles | `recording.mkv` at the bundle root | **yes**, one track per participant |
| **mixed** | `meeting.webm` and the portable `.opus` — all audio tracks summed into **one mono channel** | inside the built `.meeting` bundle / next to it | **no. Irreversibly gone.** |

The mixdown is `transcribe.MixDownToWebM` in
[`cassini-go-recorder/internal/transcribe/audio.go`](../cassini-go-recorder/internal/transcribe/audio.go).
Each participant track is first decoded to a gap-preserving WAV (so turn-taking
does not collapse into artificial overlap), and then:

```text
amix=inputs=N:duration=longest:normalize=0,alimiter=limit=0.95
-ac 1  -ar 48000  -c:a libopus  -b:a 64k  -vbr on  -application voip
```

`-ac 1` is the load-bearing flag. After `amix`, there is no per-speaker audio
left anywhere in the artifact — only the transcript's speaker labels, which came
from **signaling**, not from the audio. If a speaker label is wrong, or a stream
was dropped, or you want to re-run VAD/STT with a better model on one
participant, the mixed file cannot help you. The `.rtplog` can.

### The measured collapse

Take one real bundle, `01M11CF8G9FAVXB5GW0Y94VCNF.run` — the 2026-08-27 standup,
room `mczuc3mb`, 10 streams:

| artifact | bytes | ratio to raw |
|---|---|---|
| `session/streams/` — 10 `.rtplog` + `.idx` (raw) | 1,143,416,963 | 1× |
| `recording.mkv` (multitrack, audio + VP8 video) | 1,066,744,681 | 1.07× smaller |
| portable `.opus` (mixed, mono) | 17,972,938 | **≈64× smaller** |
| the whole built `.meeting` bundle | 19,210,844 | 0.87% of the 2,210,173,028 B capture |

Measured against the mkv rather than the raw, the same collapse is
**1,066,744,681 → 17,972,938 = 59×**.

That 59–64× is not compression efficiency. It is the point at which N speakers
become 1 channel. **Everything the archive is for is upstream of that number.**

For scale, the 2026-07-31 cron-era `recording.mkv` carries **7 streams**: 3×
Opus stereo + 3× VP8 + 1 attachment.

### Where raw actually exists — the definitive table

| location | bundles with raw | `.rtplog` files | raw bytes (rtplog + idx) |
|---|---|---|---|
| cron `/mnt/data/cassini/recordings/*/` | **72** of 88 dirs | 722 | **47,433,601,480** (44.18 GiB) |
| exapp `…/operator/jobs/current/*.run/session/` | **43** of 43 | 392 | **21,789,759,851** (20.29 GiB) |
| **union** | **115 bundles** | **1,114** | **≈69.2 GB / 64.5 GiB** |

Raw does **not** exist for: the 15 bare legacy `.mkv`; the 16 zero-raw failed
cron dirs; the 50 `hpb-talk-recordings/*.mkv` (except 7 stray `.mjr`,
515,748,195 B, all from one 2026-06-10 session); the 77 imported `.meeting`
bundles in `current/`; the 7 failed `runs/*.run` stubs.

Six dates where the ExApp side has no raw but the cron side does — 07-10, 07-13,
07-15, 07-21, 07-22, 07-23 — exist in `current/` **only as derived
`.meeting`/`.opus`**. Their only pre-mix copy is in
`/mnt/data/cassini/recordings/`.

---

## 3. Layout and naming

### Where it lives

```text
/mnt/data/cassini-archive/            a btrfs subvolume, root:root 0755
  ARCHIVE.md                          what this is, how to verify, how to restore
  INVENTORY.md                        generated: per-era counts, coverage calendar, known holes
  index.jsonl                         one NDJSON row per meeting; a pure derived cache
  index.jsonl.sha256
  meetings/<ANCHOR>--<ROOM>--<ERA>--<SLUG>/
      <source bytes, verbatim, at the top level>
      derived/                        .meeting / .opus bundles built from this capture
      ARCHIVE/                        meeting.json, MANIFEST.sha256, source.tsv, EXCLUDED.tsv
  by-date/<YYYY-MM-DD>/  by-job-id/<ULID>/  viewer/      symlink farms, rebuilt every run
  unattributed/{jobs,derived,hpb-mjr}/
  quarantine/                         anything unrecognised; written to, never deleted from
  state/                              ledgers, conflicts, health.json, run logs, operator DB copies
  reports/

/mnt/data/cassini-archive-snapshots/  read-only btrfs snapshots, plus a pinned 000-baseline--*
```

Only `ARCHIVE/` and `derived/` are archive-owned. Everything else in a meeting
directory is the producer's bytes, unmodified, under their original names. The
ingester asserts no source entry is ever called `ARCHIVE` or `derived` and fails
loudly if one is.

### The name

```text
meetings/<ANCHOR>--<ROOM>--<ERA>--<SLUG>/
```

* **ANCHOR** — `YYYY-MM-DDTHHMMSS`, plus a trailing `Z` **only when the instant
  is a measured UTC value the producer wrote**. No `Z` means the time was read
  out of a filename whose zone is unproven.
* **ROOM** — the 8-character Talk room token. **Never `unknown`.**
* **ERA** — `cron` | `legacy` | `hpb` | `exapp`.
* **SLUG** — `[a-z0-9-]{1,40}`, decoration only, never used for lookup.

`<ANCHOR>--<ROOM>--<ERA>` is the **identity prefix**: recomputable from immutable
producer facts, and empirically unique — all 196 meetings produce 196 distinct
keys and 196 distinct directory names, max length 58.

Where each era's facts come from:

| era | anchor | `Z` | room token from |
|---|---|---|---|
| `cron` | `session/session.json → started_wall_utc`, truncated to whole seconds | yes | `session.json → platform.room` |
| `exapp` | same | yes | same |
| `hpb` | the `Recording-<tok>-<date>_<HH-MM-SS>_<µs>.mkv` filename stamp — which is the recording **finalize** time, not the start | yes | the filename |
| `legacy` | wall-clock read out of the bare `.mkv` basename | **no** | the mkv's `TAG:title` — all 15 carry `Cassini Go Recording <token>` |

Real names, from the real corpus:

```text
2026-07-31T103012Z--mczuc3mb--cron--daily-meeting/        6 rtplog, state=ready
2026-04-15T093508Z--mczuc3mb--cron--daily-meeting/        13 rtplog, state=failed, truncated mkv
2026-03-10T123000--mczuc3mb--legacy--daily-meeting/       no Z: zone unproven. No raw.
2026-08-27T103255Z--mczuc3mb--exapp--daily-standup-meeting/
2026-06-10T134456Z--mrzd4477--hpb--talk-recording/        7 .mjr attached under janus-mjr/
```

And the doubled 2026-07-31, which is the whole reason the era token is in the
name:

```text
2026-07-31T085300Z--qv6gbwgh--exapp--ivan/                a different room, same day
2026-07-31T103001Z--mczuc3mb--exapp--daily-standup-meeting/
2026-07-31T103012Z--mczuc3mb--cron--daily-meeting/        11 s later, the other bot
```

Three decisions worth knowing about:

* **Whole seconds, not milliseconds.** In 6 of the 42 failed cron bundles —
  04-01, 04-07, 04-14, 05-13, 06-19, 07-08 — the `sessions/<recording_…Z>`
  directory name and `session.json:started_wall_utc` disagree at the
  millisecond. At whole-second precision they agree 42/42. A millisecond key
  would silently split six meetings in two.
* **Collisions refuse, they never suffix.** Two sources producing one key are
  **both** rejected, both written to `state/conflicts.tsv`, and the run exits
  non-zero. An invented `-v2` suffix would manufacture two meetings that might
  be one.
* **A changed fingerprint on an already-ingested source is a conflict, not a
  heal.** The archived copy is never touched, never overwritten.

### Viewer compatibility

The viewer parses the title and date back out of the directory basename
(`describeMeeting` in
[`cassini-viewer/src/viewer/portable.ts`](../cassini-viewer/src/viewer/portable.ts)).
Date-first names like `2026-07-31T103012Z--mczuc3mb--cron--daily-meeting` do
**not** parse — deliberately: chronological `ls` and
`ls meetings/ | cut -c1-10 | uniq` are the gap-spotting surface that the
2026-08-03/04/05 loss argues for.

So the archive additionally builds a `viewer/` symlink farm in the form
`describeMeeting`'s `modernStamp` branch does accept, which needs no colons:

| symlink | title | dateLabel |
|---|---|---|
| `daily-meeting-cron--20260731T103012.meeting` | `Daily Meeting Cron` | `2026-07-31 10:30` |
| `daily-standup-meeting-exapp--20260731T103001.meeting` | `Daily Standup Meeting Exapp` | `2026-07-31 10:30` |
| `talk-recording-hpb--20260219T121030.meeting` | `Talk Recording Hpb` | `2026-02-19 12:10` |
| `ivan-exapp--20260731T085300.meeting` | `Ivan Exapp` | `2026-07-31 08:53` |

The era token is always part of the rendered title. That is intentional: on the
19 doubled dates the viewer would otherwise show two identical
`Daily Meeting / 2026-07-31 10:30` entries. The farm is deleted and rebuilt from
scratch every run, so it cannot drift.

### `index.jsonl`

One NDJSON row per meeting, sorted by `dir`, **every field always present as an
explicit `null`** so a `jq` filter can never silently miss a row. 54 fields
covering identity (`key`, `era`, `room`, `room_source`, `anchor_kind`,
`anchor_zone_proven`), provenance (`source_paths`, `source_present`,
`source_fingerprint`, `capture_state`, `job_id`, `job_state`), content
(`has_raw`, `raw_kind`, `rtplog_count`, `raw_bytes`, `has_mix`, `media`,
`derived`, `buildable`, `remuxable`), and integrity (`sha256_manifest`,
`last_verified_utc`, `copy_mode`, `tool_sha256`).

It is a **pure derived cache**, rebuilt in full from the per-meeting
`ARCHIVE/meeting.json` files every run. Losing it costs one run, never data.

Seven ExApp jobs failed before recording anything (the room-unjoinable ones).
They get **job-only rows with `dir: null`** under `unattributed/jobs/`, so
196 meetings produce **203 index rows**. A recorded loss is data; an absent row
is not.

---

## 4. Running it

Everything is `ops/cassini-archive-sync.sh`. It runs **on the host as root**, it
never enters CT 112 and never talks to Docker, so a stopped or upgrading
container does not stop the archive.

```text
--dry-run          plan only, write nothing (THE DEFAULT — a bare invocation writes nothing)
--apply            actually write
--backfill-old     ingest the cron / legacy / hpb corpora
--sync-new         ingest ExApp .run captures, attach derived .meeting / .opus / .mjr
--all              backfill + sync + reindex + snapshot + verify slice
--snapshot         take a read-only btrfs snapshot and run the fenced pruner
--verify           re-hash the next VERIFY_BYTES_PER_RUN slice of the corpus
--rebuild-index    rebuild index.jsonl / by-date / by-job-id / viewer / INVENTORY.md
--check            rebuild the index into a temp file and diff it against the live one
--ack              acknowledge and clear /var/lib/cassini-archive/ALARM
--debounce         exit 0 if the previous run finished less than MIN_RUN_INTERVAL
                   seconds ago — for the .path unit ONLY, never for a hand run
--force            run even with --debounce
```

Exit codes: `0` ok · `2` usage or not root · `3` preflight failed · `4` operator
DB unreadable · `5` not green (a conflict, a failed assertion, or a quarantined
source) · `6` upstream quiet · `7` unacknowledged ALARM.

`--debounce` is a flag and not a global setting on purpose. When
`MIN_RUN_INTERVAL` applied to every `--apply` invocation, the two-command
backfill runbook below was silently broken: the second command ran seconds after
the first, was debounced, and exited **0 having archived nothing at all**.

### Step 0 — the one thing a human must do by hand

The script refuses to create the subvolume. `btrfs subvolume create` is the only
irreversible layout act here and it belongs to a person:

```bash
sudo btrfs subvolume create /mnt/data/cassini-archive
sudo mkdir -p /mnt/data/cassini-archive-snapshots /var/lib/cassini-archive
```

`/mnt/data` is a single top-level subvolume (`subvolid=5`) and
`btrfs subvolume list /mnt/data` is empty. Creating the archive root as its own
subvolume is legal at any path and does **not** require restructuring `/mnt/data`.
It is what makes `btrfs subvolume snapshot -r` possible at all.

### Step 1 — dry run

```bash
sudo ops/cassini-archive-sync.sh                     # dry run of everything
sudo ops/cassini-archive-sync.sh --backfill-old      # dry run, old eras only
```

Preflight asserts: root, the archive root is a btrfs subvolume, the archive root
is neither a prefix of nor prefixed by any source root, ≥200 GiB free, every
required tool present, that a `--backfill-old` pass can actually see a non-empty
old corpus (an existing-but-empty `recordings/` is what an unmounted `/mnt/data`
looks like, and it used to archive nothing and report `status: ok`), and — under
`--apply` — that `cp --reflink=always` actually works into the archive and that
every source root shares the archive's `st_dev`.

The reflink probe clones **from a file inside the archive root**, not from
`mktemp -d`. `/tmp` on george is tmpfs (`st_dev` 38) while `/mnt/data` is btrfs
(`st_dev` 48); `FICLONE` is `EXDEV` across filesystems and unimplemented on
tmpfs, so a probe sourced from `/tmp` fails unconditionally — and its failure
message points at `ALLOW_FULL_COPY=1`, i.e. at authorising the 123.6 GiB real
copy this design exists to avoid. `PrivateTmp=yes` in the units makes that
worse, not better.

### Step 2 — the one-time backfill

```bash
sudo ops/cassini-archive-sync.sh --apply --backfill-old --sync-new
```

**One invocation, both flags.** Two back-to-back commands are what the old
runbook said, and they were wrong twice over: the second was silently debounced
(above), and the acceptance counts below are asserted per pass, so the eras a
pass did not touch are the eras it cannot vouch for.

This reads **~123.6 GiB** (132,736,455,737 B) to hash it: expect **1–3 hours** at
`Nice=10` with idle I/O. It writes ~0 physical bytes, because every file is
cloned with `cp --reflink=always`. Run it by hand, in a terminal you can watch;
the timer cadence below is meaningless until it finishes.

It is resumable per meeting and cheap to re-run. A crash costs at most re-reading
the largest bundle (7.26 GB, `daily-meeting-2026-04-15`).

Acceptance is **asserted, not hoped for**, by whichever pass could have produced
the number:

| pass | asserts | how |
|---|---|---|
| `--backfill-old` | `EXPECT_CRON=88`, `EXPECT_LEGACY=15`, `EXPECT_HPB=50` | exact equality — these corpora are frozen, so any other number, in either direction, is a failure |
| `--sync-new` | `EXPECT_EXAPP=43` | a **floor**: the ExApp era keeps growing, so it must never shrink |

A mismatch is `assert_fail`, which makes the run exit 5 and the unit fail.

The rest of the expected end state is asserted by other machinery in the same
run rather than by this function, and is what you should read the plan for:
**196 meeting directories, 203 index rows** (88 + 15 + 50 + 43 meetings plus 7
job-only rows), **0** bundles under `unattributed/derived/` and
`unattributed/hpb-mjr/` (each one raises its own assertion), **0** rows in
`conflicts.tsv`, and **3** rows in `attach-low-confidence.tsv` — the three
`/work/work/<date>-recovered.mkv` imports, which are attached by date-uniqueness
and are the only low-confidence links in the corpus.

### Step 3 — ongoing sync

Install the script and its config on the host, then the units:

```bash
sudo install -m 0755 ops/cassini-archive-sync.sh        /usr/local/sbin/cassini-archive-sync
sudo install -m 0755 ops/cassini-archive-healthcheck.sh /usr/local/sbin/cassini-archive-healthcheck
sudo install -m 0755 ops/cassini-archive-alert.sh       /usr/local/sbin/cassini-archive-alert
sudo install -m 0644 ops/cassini-archive.default        /etc/default/cassini-archive
sudo install -m 0644 ops/cassini-archive-*.{service,timer,path} /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now cassini-archive-sync.timer cassini-archive-snapshot.timer
sudo systemctl enable --now cassini-archive-healthcheck.timer
sudo systemctl enable --now cassini-archive-sync.path     # optional
```

Everything lives directly in `ops/` — there is no `ops/systemd/` directory, and
the watchdog unit is called `cassini-archive-healthcheck`, not
`cassini-archive-watchdog`. `ops/README.md` carries the authoritative, complete
install sequence.

The unit contract, which matters more than the file names:

| unit | schedule | why |
|---|---|---|
| `…-sync.path` | `PathChanged=…/operator/jobs/current` | fires within seconds of a promotion; the unit passes `--debounce`, so `MIN_RUN_INTERVAL=300` collapses a burst of promotions into one pass |
| `…-sync.timer` | `OnCalendar=*-*-* 05:20,13:40,21:40`, `RandomizedDelaySec=10m`, `Persistent=true` | 13:40 local catches the ~13:15 CEST standup completion; 21:40 writes the heartbeat ~6 h before restic uploads it; 05:20 mops up |
| `…-healthcheck.timer` | `OnCalendar=*-*-* 07:30,19:30`, `RandomizedDelaySec=5m` | a **different unit on a different schedule**, because `OnFailure=` cannot fire for a unit that was disabled, masked, or never scheduled — which is exactly how `cassini-ingest-batch.service` sat failed for 21 days |

**Do not collide with** the daily `cassini-exapp-backup` window (03:30–03:50,
it reads the same tree) or the monthly `0 2 1 * *` `btrfs scrub -B /mnt/data`.

Both work units are `Type=oneshot`, `User=root`, `Nice=10`,
`IOSchedulingClass=idle`, `TimeoutStartSec=6h`,
`EnvironmentFile=-/etc/default/cassini-archive`,
`flock -n /run/lock/cassini-archive.lock` — matching the `cassini-exapp-backup`
precedent — plus the single most valuable line in the whole design:

```ini
ReadOnlyPaths=/mnt/data/cassini /mnt/data/cassini-exapp
ReadWritePaths=/mnt/data/cassini-archive /mnt/data/cassini-archive-snapshots /var/lib/cassini-archive
```

A bug that tries to write, rename or unlink anything in either source corpus gets
`EROFS` and a failed unit, not a deleted meeting. The script enforces the same
thing itself: every mutating operation goes through an `a_*` wrapper that calls
`guard_write()` first, and a path outside the writable roots is fatal.

A representative `ExecStart` for the timer:

```ini
ExecStart=/usr/local/sbin/cassini-archive-sync --apply --sync-new --snapshot --verify
```

### Step 4 — snapshots

```bash
sudo ops/cassini-archive-sync.sh --apply --snapshot
```

Snapshots go to `/mnt/data/cassini-archive-snapshots/<YYYY-MM-DDTHHMMSSZ>/`. The
archive is append-only and never rewrites a file, so a snapshot pins essentially
metadata; the retention policy is generous on purpose — every snapshot from the
last 14 days, the latest of each ISO week for 8 weeks, the latest of each month
for 24 months, and `000-baseline--*` pinned forever.

Deletion is the only destructive operation in this system, so the pruner is
fenced four ways: `btrfs subvolume delete` only (never `-c`), only on entries
directly under the snapshots dir matching `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{6}Z$`;
it refuses to touch the newest snapshot or the baseline; it refuses to run if
fewer than 3 snapshots exist or the newest is older than 30 h; and **no single
run may delete more than 3 snapshots**. A date-math bug cannot wipe the history
in one pass.

### Step 5 — health, and the only channel that survives george dying

Every run writes `state/health.json` — run id, start/finish, status, meeting and
index counts, `newest_capture_utc`, `operator_db_max_record_finished_at`,
`capture_lag_seconds`, `quiet_weekdays`, snapshot freshness, free bytes,
`copy_mode`, `assertions_failed[]`, `script_sha256` — and copies it to:

```text
…/nc_app_gocassini_data/_data/operator/backups/cassini-archive-health.json
```

That path is not arbitrary. `/usr/local/sbin/cassini-exapp-backup` does **not**
back up `$SRC` wholesale; it backs up an explicit path list, and
`operator/backups` is on it (`[ -e "$p" ] && paths+=("$p")`), and a `.json`
matches none of its excludes. So the heartbeat ships to R2 nightly at 03:30
**with zero edits to the production backup script**.

Reading it back is a **cross-repo dependency and must be tracked as one**: one
step added to `codemyriad/systems`' `backup-verify.yml` (daily 06:00 UTC,
read-only R2 token, `--no-lock`) that `restic dump`s the heartbeat and fails if
`last_run_finished_utc` is more than 30 h old, `last_run_status != ok`, or
`assertions_failed` is non-empty. **Until that step lands, every detection
channel lives on george and dies with it.**

On-host, three more channels: the watchdog's nine assertions (including
`systemctl is-failed` over a unit list seeded with `cassini-exapp-backup.service`
— the exact check that would have caught the three-week failure); an upstream
liveness check that **fails the run when no new capture has appeared in
`MAX_QUIET_WEEKDAYS=3` weekdays even though the ingest worked perfectly**
(over the real 2026-03→08 history a threshold of 3 fires **twice**: on the
2026-07-31 → 08-06 incident this project exists to prevent, and on one genuine
quiet week, 2026-05-13 → 05-19. Raising it to 4 changes nothing — both gaps are
4 weekdays — and 5 would miss the incident, so 3 stands. Expect roughly one
false positive per quarter, and a company shutdown of a week or more every year:
that is what `EXPECTED_QUIET_UNTIL` is for, and declaring a holiday on the record
is the point); and an `ALARM` file written to
`/var/lib/cassini-archive/`, logged at `daemon.crit`, `wall`ed, and printed on
every SSH login by `/etc/update-motd.d/98-cassini-archive`.

**The ingest refuses to report green while an unacknowledged ALARM exists.**
Clearing it is an explicit `--ack`, so an alarm cannot be erased by the next
successful run.

Holidays are silenced with `EXPECTED_QUIET_UNTIL=YYYY-MM-DD` in
`/etc/default/cassini-archive` — deliberately, on the record, rather than the
alarm being learned-ignored.

### Verifying a meeting by hand

```bash
cd /mnt/data/cassini-archive/meetings/2026-07-31T103012Z--mczuc3mb--cron--daily-meeting
sha256sum -c ARCHIVE/MANIFEST.sha256
```

A rotating re-verify (`VERIFY_BYTES_PER_RUN=4G`, oldest-verified-first cursor in
`state/verify.json`) re-hashes the whole 123.6 GiB corpus every ~40 days, so bit
rot is found on a bounded schedule rather than at restore time.

---

## 5. The data-safety position — stated plainly

**Backup for this archive is local btrfs snapshots only. There is no off-host
copy of the ~69 GB raw corpus. This is a decision, not an oversight, and it is
an accepted exposure.**

What is actually true today:

* The daily restic job's excludes are literally `--exclude '*.rtplog' --exclude
  '*.idx' --exclude '*.mkv'` with the in-script comment "Deliberately excluded:
  raw capture intermediates". So the **entire raw pre-mix corpus (69.2 GB) and
  every `recording.mkv` (44.9 GB across both sides) has no off-host copy.**
* `/mnt/data/cassini` is outside that job's `$SRC` entirely — zero backup, zero
  snapshots, until this archive exists.
* After this work lands, that is **still true of the bytes**. The archive gives
  the raw corpus an independent inode and an independent namespace, and local
  read-only snapshots. It does not give it a second device.

What the archive genuinely defends against:

* an operator or ExApp bug deleting from `current/`;
* a future `-artifact-retention` policy sweeping the volume;
* an AppAPI app update acting on its own storage;
* accidental `rm` in a source tree;
* silent bit rot — **detected**, on a ~40-day cycle, by the rotating sha256 plus
  the monthly `btrfs scrub`.

What it does **not** defend against, and cannot:

* **`/dev/sda1` dying.** One event loses the 69.2 GB raw corpus, the archive, the
  index, and every snapshot. Local snapshots are not a backup.
* **A bad extent.** A reflink is an independent inode with **shared extents**.
  One damaged extent damages source and archive together, and read-only snapshots
  pin the same extents. On a single-device data profile, nothing repairs it — the
  rotating hash tells you, it does not fix it.

To close the exposure, something has to change:

1. **Extend the existing restic job**, or add a separate media-only repo and
   timer, pointed at `/mnt/data/cassini-archive` without the
   `*.rtplog/*.idx/*.mkv` excludes. Cost: the nightly run goes from 2.960 GiB in
   0:03 to **~180 GB initial plus ~272 GiB/year** to R2, with matching storage
   and egress. Note the existing job's safety contract — it "only ever writes; it
   never calls forget, prune or unlock" — so retention would have to be designed
   alongside it.
2. **Or a second physical device**, and `btrfs send`/`receive` of the snapshots to
   it. Cheapest in bandwidth, still same-building.
3. **Or accept mixed-only durability**: back up `derived/` and `index.jsonl`
   off-host, and keep raw single-copy on purpose. That is roughly what happens
   today by accident; doing it deliberately at least makes the boundary visible.

Space is not the obstacle. Measured growth is **≈5.2 GiB/week ≈ 272 GiB/year**
(51.9% raw / 48.1% mixed), against **7.58 TiB free**. Cassini alone would take
decades to fill the volume — though `/mnt/data` also holds jellyfin (2.6 T,
synced every 15 minutes) and `ai` (501 G), so the free space is not exclusively
Cassini's to plan against.

---

## 6. Operational gotchas

These will each bite exactly one person, once, expensively.

### It must run as root, and a non-root run lies

42 of the failed cron bundles carry `recording-segments-*/artifact-remux-work/`
directories at **mode 0700**. A non-root traversal cannot enter them, so `du`,
`find` and `rsync` all skip **40,248,765,505 B (37.5 GiB)** — **and exit 0**.
This is exactly how the same tree got reported as 77 G and 114 G in different
audits.

The script hard-fails with `exit 2` if `id -u != 0`, and every bundle asserts
`copied + excluded == total` over regular-file bytes. A one-byte mismatch aborts
that bundle, writes `reports/anomalies.tsv`, and fails the run. A
permission-hidden subtree can no longer be lost silently.

That 37.5 GiB of orphaned crashed-remux scratch is **excluded from the archive,
byte-counted in `state/EXCLUDED.tsv`, and left in place**. Deleting it is a
separate human decision and the script will never make it.

### The unprivileged-LXC uid mapping

CT 112 is `unprivileged: 1` with map `0 100000 65536`. That produces a confusing
ownership map on the host:

| path | host owner | inside CT 112 |
|---|---|---|
| `/mnt/data/cassini` | `0:0` | `nobody:nogroup`, **not writable** |
| `/mnt/data/cassini/recordings` | `100000:100000` | container-root, writable |
| `/mnt/data/cassini/hpb-talk-recordings` | `0:0` | `nobody:nogroup`, **not writable** |
| `/mnt/data/cassini/previews` | `101000:101000` | container uid 1000 |
| the whole ExApp volume | `100000:100000` | container-root, writable |

So a container-side job can append into `recordings/` but **cannot create a new
sibling under `/mnt/data/cassini/`** without a host-side `mkdir` + `chown`. A
host-side job sidesteps all of it — which is why this one is host-side.

The archive is deliberately **`root:root`, dirs `0555`, files `0444`**, and
explicitly *not* `100000:100000`: CT 112 physically cannot write it. Only the
host timer can. A meeting directory is flipped to `0755` only for the duration of
a single `rename(2)` when a derived subtree is attached, then sealed again.

### The ExApp volume is AppAPI-owned storage

`…/docker/volumes/nc_app_gocassini_data/_data` belongs to AppAPI, not to us. An
app update, a redeploy, or a future retention policy acts on it. The operator's
own `reconcilePromotionStaging` already deletes things it does not recognise
under `current/.staging`.

Empirically, nothing has ever pruned `current/`: **0 of 52 `artifact retention`
log lines mention it**. That is a measurement of today's binary, not a guarantee.
Treat `current/` as a working directory that happens to be durable, and the
archive as the thing that is durable on purpose.

Related: **never set `CASSINI_OPERATOR_WORK_ROOT`.** `exapp.go` redirects the
work root into `<APP_PERSISTENT_STORAGE>/operator/jobs` only when the env value is
empty **or byte-identical to the image default** `/var/lib/cassini-operator/jobs`.
Setting it to anything else defeats the redirect and puts new bundles on the
32 GiB container overlay, where an app update destroys them.

### The deployed source is not in this checkout

`george` runs `APP_VERSION=0.2.0-beta.2`, binaries built 2026-08-19/20. This
checkout is `v0.2.0-alpha.2-49-g6ed9747`, last commit **2026-07-10**, and there
is **no beta.2 tag or branch anywhere**.

Consequences you have to hold in your head:

* The in-progress gates (promotion ordering in `artifact_promotion.go`, the
  `state:"ready"` predicate in `runbundle.go`) were read from **alpha-2 source**.
  If beta.2 changed promotion ordering, a still-growing bundle could in principle
  be archived as complete. The two gates are independent guards, but they were
  read from the same unverifiable producer.
* `-sink nextcloud-files` has been active since 2026-08-21 with **no
  corresponding env var** and a `--help` that still says `default local`. Nobody
  can read why.
* `-artifact-retention all|superseded` semantics cannot be read. The help text
  says "under `runs/`". That is the single largest standing risk to treating
  `current/` as durable.

### Smaller traps, in the order you will hit them

* **`jobs.artifact_run_path` is a *container* path**
  (`/nc_app_gocassini_data/operator/jobs/current/<ULID>.run`). Comparing it to a
  host path without translating the prefix matches nothing — the ingest would
  skip every new capture and exit 0. `CONTAINER_DATA_PREFIX` is configurable so a
  volume move fails loudly instead.
* **`jobs.sqlite3` has no `room` column.** The room token comes from
  `talk_binding.room_token`, with `request_json.roomToken` as fallback. And
  `talk_binding.room_name` is a **viewer-relative Talk conversation name** — room
  `btsq78i8` is `"Chris"` to owner `silviot` — so it is metadata, never identity.
  17 older rows carry no `room_name` at all; those ExApp slugs become `untitled`.
* **`talk_delivered_at` does not exist.** Migration 4 (`talk_delivery`) is
  recorded applied but only half-applied. **The DB cannot tell you which
  recordings actually reached Nextcloud.**
* **The DB cannot index `.opus`.** Only 8 of 50 rows carry
  `artifact_opus_path`; 45 `.opus` exist on disk, and packing runs in an async
  goroutine off the stage machine. `opus_attached` is therefore re-checked with a
  `stat()` every pass, forever, with no completion latch.
* **Colons in filenames break `ffprobe`.** `daily-meeting-2026-03-10--12:30.mkv`
  is read as a URL protocol unless you pass `ffprobe -i "file:$path"`. This is
  also why no generated name in the archive contains a colon.
* **The hpb anchor is a finalize time**, while every other era's is a start time.
  Chosen because the filename stamp is byte-stable, whereas
  `end − ffprobe_duration` would silently re-key a meeting after an ffprobe
  upgrade. `anchor_kind` records it on every row. It is a wart; a reader will
  trip over it once.
* **Ingesting hpb takes the meeting count from 146 to 196**, and 28 of its 50
  files are room `erwcr27x`, not the standup. Every naive "how many meetings do
  we have" over-counts. Filter on `era`.
* **Nothing reconciles against Nextcloud.** Every succeeded Talk job also uploads
  to the room owner's Talk folder (4 different owners) and, since 2026-08-21, to
  `Cassini/Recordings`. Those copies are mixed-only, spread across users,
  user-deletable, and there is no `talk_delivered_at` to reconcile against. The
  archive quietly becomes the sole system of record without ever asserting that
  it is.
* **`previews/` (4,990,592,058 B) is excluded on an unproven assumption** — that
  it duplicates `recordings/`. Sizes match on 5 dates; nothing is checksummed.
  `--previews` ships a verifier, but the backfill does not block on its result,
  so the archive can be declared complete while possibly-unique media sits
  outside it.

---

## 7. History and provenance

Everything that exists, by era, as measured on 2026-08-28.

| era | source | span | items | measured bytes | pre-mix raw |
|---|---|---|---|---|---|
| **hpb** — Talk/Janus server-side recorder, pre-Cassini | `/mnt/data/cassini/hpb-talk-recordings/` | 2026-02-19 → 2026-06-10 | 83 files: 50 `.mkv`, 26 `.json` sidecars, 7 `.mjr` | 6,581,009,048 (6.13 GiB) | **almost none** — only the 7 `.mjr` (515,748,195 B), all one 2026-06-10 session |
| **legacy** — bare mixdowns dropped in `recordings/` | `/mnt/data/cassini/recordings/*.mkv` | 2026-03-05 → 2026-03-26 | 15 files | 8,701,273,663 (8.10 GiB) | **none** |
| **cron** — CT 107, `CodemyriadRecorder` | `/mnt/data/cassini/recordings/<dir>/` | 2026-03-27 → 2026-07-31 | 88 dirs: **46 `ready`** (`session/streams/`), **42 `failed`** (`sessions/<id>/streams/`) | 113,592,027,749 for the dirs, of which **40,248,765,505 is excluded** orphan remux scratch | **72 of 88** — 722 `.rtplog` + 722 `.idx` = **47,433,601,480** (44.18 GiB) |
| **exapp** — CT 112 / AppAPI, `CassiniRecorder` | `…/_data/operator/jobs/current/` | 2026-06-15 → 2026-08-27 | 43 `.run`, 120 `.meeting` (43 ULID + 77 imported), 45 `.opus` | 43,989,920,390 (40.97 GiB) | **43 of 43** — 392 `.rtplog` + 392 `.idx` = **21,789,759,851** (20.29 GiB) |

Rolled up:

| | |
|---|---|
| meeting directories in the archive | **196** (88 cron + 15 legacy + 50 hpb + 43 exapp) |
| index rows | **203** (196 + 7 job-only rows for zero-media failed jobs) |
| bytes ingested | **132,736,455,737** (~123.6 GiB logical, ~0 physical under reflink) |
| bytes deliberately excluded | **40,248,765,505** orphan remux scratch + **4,990,592,058** `previews/` |
| pre-mix raw, both eras | **1,114 `.rtplog` across 115 bundles ≈ 69.2 GB / 64.5 GiB** |
| dates with two independent concurrent captures | **19** |
| business days lost at the handover | **3** (2026-08-03, 08-04, 08-05) — unrecoverable |
| growth | ≈5.2 GiB/week ≈ 272 GiB/year, 51.9% raw / 48.1% mixed |
| free space on `/mnt/data` | 7.58 TiB |

Distribution details worth keeping:

* **Rooms.** ExApp jobs: `mczuc3mb` ×36, `btsq78i8` ×6, `qv6gbwgh` ×4,
  `erwcr27x` ×2, `7uf6nh9i` ×1, `mrzd4477` ×1. hpb `.mkv`: `erwcr27x` ×28,
  `mczuc3mb` ×11, `kf6ke5xo` ×8, plus one each of `7uf6nh9i`, `eyfev7pt`,
  `mrzd4477`.
* **Owners** of the 50 ExApp jobs: Chris 27, silviot 11, Ivan 6, Alex 6.
  43 succeeded, 7 failed.
* **16 cron dirs carry zero raw**, all `failed`: 2026-03-30-part2, -part2b,
  04-03, 04-06, 04-13, 05-01, 05-19, 05-20, 05-21, 05-22, 05-25, 06-02, 06-03,
  06-11, 06-12, 07-20.
* **2026-03-24 exists nowhere except `hpb-talk-recordings/`.** That is why the
  hpb era is ingested at all.
* **77 of the 120 `.meeting` bundles in `current/` are derived imports**, not
  captures: 59 from a 2026-06-24 pass (`source_kind=mkv`), 8 from 2026-08-05
  (`/src/…`), 7 built on 2026-08-04 from the old bundles
  (`source_path=/src/daily-meeting-<date>`, dates 06-25, 06-29, 07-10, 07-13,
  07-15, 07-21, 07-22), and 3 from `/work/work/<date>-recovered.mkv` (04-23,
  05-29, 07-23). `/work` no longer exists in any container and no script in this
  repo performs that recovery; those 3 attach by date-uniqueness and are logged
  in `state/attach-low-confidence.tsv`.
* **Every meeting in the whole corpus starts between 07:49 and 21:00 local.** No
  plausible ±2 h timezone error can move any of them across a date boundary, so
  the `YYYY-MM-DD` prefix is unambiguous even for the 15 legacy names whose zone
  is unproven.

---

## See also

* `ops/cassini-archive-sync.sh` — the implementation; its header comment is the
  authoritative option and exit-code reference.
* [Artifacts and filesystem](./reference/artifacts-and-filesystem.md) — what a
  `.run`, `.meeting`, `.site` and `.opus` each contain.
* [Core pipeline](./core-pipeline.md) — record → remux → transcribe → publish,
  and where the mixdown sits in it.
* [Audio & media glossary](./audio-glossary.md) — RTP, rtplog, remux, VAD/STT.
