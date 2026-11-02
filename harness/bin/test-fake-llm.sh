#!/usr/bin/env bash
# Offline test for the harness fake LLM endpoint (harness/fake-llm/fake_llm.py,
# D-895): no Docker, no network beyond loopback.
#
# Covers: the server binds a free loopback port it picks itself; GET /v1/models
# lists one model; POST /v1/chat/completions answers an OpenAI-shaped
# completion whose markdown carries the canned sentence and the SHA-256 of the
# system message it was sent; every request lands in the JSON-lines log with
# its parsed body, so a scenario can find the prompt it sent.
#
# Does NOT cover: the compose service around it (image, mounts, ephemeral host
# port) or the recorder actually calling it — ci-e2e-summary-template.sh owns
# that, in CI.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FAKE="$SCRIPT_DIR/../fake-llm/fake_llm.py"

work="$(mktemp -d)"
server_pid=""
cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" >/dev/null 2>&1 || true
    wait "$server_pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

fail() { printf '[test-fake-llm] FAIL: %s\n' "$*" >&2; exit 1; }

python3 "$FAKE" --host 127.0.0.1 --port 0 \
  --log "$work/requests.jsonl" --port-file "$work/port" 2>"$work/server.log" &
server_pid=$!

for _ in $(seq 1 50); do
  [[ -s "$work/port" ]] && break
  kill -0 "$server_pid" 2>/dev/null || { cat "$work/server.log" >&2; fail "server exited before listening"; }
  sleep 0.1
done
[[ -s "$work/port" ]] || fail "server never wrote its port"
port="$(tr -d '[:space:]' <"$work/port")"
base="http://127.0.0.1:$port/v1"

curl -fsS "$base/models" >"$work/models.json" || fail "GET /v1/models failed"

marker="Marker sentence for the offline fake LLM test."
system="You summarise meetings. $marker"
jq -nc --arg system "$system" '{
  model: "harness/fake-summary",
  messages: [{role: "system", content: $system}, {role: "user", content: "S1: hello"}],
  temperature: 0, max_tokens: 64
}' >"$work/request.json"
curl -fsS -H 'Content-Type: application/json' --data-binary "@$work/request.json" \
  "$base/chat/completions" >"$work/reply.json" || fail "POST /v1/chat/completions failed"

# The request log is appended before the reply is written, so it is complete
# by the time curl returns.
WORK="$work" SYSTEM="$system" MARKER="$marker" python3 - <<'PY' || fail "reply or log is not what the fake promises"
import hashlib
import json
import os
import sys

work = os.environ["WORK"]
system = os.environ["SYSTEM"]
marker = os.environ["MARKER"]
problems = []

models = json.load(open(os.path.join(work, "models.json")))
ids = [m.get("id") for m in models.get("data", [])]
if ids != ["harness/fake-summary"]:
    problems.append(f"models lists {ids!r}, want exactly the one fake model")

reply = json.load(open(os.path.join(work, "reply.json")))
choice = (reply.get("choices") or [{}])[0]
content = (choice.get("message") or {}).get("content", "")
digest = hashlib.sha256(system.encode()).hexdigest()
if choice.get("finish_reason") != "stop":
    problems.append(f"finish_reason {choice.get('finish_reason')!r}, want 'stop'")
if "This summary was written by the harness fake LLM endpoint." not in content:
    problems.append("the reply does not carry the canned sentence")
if f"System prompt SHA-256: {digest}" not in content:
    problems.append("the reply does not echo the system prompt's SHA-256")

lines = [json.loads(l) for l in open(os.path.join(work, "requests.jsonl")) if l.strip()]
posts = [l for l in lines if l["method"] == "POST" and l["path"] == "/v1/chat/completions"]
gets = [l for l in lines if l["method"] == "GET" and l["path"] == "/v1/models"]
if len(posts) != 1 or len(gets) != 1:
    problems.append(f"log holds {len(gets)} model and {len(posts)} completion requests, want one each")
elif not any(marker in (m.get("content") or "") for m in posts[0]["body"].get("messages", [])):
    problems.append("the logged completion body does not carry the marker sentence")

for p in problems:
    print(f"[test-fake-llm] {p}", file=sys.stderr)
sys.exit(1 if problems else 0)
PY

# An unknown route is a 404, not a canned success.
status="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$base/embeddings" -d '{}')"
[[ "$status" == "404" ]] || fail "POST /v1/embeddings answered $status, want 404"

printf '[test-fake-llm] OK\n'
