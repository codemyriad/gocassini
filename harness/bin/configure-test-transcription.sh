#!/usr/bin/env bash
# Exercise the administrator's model preparation and explicit activation path.
set -euo pipefail

# shellcheck source=./lib/e2e-local.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/e2e-local.sh"

: "${OPERATOR_URL:?Set the installed AppAPI proxy URL including /operator}"
DEVICE="${DEVICE:-cpu}"
LOG_DIR="${LOG_DIR:-/tmp/cassini-model-setup-$$}"
mkdir -p "$LOG_DIR"
case "$DEVICE" in
  cpu)
    quality=balanced
    candidates='["parakeet-ultra-0.6b-int8","parakeet-tdt-0.6b-v3-int8"]'
    ;;
  cuda)
    quality=best
    candidates='["parakeet-ultra-0.6b","parakeet-tdt-0.6b-v3"]'
    ;;
  *) echo "Unsupported test device: $DEVICE" >&2; exit 1 ;;
esac

# Preserve redirected error bodies on stderr when curl --fail-with-body exits
# non-zero, so 4xx/5xx operator replies surface in CI logs.
fetch_operator_json() {
  local out="$1" rc=0
  shift
  operator_request "$@" > "$out" || rc=$?
  if (( rc != 0 )); then
    [[ ! -s "$out" ]] || { cat "$out" >&2; echo >&2; }
    return "$rc"
  fi
}

fetch_operator_json "$LOG_DIR/settings-before.json" "$OPERATOR_URL/settings"
jq -e '.transcription_enabled == false' "$LOG_DIR/settings-before.json" >/dev/null
fetch_operator_json "$LOG_DIR/models-before.json" "$OPERATOR_URL/settings/models?device=$DEVICE"
jq -e 'all(.models[]; .installed == false)' "$LOG_DIR/models-before.json" >/dev/null

# Pick the newest known tier model advertised by the running operator so the
# upstream Nextcloud canary keeps working when main advances its model catalogue
# ahead of the frozen canary_image release.
if ! model="$(jq -er --argjson candidates "$candidates" '
  ([.models[] | select(.kind == "speech") | .id]) as $available
  | ($candidates | map(select(. as $id | $available | index($id))) | first) // empty
' "$LOG_DIR/models-before.json")"; then
  echo "No expected $DEVICE speech model in operator catalogue; advertised: $(jq -c '[.models[] | select(.kind == "speech") | .id]' "$LOG_DIR/models-before.json")" >&2
  exit 1
fi

payload="$(jq -nc --arg model "$model" --arg device "$DEVICE" '{model:$model,device:$device}')"
fetch_operator_json "$LOG_DIR/model-job.json" -H 'Content-Type: application/json' -d "$payload" \
  "$OPERATOR_URL/settings/models/install"
job="$(jq -er '.id' "$LOG_DIR/model-job.json")"
deadline=$((SECONDS + 900))
while :; do
  fetch_operator_json "$LOG_DIR/model-job.json" "$OPERATOR_URL/settings/models/jobs/$job"
  state="$(jq -er '.state' "$LOG_DIR/model-job.json")"
  jq -c '{state,progress,error}' "$LOG_DIR/model-job.json"
  case "$state" in
    ready) break ;;
    failed|cancelled) echo "Model preparation failed" >&2; exit 1 ;;
  esac
  if (( SECONDS >= deadline )); then echo "Model preparation timed out" >&2; exit 1; fi
  sleep 5
done

# Completing the download must not silently activate transcription.
fetch_operator_json "$LOG_DIR/settings-prepared.json" "$OPERATOR_URL/settings"
jq -e '.transcription_enabled == false' "$LOG_DIR/settings-prepared.json" >/dev/null
revision="$(jq -er '.revision' "$LOG_DIR/model-job.json")"
payload="$(jq -nc --arg model "$model" --arg revision "$revision" \
  --arg device "$DEVICE" --arg quality "$quality" \
  '{transcription_enabled:true,active_model:$model,active_revision:$revision,device_override:$device,quality:$quality}')"
fetch_operator_json "$LOG_DIR/settings-enabled.json" -X PUT -H 'Content-Type: application/json' -d "$payload" \
  "$OPERATOR_URL/settings"
jq -e --arg model "$model" --arg revision "$revision" \
  '.transcription_enabled == true and .active_model == $model and .active_revision == $revision' \
  "$LOG_DIR/settings-enabled.json" >/dev/null
