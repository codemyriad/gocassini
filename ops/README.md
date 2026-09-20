# `ops/` — host-side operations scripts

Scripts and unit files that run on **hosts**, not in CI and not in the dev
stack. Everything here follows the same conventions: `set -euo pipefail`, a
header comment saying what it is and how to run it, and `${VAR:-default}`
environment overrides.

| file | what it is |
|---|---|
| `process-recordings.sh` | batch remux/transcode helper for a directory of recordings |
| `proxmox-sync-jellyfin-nvidia.service` | NVIDIA passthrough sync for CT 103 (see `docs/proxmox-jellyfin-nvidia.md`) |
| `cassini-archive-*` | the unified Cassini meeting archive — see below |

---

# The unified Cassini meeting archive

One additive, never-mutating, never-deleting reflink mirror of every meeting
Cassini has ever recorded — the old CT 107 cron era, the bare legacy mixdowns,
the hpb Talk/Janus recordings, and the new Nextcloud ExApp captures — under
`/mnt/data/cassini-archive/`, with the raw pre-mix `.rtplog` streams kept
alongside each mixdown.

It is also a **detector**. The previous pipeline stopped on 2026-07-31 with
every unit green and cost three unrecoverable business days, and
`cassini-ingest-batch.service` sat `failed` for three weeks unnoticed. george
has no mail, no metrics exporter and no messaging credentials, so detection is
built from four independent channels that all work on this host (see
*Failure detection* below).

Design, corpus numbers and the full runbook: **`docs/meeting-archive.md`**.

## Files

| file | installs as | role |
|---|---|---|
| `cassini-archive-sync.sh` | `/usr/local/sbin/cassini-archive-sync` | backfill + ongoing ingest + snapshot + verify + index |
| `cassini-archive-healthcheck.sh` | `/usr/local/sbin/cassini-archive-healthcheck` | 15 freshness/consistency assertions; exit 3 when any fails |
| `cassini-archive-alert.sh` | `/usr/local/sbin/cassini-archive-alert` | `OnFailure=` handler: durable marker, journal, wall, heartbeat stamp, optional notifier |
| `98-cassini-archive-motd` | `/etc/update-motd.d/98-cassini-archive` | prints an unacknowledged alarm at every SSH login |
| `cassini-archive.default` | `/etc/default/cassini-archive` | every knob, shared by all three scripts and by the units |
| `cassini-archive-alert.default` | `/etc/default/cassini-archive-alert` | optional notifier config (may hold a token → 0600) |
| `cassini-archive-sync.service` / `.timer` | `/etc/systemd/system/` | ingest, 05:20 / 13:40 / 21:40 local |
| `cassini-archive-backfill.service` | `/etc/systemd/system/` | the one-time backfill, under the same sandbox; started by hand, no timer |
| `cassini-archive-sync.path` | `/etc/systemd/system/` | **optional** low-latency trigger on capture promotion |
| `cassini-archive-snapshot.service` / `.timer` | `/etc/systemd/system/` | nightly read-only btrfs snapshot + fenced prune + one integrity slice, 22:50 |
| `cassini-archive-healthcheck.service` / `.timer` | `/etc/systemd/system/` | watchdog, 07:30 / 19:30 |
| `cassini-archive-alert@.service` | `/etc/systemd/system/` | the `OnFailure=` target of all three units above |

## What runs when

| local time | unit | why that slot |
|---|---|---|
| 05:20 (+≤10 m) | `cassini-archive-sync` | mop-up for anything that landed overnight |
| 07:30 (+≤5 m) | `cassini-archive-healthcheck` | before the working day, so a stopped recorder is known before the standup |
| 13:40 (+≤10 m) | `cassini-archive-sync` | after the ~13:15 CEST standup has finished building |
| 19:30 (+≤5 m) | `cassini-archive-healthcheck` | a failed 13:40 pass is noticed the same evening |
| 21:40 (+≤10 m) | `cassini-archive-sync` | late meetings; refreshes the heartbeat ~6 h before it is uploaded |
| 22:50 (+≤10 m) | `cassini-archive-snapshot` | one hour after the last ingest, so the snapshot pins that day's work |

Deliberately clear of everything else on george:
`cassini-exapp-backup.timer` at **03:30 + 20 m jitter**, the monthly
`0 2 1 * * btrfs scrub -B /mnt/data`, and `e2scrub_all` / `xfs_scrub_all` at
03:10. Both work units also take an outer `flock` on
`/run/lock/cassini-archive-unit.lock`, so if a long ingest overruns, the
snapshot **waits** instead of being silently skipped by the script's own
`flock -n`.

## Failure detection

1. **Off-host.** Every run writes `state/health.json` and copies it to
   `…/_data/operator/backups/cassini-archive-health.json`. That directory is
   already in `/usr/local/sbin/cassini-exapp-backup`'s path list and `.json`
   matches none of its excludes, so the heartbeat ships to R2 nightly **with
   zero edits to the production backup script**. Asserting on its freshness
   from `codemyriad/systems`' `backup-verify.yml` is a cross-repo follow-up and
   must be tracked as one — until it lands, every channel lives on george.
2. **A watchdog that is not the ingest.** `cassini-archive-healthcheck` is a
   different unit on a different schedule, because `OnFailure=` cannot fire for
   a unit that was disabled, masked or never scheduled — which is exactly how
   `cassini-ingest-batch.service` failed for three weeks. Among its assertions:
   *is a finished capture sitting in the ExApp volume unarchived* (A12), *are
   the watched units failed* (A10), and *are their timers still active* (A11).
3. **Upstream liveness.** The 2026-08-03/04/05 loss was *no new recordings*
   with everything green, so both the ingest and the healthcheck fail when no
   new meeting has been captured in `MAX_QUIET_WEEKDAYS` (3) weekdays.
   Replayed over the real 2026-03→08 calendar, 3 fires **twice**: the real
   incident (2026-07-31 → 08-06) and one genuine quiet week (2026-05-13 →
   05-19). 4 fires on the same two — both gaps are 4 weekdays — and 5 would miss
   the incident, so 3 stands. Budget for roughly one false positive a quarter
   and one company shutdown a year, and declare both with
   `EXPECTED_QUIET_UNTIL`; a latched `ALARM` also needs an explicit
   `--apply --ack`.
4. **In your face on-host.** `OnFailure=cassini-archive-alert@%n.service`
   writes `/var/lib/cassini-archive/ALARM`, a `daemon.crit` journal record and a
   `wall` broadcast; the motd hook reprints it at every login; and
   `cassini-archive-sync` **refuses to report green while the marker exists**.
   Clearing it is an explicit `cassini-archive-sync --apply --ack`.

An optional notifier (`ALERT_WEBHOOK_URL` or `ALERT_COMMAND` in
`/etc/default/cassini-archive-alert`) is delivered best-effort. When it is
unconfigured or fails, the handler logs it and still exits 0 — a broken
notifier must never look like a broken archive.

---

## INSTALL — the exact commands to run on george

Run these **as root on george**, from a checkout of this repo (`cd` into it
first; `$REPO` below is that checkout). Nothing in this repo installs itself.

### 1. Create the archive root as a btrfs subvolume

`/mnt/data` is a single top-level btrfs subvolume (subvolid 5) with no nested
subvolumes. Creating the archive root as its own subvolume is legal at any path
and is what makes read-only snapshots possible. **This is the one irreversible
layout decision, so the scripts deliberately do not do it for you** —
`cassini-archive-sync` refuses to run until it exists.

```bash
btrfs subvolume create /mnt/data/cassini-archive
mkdir -p /mnt/data/cassini-archive-snapshots /var/lib/cassini-archive

# root:root on purpose. CT 112 is unprivileged (host 0 -> 100000), so with
# these owners the container physically cannot write the archive: only the
# host timer can.
chown root:root /mnt/data/cassini-archive /mnt/data/cassini-archive-snapshots /var/lib/cassini-archive
chmod 0755 /mnt/data/cassini-archive /mnt/data/cassini-archive-snapshots
chmod 0750 /var/lib/cassini-archive
```

Prove reflink works from a source corpus into the archive before trusting the
plan (a full copy would be ~123.6 GiB instead of ~0):

```bash
src=$(find /mnt/data/cassini/recordings -name '*.mkv' -type f | head -1)
cp --reflink=always "$src" /mnt/data/cassini-archive/.reflink-probe && echo "reflink OK"
rm -f /mnt/data/cassini-archive/.reflink-probe
```

Both endpoints must be on the **same filesystem**: `FICLONE` returns `EXDEV`
across mounts and is unimplemented on tmpfs. `/mnt/data` is `st_dev` 48 and
`/tmp` is a tmpfs at `st_dev` 38, so a probe that reads from `/tmp` can only
ever fail — which is why the script's own probe now clones from a file it writes
inside `$ARCHIVE_ROOT/state/`.

### 2. Install the scripts and config

```bash
install -m 0755 "$REPO/ops/cassini-archive-sync.sh"        /usr/local/sbin/cassini-archive-sync
install -m 0755 "$REPO/ops/cassini-archive-healthcheck.sh" /usr/local/sbin/cassini-archive-healthcheck
install -m 0755 "$REPO/ops/cassini-archive-alert.sh"       /usr/local/sbin/cassini-archive-alert
install -m 0755 "$REPO/ops/98-cassini-archive-motd"        /etc/update-motd.d/98-cassini-archive
install -m 0644 "$REPO/ops/cassini-archive.default"        /etc/default/cassini-archive

# Optional notifier. Only if you have a webhook or a bot token; it may hold a
# secret, hence 0600.
install -m 0600 "$REPO/ops/cassini-archive-alert.default"  /etc/default/cassini-archive-alert
```

### 3. Install the units

```bash
install -m 0644 \
  "$REPO/ops/cassini-archive-sync.service" \
  "$REPO/ops/cassini-archive-sync.timer" \
  "$REPO/ops/cassini-archive-backfill.service" \
  "$REPO/ops/cassini-archive-snapshot.service" \
  "$REPO/ops/cassini-archive-snapshot.timer" \
  "$REPO/ops/cassini-archive-healthcheck.service" \
  "$REPO/ops/cassini-archive-healthcheck.timer" \
  "$REPO/ops/cassini-archive-alert@.service" \
  /etc/systemd/system/

# Optional, only if you want ingest within seconds of a capture being promoted
# instead of at the next timer tick:
install -m 0644 "$REPO/ops/cassini-archive-sync.path" /etc/systemd/system/

systemctl daemon-reload
systemd-analyze verify /etc/systemd/system/cassini-archive-*.{service,timer,path}
```

### 4. Dry run — writes nothing, anywhere

```bash
/usr/local/sbin/cassini-archive-sync              # full plan of every era
/usr/local/sbin/cassini-archive-sync --sync-new   # just the ExApp pass
```

Read the plan. Every mode defaults to a dry run; `--apply` is the only way to
write.

### 5. The one-time backfill

It reads ~123.6 GiB to hash it (1–3 h at `Nice=10` with idle I/O) and is
resumable per meeting, so run it in `tmux`/`screen`, not over a fragile SSH
session. Do **not** enable the timers until it has finished — the cadence is
meaningless before then.

Run it through **`cassini-archive-backfill.service`**, not as a bare command.
Every scheduled pass gets `ProtectSystem=strict` and `ReadOnlyPaths` on both
source corpora; this is the pass with the most irreplaceable data in front of
it, and running it by hand is the only way to forgo them.

```bash
systemctl start --no-block cassini-archive-backfill.service
journalctl -fu cassini-archive-backfill.service          # watch it; Ctrl-C is safe
systemctl show -p Result -p ExecMainStatus cassini-archive-backfill.service
```

`Result=success` / `ExecMainStatus=0` is the only acceptable outcome. If you
really want it in a terminal instead, keep the sandbox:

```bash
tmux new -s cassini-backfill
systemd-run -P --wait --unit=cassini-archive-backfill-adhoc \
  -p ProtectSystem=strict -p ProtectHome=read-only -p TimeoutStartSec=12h \
  -p ReadOnlyPaths=/mnt/data/cassini -p ReadOnlyPaths=/mnt/data/cassini-exapp \
  -p ReadWritePaths=/mnt/data/cassini-archive \
  -p ReadWritePaths=/mnt/data/cassini-archive-snapshots \
  -p ReadWritePaths=/var/lib/cassini-archive \
  /usr/local/sbin/cassini-archive-sync --apply --backfill-old --sync-new
```

**One command, both flags.** Two back-to-back invocations do not work: each pass
only asserts the eras it touched, and (before `--debounce` became opt-in) the
second was silently debounced by `MIN_RUN_INTERVAL` and exited 0 having archived
nothing.

Expected on completion: **196 meeting directories, 203 index rows** (88 cron +
15 legacy + 43 exapp + 50 hpb, plus 7 job-only rows), 0 rows in
`state/conflicts.tsv`, 3 rows in `state/attach-low-confidence.tsv` (the three
`/work/work/<date>-recovered.mkv` imports, attached by date-uniqueness), and
nothing under `unattributed/`. The script asserts the four per-era counts
itself — exactly for cron/legacy/hpb, as a floor for exapp — and every
unattributable bundle raises its own assertion; a different number is a failed
backfill, not a shrug.

### 6. Pin the baseline snapshot, forever

```bash
btrfs subvolume snapshot -r /mnt/data/cassini-archive \
  "/mnt/data/cassini-archive-snapshots/000-baseline--$(date -u +%Y-%m-%dT%H%M%SZ)"
```

The pruner refuses to touch anything named `000-baseline--*`.

### 7. Enable the timers

```bash
systemctl enable --now cassini-archive-sync.timer
systemctl enable --now cassini-archive-snapshot.timer
systemctl enable --now cassini-archive-healthcheck.timer
systemctl enable --now cassini-archive-sync.path     # optional

systemctl list-timers 'cassini-archive-*' --all
```

### 8. Prove the detection works before you need it

```bash
# The healthcheck, on the real archive:
/usr/local/sbin/cassini-archive-healthcheck            # expect: healthy, exit 0
/usr/local/sbin/cassini-archive-healthcheck --json | jq .

# The alarm chain end to end, without breaking anything:
systemctl start cassini-archive-alert@cassini-archive-sync.service
cat /var/lib/cassini-archive/ALARM                     # the durable marker
journalctl -t cassini-archive -n 20 --no-pager         # the daemon.crit record
/usr/local/sbin/cassini-archive-healthcheck            # now FAILS on [A15]
run-parts /etc/update-motd.d/                          # the login banner

# Clear it — deliberately a separate, explicit act:
/usr/local/sbin/cassini-archive-sync --apply --ack
/usr/local/sbin/cassini-archive-healthcheck            # healthy again
```

### 9. Rollback

Nothing here deletes source data, so rollback is only about stopping the
machinery:

```bash
systemctl disable --now cassini-archive-sync.timer cassini-archive-snapshot.timer \
                        cassini-archive-healthcheck.timer cassini-archive-sync.path
rm -f /etc/systemd/system/cassini-archive-*.{service,timer,path}
rm -f /etc/update-motd.d/98-cassini-archive
systemctl daemon-reload
```

The archive itself and its snapshots stay. Removing them is a separate,
deliberate act (`btrfs subvolume delete` each snapshot, then the root) and
should not be done while it is the only copy of the raw corpus.

---

## Residual risk you must not read as solved

Local btrfs snapshots are **not a backup**: `/dev/sda1` dying loses the raw
corpus, the archive, the index and every snapshot in one event. Reflinks make
the archive an independent inode and an independent namespace — which defeats
an accidental delete on the source — but not independent blocks. After this
lands, the raw meeting corpus still has **zero off-host copies**; only the small
`health.json` heartbeat leaves the machine.

## Tests

Offline, no host access, no fixed sleeps:

```bash
harness/bin/test-cassini-archive-units.sh        # unit syntax, schedule, sandbox
harness/bin/test-cassini-archive-healthcheck.sh  # each assertion fires on its own fixture
harness/bin/test-cassini-archive-alert.sh        # durable marker + notifier degradation
```
