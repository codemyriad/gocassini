#!/usr/bin/env bash
#
# Regression test for the Cassini archive systemd units.
#
# What it pins, and why each one is a real bug if it regresses:
#   * every unit file is syntactically valid (systemd-analyze verify)
#   * no timer window overlaps the 03:30-03:50 cassini-exapp-backup window, the
#     monthly `0 2 1 * * btrfs scrub -B /mnt/data`, or the 03:10 e2scrub/xfs_scrub
#     — a heavy archive pass on top of the nightly restic upload is how you get
#     an unrelated backup failure and learn to ignore backup failures
#   * no two archive timer windows overlap each other, so the outer flock is a
#     safety net rather than the normal path
#   * every work unit declares OnFailure=cassini-archive-alert@%n.service —
#     without it a failure is silent, which is the entire premise of this project
#   * every work unit declares ReadOnlyPaths on both source corpora, so a bug
#     that tries to write a source tree gets EROFS instead of deleting a meeting
#   * the alert template declares no OnFailure= of its own (no alert loop)
#   * the timers are Persistent=true, so a rebooted host catches up
#   * the shipped shell scripts parse
#
# Fully offline: no ssh, no docker, no systemd required (verify is skipped when
# systemd-analyze is unavailable). Run directly:
#   ./harness/bin/test-cassini-archive-units.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
OPS="$PROJECT_ROOT/ops"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# The backfill unit is a WORK unit too: it is the one pass that walks the whole
# irreplaceable corpus, and it used to be run by hand outside every sandbox.
WORK_UNITS=(cassini-archive-sync.service cassini-archive-snapshot.service cassini-archive-healthcheck.service cassini-archive-backfill.service)
TIMERS=(cassini-archive-sync.timer cassini-archive-snapshot.timer cassini-archive-healthcheck.timer)
ALL_UNITS=("${WORK_UNITS[@]}" "${TIMERS[@]}" cassini-archive-alert@.service cassini-archive-sync.path)

# 1. Every unit file ships.
for u in "${ALL_UNITS[@]}"; do
  [[ -f "$OPS/$u" ]] || fail "missing unit file ops/$u"
done

# 2. Systemd itself accepts them. The only tolerated diagnostics are the
#    "Command ... is not executable" lines for binaries that are not installed
#    on this workstation — that is what INSTALL is for.
if command -v systemd-analyze >/dev/null 2>&1; then
  TMP_UNITS="$(mktemp -d)"
  trap 'rm -rf "$TMP_UNITS"' EXIT
  for u in "${ALL_UNITS[@]}"; do cp "$OPS/$u" "$TMP_UNITS/"; done
  verify_out="$(systemd-analyze verify --no-pager "$TMP_UNITS"/* 2>&1 || true)"
  unexpected="$(printf '%s\n' "$verify_out" | grep -v 'is not executable: No such file or directory' | grep -v '^$' || true)"
  [[ -z "$unexpected" ]] || fail "systemd-analyze verify complained:"$'\n'"$unexpected"
else
  echo "note: systemd-analyze unavailable, skipping unit verification"
fi

# 3. Service-level invariants.
for u in "${WORK_UNITS[@]}"; do
  f="$OPS/$u"
  grep -qx 'Type=oneshot' "$f" || fail "$u: not Type=oneshot"
  grep -qx 'OnFailure=cassini-archive-alert@%n.service' "$f" \
    || fail "$u: no OnFailure= handler — a failure here would be silent, which is the bug this project exists to prevent"
  grep -qx 'EnvironmentFile=-/etc/default/cassini-archive' "$f" \
    || fail "$u: missing the tolerant EnvironmentFile for /etc/default/cassini-archive"
  grep -q '^TimeoutStartSec=' "$f" || fail "$u: no TimeoutStartSec"
  grep -qx 'NoNewPrivileges=yes' "$f" || fail "$u: NoNewPrivileges is not set"
  grep -qx 'ProtectSystem=strict' "$f" || fail "$u: ProtectSystem=strict is not set"
  # The single most valuable line in the design: the sources are mounted
  # read-only into the unit's namespace.
  grep -qx 'ReadOnlyPaths=/mnt/data/cassini /mnt/data/cassini-exapp' "$f" \
    || grep -q '^ReadOnlyPaths=.*/mnt/data/cassini .*/mnt/data/cassini-exapp' "$f" \
    || fail "$u: does not declare ReadOnlyPaths on both source corpora"
done

# The two heavy units must stay polite about I/O; the host also runs Talk.
for u in cassini-archive-sync.service cassini-archive-snapshot.service; do
  grep -qx 'Nice=10' "$OPS/$u" || fail "$u: Nice=10 missing"
  grep -qx 'IOSchedulingClass=idle' "$OPS/$u" || fail "$u: IOSchedulingClass=idle missing"
  grep -q 'flock -w .* /run/lock/cassini-archive-unit.lock' "$OPS/$u" \
    || fail "$u: no outer flock — the script's own flock -n would make the loser exit 0 having done nothing"
done

# Only the sync unit may ingest; the snapshot unit must never write meetings.
grep -q -- '--sync-new' "$OPS/cassini-archive-sync.service" || fail "sync.service does not run --sync-new"
grep -q -- '--snapshot' "$OPS/cassini-archive-snapshot.service" || fail "snapshot.service does not run --snapshot"
grep -q -- '--sync-new' "$OPS/cassini-archive-snapshot.service" \
  && fail "snapshot.service must not ingest; that belongs to cassini-archive-sync.service"

# The healthcheck writes nothing, so it must be granted nothing.
grep -q '^ReadWritePaths=' "$OPS/cassini-archive-healthcheck.service" \
  && fail "healthcheck.service grants write access it does not need"

# 4. The alert handler must not alert about itself.
grep -q '^OnFailure=' "$OPS/cassini-archive-alert@.service" \
  && fail "cassini-archive-alert@.service declares OnFailure= — that is an alert loop"
grep -q '^ExecStart=.*cassini-archive-alert %i' "$OPS/cassini-archive-alert@.service" \
  || fail "cassini-archive-alert@.service does not pass the failing unit name (%i) to the handler"
grep -qx 'EnvironmentFile=-/etc/default/cassini-archive-alert' "$OPS/cassini-archive-alert@.service" \
  || fail "cassini-archive-alert@.service does not read the optional notifier config"

# 5. Timer invariants + the schedule.
for t in "${TIMERS[@]}"; do
  grep -qx 'Persistent=true' "$OPS/$t" || fail "$t: Persistent=true missing (a rebooted host would never catch up)"
  grep -q '^OnCalendar=' "$OPS/$t" || fail "$t: no OnCalendar"
  grep -q '^Unit=' "$OPS/$t" || fail "$t: no explicit Unit="
  grep -qx 'WantedBy=timers.target' "$OPS/$t" || fail "$t: not installable into timers.target"
done

# Collect every (start_minute, end_minute) window a timer can fire in.
windows=()   # "name start end"
for t in "${TIMERS[@]}"; do
  jitter_spec="$(sed -n 's/^RandomizedDelaySec=\(.*\)$/\1/p' "$OPS/$t" | head -1)"
  case "$jitter_spec" in
    *m) jitter=${jitter_spec%m} ;;
    *s) jitter=$(( ${jitter_spec%s} / 60 )) ;;
    "") jitter=0 ;;
    *) jitter=$jitter_spec ;;
  esac
  while read -r hh mm; do
    start=$((10#$hh * 60 + 10#$mm))
    windows+=("$t $start $((start + jitter))")
  done < <(sed -n 's/^OnCalendar=\*-\*-\* \([0-9][0-9]\):\([0-9][0-9]\):[0-9][0-9]$/\1 \2/p' "$OPS/$t")
done
[[ ${#windows[@]} -ge 6 ]] || fail "expected at least 6 scheduled windows, parsed ${#windows[@]}"

# Forbidden bands, minutes of day. The guard band around the backup is wider
# than 03:30-03:50 on purpose: restic runs for a while after it starts.
forbidden=("btrfs-scrub-and-fs-scrubs 110 240" "cassini-exapp-backup 200 240")
for w in "${windows[@]}"; do
  read -r name ws we <<<"$w"
  for f in "${forbidden[@]}"; do
    read -r fname fs fe <<<"$f"
    if [[ $ws -lt $fe && $we -gt $fs ]]; then
      fail "$name fires at $((ws / 60)):$(printf '%02d' $((ws % 60)))-$((we / 60)):$(printf '%02d' $((we % 60))), which collides with $fname"
    fi
  done
done

# No two archive windows may overlap each other either.
for ((i = 0; i < ${#windows[@]}; i++)); do
  for ((j = i + 1; j < ${#windows[@]}; j++)); do
    read -r n1 s1 e1 <<<"${windows[$i]}"
    read -r n2 s2 e2 <<<"${windows[$j]}"
    if [[ $s1 -lt $e2 && $e1 -gt $s2 ]]; then
      fail "overlapping archive windows: $n1 [$s1,$e1] and $n2 [$s2,$e2]"
    fi
  done
done

# 6. The shipped scripts parse, and the units point at what INSTALL installs.
for s in cassini-archive-sync.sh cassini-archive-healthcheck.sh cassini-archive-alert.sh 98-cassini-archive-motd; do
  [[ -f "$OPS/$s" ]] || fail "missing ops/$s"
  [[ -x "$OPS/$s" ]] || fail "ops/$s is not executable"
  bash -n "$OPS/$s" || fail "ops/$s does not parse"
done

README="$OPS/README.md"
[[ -f "$README" ]] || fail "ops/README.md is missing — the operator has no install instructions"
grep -q 'btrfs subvolume create /mnt/data/cassini-archive' "$README" \
  || fail "README does not document creating the archive subvolume, which the script refuses to do itself"
grep -q 'chown root:root /mnt/data/cassini-archive' "$README" \
  || fail "README does not document the ownership that keeps unprivileged CT 112 out of the archive"
for pair in \
  "cassini-archive-sync.sh:/usr/local/sbin/cassini-archive-sync" \
  "cassini-archive-healthcheck.sh:/usr/local/sbin/cassini-archive-healthcheck" \
  "cassini-archive-alert.sh:/usr/local/sbin/cassini-archive-alert"; do
  src=${pair%%:*}; dst=${pair##*:}
  grep -q "install -m 0755 .*$src.*$dst" "$README" \
    || fail "README does not install $src as $dst, which is the path the units call"
done
for u in "${ALL_UNITS[@]}"; do
  grep -qF "$u" "$README" || fail "README never mentions $u"
done

echo "PASS: cassini archive units — syntax, schedule, sandbox, alert wiring and install docs all consistent"
