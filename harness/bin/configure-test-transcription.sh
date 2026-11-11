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
  cpu) model=parakeet-ultra-0.6b-int8-v2; quality=balanced ;;
  cuda) model=parakeet-ultra-0.6b; quality=best ;;
  *) echo "Unsupported test device: $DEVICE" >&2; exit 1 ;;
esac

operator_request "$OPERATOR_URL/settings" > "$LOG_DIR/settings-before.json"
jq -e '.transcription_enabled == false' "$LOG_DIR/settings-before.json" >/dev/null
operator_request "$OPERATOR_URL/settings/models?device=$DEVICE" > "$LOG_DIR/models-before.json"
jq -e 'all(.models[]; .installed == false)' "$LOG_DIR/models-before.json" >/dev/null

payload="$(jq -nc --arg model "$model" --arg device "$DEVICE" '{model:$model,device:$device}')"
operator_request -H 'Content-Type: application/json' -d "$payload" \
  "$OPERATOR_URL/settings/models/install" > "$LOG_DIR/model-job.json"
job="$(jq -er '.id' "$LOG_DIR/model-job.json")"
deadline=$((SECONDS + 900))
while :; do
  operator_request "$OPERATOR_URL/settings/models/jobs/$job" > "$LOG_DIR/model-job.json"
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
operator_request "$OPERATOR_URL/settings" > "$LOG_DIR/settings-prepared.json"
jq -e '.transcription_enabled == false' "$LOG_DIR/settings-prepared.json" >/dev/null
revision="$(jq -er '.revision' "$LOG_DIR/model-job.json")"
payload="$(jq -nc --arg model "$model" --arg revision "$revision" \
  --arg device "$DEVICE" --arg quality "$quality" \
  '{transcription_enabled:true,active_model:$model,active_revision:$revision,device_override:$device,quality:$quality}')"
operator_request -X PUT -H 'Content-Type: application/json' -d "$payload" \
  "$OPERATOR_URL/settings" > "$LOG_DIR/settings-enabled.json"
jq -e --arg model "$model" --arg revision "$revision" \
  '.transcription_enabled == true and .active_model == $model and .active_revision == $revision' \
  "$LOG_DIR/settings-enabled.json" >/dev/null
