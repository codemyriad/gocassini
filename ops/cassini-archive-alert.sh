#!/usr/bin/env bash
#
# cassini-archive-alert — the OnFailure= handler for every Cassini archive unit.
#
# WHY THIS EXISTS
#   george has no mail (postfix is loopback-only, no relayhost), no metrics
#   exporter, no messaging credentials, and until now no OnFailure= anywhere.
#   The observed failure mode on this host is a unit sitting `failed` for three
#   weeks with nobody noticing. So this handler is built only out of things that
#   demonstrably work here, and the optional parts degrade to log-only instead
#   of failing.
#
# WHAT IT DOES, in order of durability
#   1. writes a full-detail report to $VAR_DIR/alarms/<UTC>-<pid>--<unit>.txt
#   2. appends a summary block to the DURABLE marker $VAR_DIR/ALARM.
#      cassini-archive-sync refuses to report green while that file exists and
#      clearing it is an explicit human act (`--apply --ack`), so the next
#      successful run cannot quietly erase the evidence.
#   3. emits a daemon.crit journal record tagged `cassini-archive`
#   4. stamps last_run_status="failed" into state/health.json AND into the
#      off-host heartbeat inside the ExApp volume, so the nightly R2 upload
#      carries the failure within hours rather than the freshness check having
#      to wait 30 h to infer it
#   5. wall(1) broadcast; /etc/update-motd.d/98-cassini-archive reprints the
#      marker on every SSH login
#   6. OPTIONAL notifier, see CONFIG below. Unconfigured => log-only, exit 0.
#
# HOW TO RUN
#   Normally invoked by systemd:  OnFailure=cassini-archive-alert@%n.service
#   By hand, to test the whole chain end to end:
#       sudo /usr/local/sbin/cassini-archive-alert cassini-archive-sync.service
#       sudo systemctl start cassini-archive-alert@some-unit.service
#
# EXIT CODES
#   0  the durable marker was written (notifier failures are NOT fatal)
#   1  the marker could not be written — the one failure worth failing on
#
# CONFIG  /etc/default/cassini-archive        (shared with the other scripts)
#         /etc/default/cassini-archive-alert  (notifier only; may hold secrets,
#                                              so install it 0600 root:root)
#   ALERT_WEBHOOK_URL   POST the alert as JSON here (Slack/Zulip/ntfy/anything)
#   ALERT_COMMAND       shell command receiving the plain-text alert on stdin,
#                       e.g. the Roy telegram bot:
#                         ALERT_COMMAND='curl -sS -F chat_id="$TG_CHAT" -F text=@- \
#                            "https://api.telegram.org/bot$TG_TOKEN/sendMessage"'
#   ALERT_WALL=0        suppress the wall(1) broadcast
#   ALERT_JOURNAL_LINES how much of the failing unit's journal to capture (40)

set -euo pipefail

CONFIG_FILE=${CONFIG_FILE:-/etc/default/cassini-archive}
ALERT_CONFIG_FILE=${ALERT_CONFIG_FILE:-/etc/default/cassini-archive-alert}
# `set -a` so a notifier token from the config file is EXPORTED and therefore
# visible to the `sh -c "$ALERT_COMMAND"` child. Run by hand rather than under
# systemd, a non-exported variable would otherwise expand to nothing and the
# notifier would fail in a way that only shows up during a real incident.
set -a
# shellcheck disable=SC1090
[ -r "$CONFIG_FILE" ] && . "$CONFIG_FILE"
# shellcheck disable=SC1090
[ -r "$ALERT_CONFIG_FILE" ] && . "$ALERT_CONFIG_FILE"
set +a

ARCHIVE_ROOT=${ARCHIVE_ROOT:-/mnt/data/cassini-archive}
VAR_DIR=${VAR_DIR:-/var/lib/cassini-archive}
SRC_EXA_DATA=${SRC_EXA_DATA:-/mnt/data/cassini-exapp/docker/volumes/nc_app_gocassini_data/_data}
HEARTBEAT_DIR=${HEARTBEAT_DIR:-$SRC_EXA_DATA/operator/backups}
HEARTBEAT_FILE=${HEARTBEAT_FILE:-$HEARTBEAT_DIR/cassini-archive-health.json}
HEALTH_JSON=${HEALTH_JSON:-$ARCHIVE_ROOT/state/health.json}

ALARM_FILE="$VAR_DIR/ALARM"
ALARM_DIR="$VAR_DIR/alarms"
ALERT_JOURNAL_LINES=${ALERT_JOURNAL_LINES:-40}
ALERT_MAX_MARKER_BYTES=${ALERT_MAX_MARKER_BYTES:-262144}
ALERT_KEEP_REPORTS=${ALERT_KEEP_REPORTS:-50}
ALERT_WALL=${ALERT_WALL:-1}
ALERT_WEBHOOK_URL=${ALERT_WEBHOOK_URL:-}
ALERT_COMMAND=${ALERT_COMMAND:-}
ALERT_HOSTNAME=${ALERT_HOSTNAME:-$(hostname -s 2>/dev/null || echo unknown)}

UNIT=${1:-unknown.service}
NOW=$(date -u +%FT%TZ)
STAMP=$(date -u +%Y%m%dT%H%M%SZ)

# ---------------------------------------------------------------------------
# 0. gather what systemd knows about the failure
# ---------------------------------------------------------------------------
prop() { # prop <Name> -> value, or "" when systemctl is unavailable
  systemctl show "$UNIT" -p "$1" --value 2>/dev/null || printf ''
}

RESULT=$(prop Result)
EXEC_STATUS=$(prop ExecMainStatus)
EXEC_CODE=$(prop ExecMainCode)
INVOCATION=$(prop InvocationID)
ACTIVE_ENTER=$(prop ActiveEnterTimestamp)
INACTIVE_ENTER=$(prop InactiveEnterTimestamp)
DESCRIPTION=$(prop Description)

JOURNAL=""
if command -v journalctl >/dev/null 2>&1; then
  if [ -n "$INVOCATION" ]; then
    # Scoped to exactly the invocation that failed, not the last N lines of the
    # unit's whole history.
    JOURNAL=$(journalctl "_SYSTEMD_INVOCATION_ID=$INVOCATION" --no-pager -o cat \
              -n "$ALERT_JOURNAL_LINES" 2>/dev/null || printf '')
  fi
  if [ -z "$JOURNAL" ]; then
    JOURNAL=$(journalctl -u "$UNIT" --no-pager -o cat -n "$ALERT_JOURNAL_LINES" 2>/dev/null || printf '')
  fi
fi
[ -n "$JOURNAL" ] || JOURNAL="(no journal output captured)"

SUMMARY="cassini-archive FAILURE on $ALERT_HOSTNAME: $UNIT result=${RESULT:-unknown} exit_status=${EXEC_STATUS:-?} at $NOW"

REPORT=$(cat <<EOF
=== cassini-archive alert =====================================================
when            $NOW
host            $ALERT_HOSTNAME
unit            $UNIT
description     ${DESCRIPTION:-(unknown)}
result          ${RESULT:-(unknown)}
exit            code=${EXEC_CODE:-?} status=${EXEC_STATUS:-?}
invocation      ${INVOCATION:-(unknown)}
active since    ${ACTIVE_ENTER:-(unknown)}
inactive since  ${INACTIVE_ENTER:-(unknown)}

Exit-code meanings for cassini-archive-sync:
  2 usage / not root   3 preflight failed   4 operator DB unreadable
  5 conflicts or failed assertions          6 upstream quiet (no new capture)
  7 unacknowledged ALARM
cassini-archive-healthcheck: 2 usage/environment, 3 assertions failed.

--- journal (last $ALERT_JOURNAL_LINES lines of the failing invocation) --------
$JOURNAL
--- what to do ----------------------------------------------------------------
  systemctl status $UNIT
  journalctl -u $UNIT -n 200 --no-pager
  sudo /usr/local/sbin/cassini-archive-healthcheck        # what is actually wrong
  sudo /usr/local/sbin/cassini-archive-sync --apply --ack # clears this marker
===============================================================================
EOF
)

# ---------------------------------------------------------------------------
# 1+2. the durable half. This is the only part worth failing on.
# ---------------------------------------------------------------------------
mkdir -p "$ALARM_DIR"
safe_unit=${UNIT//\//_}
# The pid keeps two failures inside the same second from overwriting each
# other's report — which is exactly what a unit failing and immediately
# retrying looks like.
printf '%s\n' "$REPORT" >"$ALARM_DIR/$STAMP-$$--$safe_unit.txt"

# Keep the marker bounded: alarms latch until a human acks them, and a unit
# failing twice a day for a month must not fill /var.
if [ -f "$ALARM_FILE" ]; then
  sz=$(stat -c %s "$ALARM_FILE" 2>/dev/null || echo 0)
  if [ "$sz" -gt "$ALERT_MAX_MARKER_BYTES" ]; then
    mv -f "$ALARM_FILE" "$ALARM_FILE.1"
  fi
fi
printf '%s\n' "$REPORT" >>"$ALARM_FILE"

# Prune old detail reports, newest first, never the one just written.
if [ -d "$ALARM_DIR" ]; then
  # shellcheck disable=SC2012  # names are our own timestamps: sortable, no spaces
  ls -1 "$ALARM_DIR" 2>/dev/null | LC_ALL=C sort -r | tail -n +"$((ALERT_KEEP_REPORTS + 1))" \
    | while IFS= read -r old; do rm -f -- "$ALARM_DIR/$old"; done
fi

# From here on nothing may fail the unit: the evidence is already on disk, and a
# broken notifier must never look like a broken archive.
set +e

# ---------------------------------------------------------------------------
# 3. loud journal record
# ---------------------------------------------------------------------------
if command -v logger >/dev/null 2>&1; then
  printf '%s\n' "$SUMMARY" | logger -p daemon.crit -t cassini-archive
  printf '%s\n' "$REPORT" | logger -p daemon.crit -t cassini-archive
else
  printf '%s\n' "$SUMMARY" >&2
fi

# ---------------------------------------------------------------------------
# 4. stamp the failure into health.json and the off-host heartbeat
#    (the R2 copy then carries `failed` after the next 03:30 backup)
# ---------------------------------------------------------------------------
if command -v jq >/dev/null 2>&1 && [ -f "$HEALTH_JSON" ]; then
  tmp="$HEALTH_JSON.alert.$$"
  if jq --arg u "$UNIT" --arg at "$NOW" --arg s "$SUMMARY" \
       '.last_run_status="failed" | .alarm_unit=$u | .alarm_utc=$at | .alarm_summary=$s' \
       "$HEALTH_JSON" >"$tmp" 2>/dev/null; then
    mv -f "$tmp" "$HEALTH_JSON"
    [ -d "$HEARTBEAT_DIR" ] && cp -f "$HEALTH_JSON" "$HEARTBEAT_FILE"
  else
    rm -f "$tmp"
  fi
fi

# ---------------------------------------------------------------------------
# 5. wall — the channel that reaches whoever is already logged in
# ---------------------------------------------------------------------------
if [ "$ALERT_WALL" = 1 ] && command -v wall >/dev/null 2>&1; then
  printf '%s\n%s\n' "$SUMMARY" "See $ALARM_FILE — clear with: cassini-archive-sync --apply --ack" \
    | wall -n 2>/dev/null || printf '%s\n' "$SUMMARY" | wall 2>/dev/null
fi

# ---------------------------------------------------------------------------
# 6. optional notifier — degrades to log-only, never fails the unit
# ---------------------------------------------------------------------------
notified=0
if [ -n "$ALERT_WEBHOOK_URL" ] && command -v curl >/dev/null 2>&1; then
  payload=$(jq -n --arg text "$SUMMARY" --arg host "$ALERT_HOSTNAME" --arg unit "$UNIT" \
              --arg at "$NOW" --arg detail "$REPORT" \
              '{text:$text, host:$host, unit:$unit, at:$at, detail:$detail}' 2>/dev/null \
            || printf '{"text":"%s"}' "$SUMMARY")
  if printf '%s' "$payload" | curl -sS --max-time 15 -X POST \
       -H 'Content-Type: application/json' --data-binary @- "$ALERT_WEBHOOK_URL" >/dev/null 2>&1; then
    notified=1
    logger -p daemon.notice -t cassini-archive "alert webhook delivered for $UNIT" 2>/dev/null
  else
    logger -p daemon.warning -t cassini-archive "alert webhook FAILED for $UNIT (marker still written)" 2>/dev/null
  fi
fi

if [ -n "$ALERT_COMMAND" ]; then
  if printf '%s\n' "$REPORT" | sh -c "$ALERT_COMMAND" >/dev/null 2>&1; then
    notified=1
    logger -p daemon.notice -t cassini-archive "alert command delivered for $UNIT" 2>/dev/null
  else
    logger -p daemon.warning -t cassini-archive "alert command FAILED for $UNIT (marker still written)" 2>/dev/null
  fi
fi

if [ -z "$ALERT_WEBHOOK_URL" ] && [ -z "$ALERT_COMMAND" ]; then
  logger -p daemon.notice -t cassini-archive \
    "notifier=unconfigured: alert for $UNIT recorded in $ALARM_FILE, journal, wall and motd only" 2>/dev/null
fi

printf '%s\n' "$SUMMARY"
printf 'marker=%s reports=%s notified=%s\n' "$ALARM_FILE" "$ALARM_DIR" "$notified"
exit 0
