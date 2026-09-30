#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export OUTPUT="${OUTPUT:-/tmp/gocassini-ci-resume-$(date -u +%Y%m%dT%H%M%S)-$$.mkv}"
export REC_LOG="${REC_LOG:-/tmp/gocassini-ci-resume-recorder.log}"
export PUB_LOG="${PUB_LOG:-/tmp/gocassini-ci-resume-publisher.log}"
PROXY_PORT="${SIGNALING_PROXY_PORT:-28482}"
PROXY_EVIDENCE="${OUTPUT%.mkv}-resume.json"
PROXY_HOST="${SIGNALING_PROXY_HOST:-$(ip -4 route get 1.1.1.1 | awk '{for(i=1;i<=NF;i++)if($i=="src"){print $(i+1);exit}}')}"
[[ -n "$PROXY_HOST" ]] || { echo "Cannot find a host address reachable from Docker" >&2; exit 1; }
# Publishing the proxy through Docker also works on hosts whose firewall
# permits published container ports but blocks arbitrary host listeners.
PROXY_DATA="$(mktemp -d /tmp/cassini-resume-proxy-XXXXXX)"
PROXY_CONTAINER="${PROJECT_NAME:-gocassini-ci}-resume-proxy-$$"
PROXY_IMAGE="python@sha256:4c47124a8391cb7a9f571164147d154777cf012a4ece5f86097130d7a4478111"
cleanup() {
  docker rm -f "$PROXY_CONTAINER" >/dev/null 2>&1 || true
  if [[ -f "$PROXY_DATA/evidence.json" ]]; then cp "$PROXY_DATA/evidence.json" "$PROXY_EVIDENCE"; fi
  rm -rf "$PROXY_DATA"
}
trap cleanup EXIT INT TERM
docker run --rm -d --name "$PROXY_CONTAINER" -p "$PROXY_PORT:28482" \
  --add-host host.docker.internal:host-gateway \
  -v "$SCRIPT_DIR/interrupt-signaling-once.py:/proxy.py:ro" \
  -v "$PROXY_DATA:/evidence" "$PROXY_IMAGE" python /proxy.py \
  --target-host host.docker.internal --output /evidence/evidence.json \
  --ready-file /evidence/ready >/dev/null
for ((i=0;i<40;i++)); do
  [[ -f "$PROXY_DATA/ready" ]] && break
  [[ "$(docker inspect -f '{{.State.Running}}' "$PROXY_CONTAINER" 2>/dev/null)" == "true" ]] || { echo "Signaling proxy failed" >&2; exit 1; }
  sleep 0.25
done
[[ -f "$PROXY_DATA/ready" ]] || { echo "Signaling proxy not ready" >&2; exit 1; }
export SIGNALING_URL="http://$PROXY_HOST:$PROXY_PORT"
"$SCRIPT_DIR/ci-e2e.sh"
cp "$PROXY_DATA/evidence.json" "$PROXY_EVIDENCE"
python3 - "$PROXY_EVIDENCE" <<'PY'
import json, sys
s = json.load(open(sys.argv[1]))
assert s["dropped"] and s["resumed"], "signaling disconnect/resume was not exercised"
PY
rg -q 'signaling session resumed' "${REC_LOG%.log}-audio-only.log"
echo "PASS: default audio-only capture survives a real signaling session resume"
