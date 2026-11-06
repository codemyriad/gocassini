#!/usr/bin/env bash
# The summary step runs the template the administrator configured (D-895).
#
# Records a real Talk call exactly as ci-e2e-talk-record-roundtrip.sh does
# (its phases 1-7, shared through lib/talk-roundtrip.sh), with two changes made
# through the operator's own ADMIN settings routes before the call starts:
#   - a custom template is saved with PUT operator/settings/workflows/<id>, its
#     instruction carrying a marker sentence unique to the run
#   - a fake OpenAI-compatible endpoint (harness/fake-llm, compose profile
#     fake-llm) is registered with PUT operator/settings/llm, and the summary
#     step is switched on against it with that template
# and then asserts, on the published meeting:
#   (a) the .opus manifest's summary block names the custom template:
#       templateId is its id, and templateSha256 and templateVersion are what
#       GET operator/settings/workflows lists for it
#   (b) the fake endpoint was sent the marker sentence, as the system prompt
#       whose SHA-256 is that listed hash
#   (c) the published summary.md, read back with `cassini meetings context
#       --local`, is the fake's canned reply for that prompt
#
# Phases:
#   1-3  stack, AppAPI, operator container + model activation (shared)
#   3b   start the fake endpoint; prove the operator can list its models
#   3c   save the custom template; point the summary step at it
#   4-7  Talk recording_servers, room + bots, record, published .opus (shared)
#   8-10 assertions (a), (b), (c)
#
# Covers: the template id travelling from the settings the panel writes,
# through the operator's child environment (CASSINI_SUMMARY_TEMPLATE,
# CASSINI_TEMPLATES_DIR, SUMMARY_*), into the build's summary step, onto the
# wire to the configured endpoint, and into the published file's manifest and
# summary attachment.
#
# Does NOT cover: transcript quality, the catalog's room or the absence of a
# Talk upload (the roundtrip owns those, and this script does not repeat
# them); a real model's output; the faithful HaRP install path (this is the
# same manual-install operator container the roundtrip runs); the settings
# panel UI; insights; the fallback to the built-in summarise when a template
# cannot run; summaries written later by a summarize attempt (D-814); the
# Nextcloud Files sink.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO_ROOT="$(cd "$HARNESS_DIR/.." && pwd)"
COMPOSE_FILE="$HARNESS_DIR/compose.yml"
INFO_XML="$REPO_ROOT/appinfo/info.xml"
# shellcheck source=./lib/e2e-local.sh
source "$SCRIPT_DIR/lib/e2e-local.sh"
# shellcheck source=./lib-exapp-manifest.sh
source "$SCRIPT_DIR/lib-exapp-manifest.sh"
harness_e2e_local_stack_env full legacy none

# Its own prefix, so its containers, project and logs are told apart from the
# roundtrip's (the shared stale-run sweep reaps both families: on one runner
# any match is debris), and the fake's profile on every compose call so its
# logs and teardown ride with the rest of the stack.
TALK_RT_PREFIX="${TALK_RT_PREFIX:-cassini-summary-tpl-e2e}"
TALK_RT_LOG_TAG="${TALK_RT_LOG_TAG:-summary-tpl-e2e}"
TALK_RT_COMPOSE_PROFILES="fake-llm"
# shellcheck source=./lib/talk-roundtrip.sh
source "$SCRIPT_DIR/lib/talk-roundtrip.sh"

TEMPLATE_ID="${TEMPLATE_ID:-harness-summary-d895}"
MARKER="Harness marker $(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n'): the summary step ran the configured template."
FAKE_LLM_MODEL="harness/fake-summary"
# The fake writes its request log here; it runs as this user so the log stays
# readable and removable by the runner.
FAKE_LLM_LOG_DIR="$LOG_DIR/fake-llm"
FAKE_LLM_USER="$(id -u):$(id -g)"
export FAKE_LLM_LOG_DIR FAKE_LLM_USER
mkdir -p "$FAKE_LLM_LOG_DIR"
FAKE_LLM_REQUESTS="$FAKE_LLM_LOG_DIR/requests.jsonl"
OPERATOR_URL=""

talk_rt_cleanup_hook() {
  compose logs fake-llm >"$LOG_DIR/fake-llm.log" 2>&1 || true
  if [[ "$1" -ne 0 ]]; then
    log "--- fake-llm requests (method path) ---"
    python3 - "$FAKE_LLM_REQUESTS" <<'PY' 2>/dev/null | sed 's/^/    /' || true
import json, sys
for line in open(sys.argv[1]):
    if line.strip():
        entry = json.loads(line)
        print(entry.get("method"), entry.get("path"))
PY
  fi
}

talk_rt_begin
talk_rt_phase1_stack
talk_rt_phase2_appapi
talk_rt_phase3_exapp

OPERATOR_URL="$NC_URL_HOST/index.php/apps/app_api/proxy/$APP_ID/operator"

# ============================================================================
# Phase 3b: start the fake OpenAI-compatible endpoint
# ============================================================================
phase 3b "Start the fake LLM endpoint"

compose up -d fake-llm >>"$LOG_DIR/fake-llm-up.log" 2>&1 \
  || { tail -n 40 "$LOG_DIR/fake-llm-up.log"; fail "could not start the fake LLM endpoint"; }
FAKE_LLM_ADDR="$(compose port fake-llm 8000 2>/dev/null | tail -n1)"
[[ "$FAKE_LLM_ADDR" == 127.0.0.1:* ]] || fail "fake LLM endpoint has no loopback host port (got: ${FAKE_LLM_ADDR:-<empty>})"
# The operator runs with --network host, so the host's loopback port is the
# address its build child reaches too.
FAKE_LLM_BASE_URL="http://$FAKE_LLM_ADDR/v1"

DEADLINE=$(( SECONDS + 60 ))
until curl -fsS "http://$FAKE_LLM_ADDR/healthz" >/dev/null 2>&1; do
  (( SECONDS < DEADLINE )) || fail "fake LLM endpoint never answered /healthz at $FAKE_LLM_ADDR"
  sleep 1
done
log "OK fake LLM endpoint at $FAKE_LLM_BASE_URL"

# ============================================================================
# Phase 3c: save the custom template; point the summary step at it
# ============================================================================
phase 3c "Save custom template '$TEMPLATE_ID'; summary step on the fake endpoint"

TEMPLATE_BODY="$(jq -nc --arg marker "$MARKER" '{
  name: "Harness summary (D-895)",
  description: "Saved by ci-e2e-summary-template.sh",
  instruction: ("Summarise the meeting transcript you are given as short markdown.\n\n" + $marker)
}')"
operator_request -X PUT -H 'Content-Type: application/json' -d "$TEMPLATE_BODY" \
  "$OPERATOR_URL/settings/workflows/$TEMPLATE_ID" >"$LOG_DIR/template-put.json" \
  || { cat "$LOG_DIR/template-put.json" 2>/dev/null; fail "PUT settings/workflows/$TEMPLATE_ID failed"; }

operator_request "$OPERATOR_URL/settings/workflows" >"$LOG_DIR/workflows.json" \
  || fail "GET settings/workflows failed"
LISTED_SHA256="$(jq -er --arg id "$TEMPLATE_ID" \
  '.[] | select(.id == $id and .origin == "Custom" and ((.error // "") == "")) | .sha256' \
  "$LOG_DIR/workflows.json")" \
  || fail "the workflow listing has no runnable custom template '$TEMPLATE_ID'"
LISTED_VERSION="$(jq -er --arg id "$TEMPLATE_ID" '.[] | select(.id == $id) | .version' "$LOG_DIR/workflows.json")" \
  || fail "the workflow listing has no version for '$TEMPLATE_ID'"
jq -e --arg sha "$LISTED_SHA256" --arg marker "$MARKER" \
  '.sha256 == $sha and (.instruction | contains($marker))' "$LOG_DIR/template-put.json" >/dev/null \
  || fail "the saved template is not the one the listing reports (see $LOG_DIR/template-put.json)"
log "OK custom template $TEMPLATE_ID $LISTED_VERSION sha256=$LISTED_SHA256"

LLM_BODY="$(jq -nc --arg base "$FAKE_LLM_BASE_URL" --arg model "$FAKE_LLM_MODEL" --arg template "$TEMPLATE_ID" '{
  providers: [{id: "fake", name: "Harness fake", base_url: $base, api_key: "", model: $model}],
  summary: {enabled: true, provider: "fake", template: $template}
}')"
operator_request -X PUT -H 'Content-Type: application/json' -d "$LLM_BODY" \
  "$OPERATOR_URL/settings/llm" >"$LOG_DIR/llm-settings.json" \
  || { cat "$LOG_DIR/llm-settings.json" 2>/dev/null; fail "PUT settings/llm failed"; }
jq -e --arg base "$FAKE_LLM_BASE_URL" --arg template "$TEMPLATE_ID" --arg model "$FAKE_LLM_MODEL" \
  '.summary.enabled == true and .summary.template == $template
   and .effective.summary.base_url == $base and .effective.summary.model == $model' \
  "$LOG_DIR/llm-settings.json" >/dev/null \
  || fail "the summary step is not on the fake endpoint with $TEMPLATE_ID (see $LOG_DIR/llm-settings.json)"

# The operator reaching the fake from its own network namespace, before any
# recording depends on it.
operator_request "$OPERATOR_URL/settings/llm/providers/fake/models" >"$LOG_DIR/fake-models.json" \
  || fail "the operator could not list the fake endpoint's models"
jq -e --arg model "$FAKE_LLM_MODEL" '[.models[].id] == [$model]' "$LOG_DIR/fake-models.json" >/dev/null \
  || fail "the operator listed unexpected models from the fake (see $LOG_DIR/fake-models.json)"
log "OK summary step on $FAKE_LLM_BASE_URL with template $TEMPLATE_ID"

talk_rt_phase4_recording_servers
talk_rt_phase5_room_and_bots
talk_rt_phase6_record
talk_rt_phase7_await_publish

# ============================================================================
# Phase 8: (a) the published manifest names the custom template
# ============================================================================
phase 8 "Published manifest's summary block names $TEMPLATE_ID"

# The manifest travels in the .opus's CASSINI_PAYLOAD_* stream tags
# (base64url + gzip JSON, checksummed by CASSINI_PAYLOAD_SHA256), decoded here
# with validate-seeds.py's own reader for a published pack. ffprobe runs in the
# ExApp container, which already needs it for `cassini inspect`.
PROBE_JSON="$LOG_DIR/published-probe.json"
docker exec "$CONTAINER_NAME" ffprobe -v error -show_entries format_tags:stream_tags -of json \
  "$PUBLISHED_OPUS" >"$PROBE_JSON" 2>"$LOG_DIR/published-probe.err" \
  || { cat "$LOG_DIR/published-probe.err"; fail "ffprobe could not read $PUBLISHED_OPUS"; }
MANIFEST_JSON="$LOG_DIR/published-manifest.json"
python3 - "$PROBE_JSON" "$MANIFEST_JSON" "$SCRIPT_DIR/validate-seeds.py" "$PUBLISHED_OPUS" <<'PY' || fail "could not decode the published manifest"
import importlib.util, json, pathlib, sys

probe_path, manifest_path, seeds_path, opus_path = sys.argv[1:5]
sys.dont_write_bytecode = True
# The hyphenated file name rules out a plain import.
spec = importlib.util.spec_from_file_location("validate_seeds", seeds_path)
validate_seeds = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validate_seeds)

tags = validate_seeds.probe_tags(json.load(open(probe_path)))
manifest = validate_seeds.decode_payload(tags, pathlib.PurePosixPath(opus_path).name)
with open(manifest_path, "w", encoding="utf-8") as out:
    json.dump(manifest, out, indent=2, sort_keys=True)
PY

jq -e --arg id "$TEMPLATE_ID" --arg sha "$LISTED_SHA256" --arg version "$LISTED_VERSION" \
  '.summary.templateId == $id and .summary.templateSha256 == $sha and .summary.templateVersion == $version' \
  "$MANIFEST_JSON" >/dev/null || {
  log "published summary block: $(jq -c '.summary // null' "$MANIFEST_JSON")"
  fail "the published manifest does not name $TEMPLATE_ID $LISTED_VERSION sha256=$LISTED_SHA256"
}
log "OK manifest summary: templateId=$TEMPLATE_ID templateVersion=$LISTED_VERSION templateSha256=$LISTED_SHA256"

# ============================================================================
# Phase 9: (b) the fake endpoint was sent the template's prompt
# ============================================================================
phase 9 "Fake endpoint received the marker as the listed system prompt"

[[ -s "$FAKE_LLM_REQUESTS" ]] || fail "the fake LLM endpoint logged no requests"
python3 - "$FAKE_LLM_REQUESTS" "$MARKER" "$LISTED_SHA256" <<'PY' || fail "no chat completion carried the configured template's prompt (log: $FAKE_LLM_REQUESTS)"
import hashlib, json, sys

path, marker, listed = sys.argv[1:4]
completions = []
for line in open(path, encoding="utf-8"):
    if not line.strip():
        continue
    entry = json.loads(line)
    if entry.get("method") == "POST" and entry.get("path", "").endswith("/chat/completions"):
        completions.append(entry.get("body") or {})
print(f"[summary-tpl-e2e] fake endpoint saw {len(completions)} chat completion(s)")
for body in completions:
    systems = [m.get("content", "") for m in body.get("messages", []) if m.get("role") == "system"]
    if not systems or marker not in systems[0]:
        continue
    digest = hashlib.sha256(systems[0].encode("utf-8")).hexdigest()
    if digest != listed:
        sys.exit(f"the marker arrived in a system prompt hashing {digest}, not the listed {listed}")
    sys.exit(0)
sys.exit("the marker sentence is in no chat completion's system prompt")
PY
log "OK the fake endpoint received the marker in the system prompt hashing $LISTED_SHA256"

# ============================================================================
# Phase 10: (c) the published summary.md is the fake's reply
# ============================================================================
phase 10 "Published summary.md is the fake endpoint's canned reply"

# The product's own reader for a published file's summary attachment.
CONTEXT_JSON="$LOG_DIR/published-context.json"
docker exec "$CONTAINER_NAME" cassini meetings context --local --json "$PUBLISHED_OPUS" \
  >"$CONTEXT_JSON" 2>"$LOG_DIR/published-context.err" \
  || { cat "$LOG_DIR/published-context.err"; fail "cassini meetings context --local could not read $PUBLISHED_OPUS"; }
python3 - "$CONTEXT_JSON" "$HARNESS_DIR/fake-llm" "$LISTED_SHA256" <<'PY' || fail "the published summary.md is not the fake endpoint's reply (see $CONTEXT_JSON)"
import json, sys

context_path, fake_dir, listed = sys.argv[1:4]
sys.dont_write_bytecode = True
sys.path.insert(0, fake_dir)
import fake_llm  # noqa: E402  the canned sentence comes from the fake itself

# One meeting prints as a single cassini.meetings.context.v1 document; several
# as {"meetings": [...]}.
document = json.load(open(context_path))
meetings = document["meetings"] if "meetings" in document else [document]
if len(meetings) != 1:
    sys.exit(f"expected one meeting in the context, got {len(meetings)}")
summary = meetings[0].get("summary") or {}
markdown = summary.get("markdown") or ""
if not summary.get("present") or not markdown:
    sys.exit("the published meeting carries no summary.md")
problems = []
if fake_llm.CANNED_SENTENCE not in markdown:
    problems.append("the canned sentence is missing")
if f"System prompt SHA-256: {listed}" not in markdown:
    problems.append(f"the echoed prompt hash is not the listed {listed}")
if problems:
    print("[summary-tpl-e2e] summary.md:\n" + markdown, file=sys.stderr)
    sys.exit("; ".join(problems))
PY
log "OK published summary.md is the fake's reply to the $TEMPLATE_ID prompt"

log "SUMMARY TEMPLATE E2E PASSED — configured template -> endpoint -> published manifest + summary.md"
