#!/usr/bin/env bash
# Validate one installed-ExApp viewer catalog artifact through the product proxy.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VALIDATOR="$SCRIPT_DIR/installed-validation.py"
CATALOG=""
JOB_ID=""
PROXY_URL=""
AUTH=""
OUTPUT_DIR=""
LABEL="artifact"
CURL_BIN="${CASSINI_VALIDATION_CURL_BIN:-curl}"
CASSINI_BIN="${CASSINI_VALIDATION_CASSINI_BIN:-$SCRIPT_DIR/../../bin/cassini}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --catalog) CATALOG="$2"; shift 2 ;;
    --job-id) JOB_ID="$2"; shift 2 ;;
    --proxy-url) PROXY_URL="$2"; shift 2 ;;
    --auth) AUTH="$2"; shift 2 ;;
    --output-dir) OUTPUT_DIR="$2"; shift 2 ;;
    --label) LABEL="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

for value in CATALOG JOB_ID PROXY_URL AUTH OUTPUT_DIR; do
  [[ -n "${!value}" ]] || { echo "missing required --${value,,}" >&2; exit 2; }
done
[[ -f "$CATALOG" ]] || { echo "catalog not found: $CATALOG" >&2; exit 1; }
[[ -x "$VALIDATOR" ]] || { echo "validator not executable: $VALIDATOR" >&2; exit 1; }
mkdir -p "$OUTPUT_DIR"

entry="$($VALIDATOR catalog-entry --catalog "$CATALOG" --job-id "$JOB_ID" --proxy-url "$PROXY_URL")"
kind="$(jq -r '.kind' <<<"$entry")"
artifact_url="$(jq -r '.artifact_url' <<<"$entry")"
segment_count="$(jq -r '.segment_count // 0' <<<"$entry")"

case "$kind" in
  portable)
    artifact="$OUTPUT_DIR/${LABEL}.opus"
    transcript="$OUTPUT_DIR/${LABEL}.transcript.words.v1.json"
    "$CURL_BIN" -fsS -u "$AUTH" "$artifact_url" -o "$artifact"
    [[ -s "$artifact" ]] || { echo "downloaded portable artifact is empty: $artifact_url" >&2; exit 1; }
    "$CASSINI_BIN" inspect --transcript "$artifact" >"$transcript"
    transcript_summary="$($VALIDATOR transcript-summary --kind words --transcript "$transcript")"
    word_count="$(jq -r '.word_count' <<<"$transcript_summary")"
    ;;
  directory)
    transcript="$OUTPUT_DIR/${LABEL}.transcript.display.v1.json"
    "$CURL_BIN" -fsS -u "$AUTH" "$artifact_url" -o "$transcript"
    [[ -s "$transcript" ]] || { echo "downloaded display transcript is empty: $artifact_url" >&2; exit 1; }
    transcript_summary="$($VALIDATOR transcript-summary --kind display --transcript "$transcript")"
    word_count="$(jq -r '.word_count' <<<"$transcript_summary")"
    ;;
  *)
    echo "unsupported catalog artifact kind: $kind" >&2
    exit 1
    ;;
esac

jq -nc \
  --arg job_id "$JOB_ID" \
  --arg kind "$kind" \
  --arg artifact_url "$artifact_url" \
  --arg artifact_file "${artifact:-$transcript}" \
  --argjson segment_count "$segment_count" \
  --argjson word_count "$word_count" \
  '{job_id:$job_id,kind:$kind,artifact_url:$artifact_url,artifact_file:$artifact_file,segment_count:$segment_count,word_count:$word_count}'
