#!/usr/bin/env bash
#
# Regression test for flake D-052: ci-e2e-mute.sh intermittently failed
# "Expected MKV metadata to expose at least 3 unique participants, got N" (N=1,2).
#
# Root cause (environmental): with the rotator default join_delay=i-1 the three
# publishers join ~0/1/2s apart, so their ICE negotiations hit janus
# near-simultaneously. Under CI CPU load only some reach ICE-connected inside the
# (then 36s) REC_DURATION, and the recorder only captures media once a subscriber
# is ICE-connected and a track arrives. A session that never ICE-connects (or that
# stops publishing before the capture window) contributes zero captured media,
# collapsing the unique-participant / rtplog count below the >=PUB_USERS floor.
#
# The harness mitigation in ci-e2e-mute.sh:
#   - JOIN_DELAYS staggers the joins so the ICE negotiations are serialized.
#   - REC_DURATION/BOT_DURATIONS are sized so every publisher's media window
#     overlaps the recorder's capture window with an ICE/connect budget to spare.
#
# This test pins those two timing invariants so a future edit that re-introduces
# the collision (joins too bunched) or shrinks the capture window (a publisher
# stops before/after the recorder is listening) fails here instead of flaking in
# CI. It is fast and offline: it parses the config, it does not run the stack.
#
# Run directly:
#   ./harness/bin/test-ci-e2e-mute-timing.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MUTE_SH="$SCRIPT_DIR/ci-e2e-mute.sh"
PUBLISHER_SH="$SCRIPT_DIR/../../cassini-go-recorder/e2e_with_publisher.sh"

# ICE/connect budget assumed for the slowest joiner under CI load: the gap between
# a bot joining and it actually streaming captured media. Conservative on purpose
# so the overlap assertion has margin.
ICE_BUDGET="${ICE_BUDGET:-10}"
# Minimum stagger between consecutive joins for the ICE negotiations to be
# meaningfully serialized rather than colliding (the pre-fix default was 1s apart).
MIN_JOIN_STAGGER="${MIN_JOIN_STAGGER:-3}"
# Minimum overlap (seconds) we require between each publisher's media window and
# the recorder's capture window, so a single slow publisher cannot collapse the
# captured participant count.
MIN_OVERLAP="${MIN_OVERLAP:-8}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ -f "$MUTE_SH" ]] || fail "ci-e2e-mute.sh is missing at $MUTE_SH"
[[ -f "$PUBLISHER_SH" ]] || fail "e2e_with_publisher.sh is missing at $PUBLISHER_SH"

# Pull the committed `export VAR="${VAR:-default}"` defaults out of ci-e2e-mute.sh
# without sourcing it (sourcing would trigger the whole Docker stack). Honour an
# already-exported override so callers can probe the pre-fix config (see below).
mute_default() {
  local var="$1"
  if [[ -n "${!var:-}" ]]; then
    printf '%s\n' "${!var}"
    return 0
  fi
  sed -n -E "s/^export ${var}=\"\\\$\{${var}:-([^}]*)\}\".*/\1/p" "$MUTE_SH" | head -n1
}

# START_DELAY is the recorder/publisher offset baked into e2e_with_publisher.sh.
publisher_default() {
  local var="$1"
  sed -n -E "s/^${var}=\"\\\$\{${var}:-([^}]*)\}\".*/\1/p" "$PUBLISHER_SH" | head -n1
}

REC_DURATION="$(mute_default REC_DURATION)"
PUB_DURATION="$(mute_default PUB_DURATION)"
PUB_USERS="$(mute_default PUB_USERS)"
JOIN_DELAYS="$(mute_default JOIN_DELAYS)"
BOT_DURATIONS="$(mute_default BOT_DURATIONS)"
START_DELAY="$(publisher_default START_DELAY)"

[[ -n "$REC_DURATION" ]] || fail "could not read REC_DURATION default from ci-e2e-mute.sh"
[[ -n "$PUB_USERS" ]] || fail "could not read PUB_USERS default from ci-e2e-mute.sh"
[[ -n "$START_DELAY" ]] || fail "could not read START_DELAY default from e2e_with_publisher.sh"

echo "[cfg] REC_DURATION=$REC_DURATION PUB_DURATION=$PUB_DURATION PUB_USERS=$PUB_USERS START_DELAY=$START_DELAY"
echo "[cfg] JOIN_DELAYS='$JOIN_DELAYS' BOT_DURATIONS='$BOT_DURATIONS'"

# Resolve per-bot join delays and durations the same way stream-video.sh /
# the rotator do: explicit CSV value if present for that index, else the
# rotator's i-1 default for join delay and PUB_DURATION for the duration.
IFS=',' read -r -a JOIN_LIST <<<"$JOIN_DELAYS"
IFS=',' read -r -a DUR_LIST <<<"$BOT_DURATIONS"

resolve_join() {
  local i="$1" # 1-based
  local v="${JOIN_LIST[$((i - 1))]:-}"
  v="$(echo "${v:-}" | xargs)"
  if [[ -n "$v" ]]; then
    printf '%s\n' "$v"
  else
    printf '%s\n' "$((i - 1))" # rotator default join_delay=i-1
  fi
}

resolve_duration() {
  local i="$1"
  local v="${DUR_LIST[$((i - 1))]:-}"
  v="$(echo "${v:-}" | xargs)"
  if [[ -n "$v" ]]; then
    printf '%s\n' "$v"
  else
    printf '%s\n' "$PUB_DURATION" # falls back to the global PUB_DURATION
  fi
}

# Invariant 1: consecutive joins are staggered enough to serialize ICE.
prev_join=""
for ((i = 1; i <= PUB_USERS; i++)); do
  j="$(resolve_join "$i")"
  if [[ -n "$prev_join" ]]; then
    gap=$((j - prev_join))
    if (( gap < MIN_JOIN_STAGGER )); then
      fail "publisher $i join delay ${j}s is only ${gap}s after the previous join (< ${MIN_JOIN_STAGGER}s); ICE negotiations will collide (flake D-052)"
    fi
  fi
  prev_join="$j"
done

# Invariant 2: every publisher's media window overlaps the recorder capture window
# [0, REC_DURATION] by at least MIN_OVERLAP seconds, accounting for the ICE budget.
for ((i = 1; i <= PUB_USERS; i++)); do
  j="$(resolve_join "$i")"
  d="$(resolve_duration "$i")"
  media_start=$((START_DELAY + j + ICE_BUDGET))
  media_end=$((START_DELAY + j + d)) # duration timer starts after join, before/around connect
  # Overlap of [media_start, media_end] with [0, REC_DURATION].
  ov_start=$media_start
  ov_end=$media_end
  if (( ov_end > REC_DURATION )); then ov_end=$REC_DURATION; fi
  overlap=$((ov_end - ov_start))
  echo "[bot $i] join=${j}s dur=${d}s media~[${media_start},${media_end}] vs rec[0,${REC_DURATION}] overlap=${overlap}s"
  if (( overlap < MIN_OVERLAP )); then
    fail "publisher $i media window [${media_start},${media_end}] overlaps recorder capture window [0,${REC_DURATION}] by only ${overlap}s (< ${MIN_OVERLAP}s); it may contribute no captured media (flake D-052)"
  fi
done

# Invariant 3: the per-bot publisher-health gate and the funnel summary exist so a
# systematic "always got 1" regression is distinguishable from a sporadic flake.
grep -q 'media stats: ' "$MUTE_SH" \
  || fail "ci-e2e-mute.sh no longer gates on per-bot 'media stats:'"
grep -q 'mute capture summary' "$MUTE_SH" \
  || fail "ci-e2e-mute.sh no longer emits the capture-funnel summary line"

# Invariant 4: the recorder publisher script surfaces *all* ICE transitions, not
# just =connected, so failing/disconnected sessions are visible on a short count.
grep -q 'ICE state=' "$PUBLISHER_SH" \
  || fail "e2e_with_publisher.sh no longer surfaces ICE transitions"
if grep -E 'rg -n "[^"]*ICE state=connected\|' "$PUBLISHER_SH" >/dev/null 2>&1; then
  fail "e2e_with_publisher.sh ICE filter is still narrowed to 'ICE state=connected'"
fi

# Touched scripts still parse.
bash -n "$MUTE_SH" || fail "bash -n failed for ci-e2e-mute.sh"
bash -n "$PUBLISHER_SH" || fail "bash -n failed for e2e_with_publisher.sh"

echo "PASS: ci-e2e-mute.sh staggers ICE and keeps every publisher overlapping the capture window (flake D-052)"
