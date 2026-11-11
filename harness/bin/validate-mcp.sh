#!/usr/bin/env bash
# Pin the MCP server (D-813) through the real AppAPI proxy.
#
# Runs against an ALREADY INSTALLED Cassini ExApp, the way
# validate-annotations-route.sh does. It starts nothing and is not wired into
# CI. It creates one app password per user it signs in as, and deletes them on
# exit.
#
# What it covers:
#   1. An app password works as a bearer credential through the proxy, and over
#      Basic, which is what the CLI sends. Without a credential the request
#      never reaches Cassini.
#   2. The server is stateless JSON: a POST is answered with one JSON body, and
#      GET is refused.
#   3. tools/list offers exactly the read tools, each marked read-only.
#   4. Each user's list is their own. With --expect-different the two users'
#      lists must differ (give them different shares first).
#   5. search answers with references and never transcript text (--query).
#   6. context reads the newest meeting USER_A can read, whole and windowed,
#      and reports the size of the whole document — the large-response check
#      through the proxy.
#
# Usage:
#   harness/bin/validate-mcp.sh [--query WORDS] [--expect-different]
#
# Environment: USER_A / USER_A_PASSWORD (default alice, the harness viewer
# user), USER_B / USER_B_PASSWORD (default admin / admin),
# CASSINI_HARNESS_HOST (default 127.0.0.1), NEXTCLOUD_CONTAINER.

set -euo pipefail

NEXTCLOUD_HOST="${CASSINI_HARNESS_HOST:-127.0.0.1}"
NEXTCLOUD_URL="${NEXTCLOUD_URL:-http://${NEXTCLOUD_HOST}:28080}"
NEXTCLOUD_CONTAINER="${NEXTCLOUD_CONTAINER:-spreedtest-nextcloud-1}"
ENDPOINT="${NEXTCLOUD_URL}/index.php/apps/app_api/proxy/gocassini/mcp"
USER_A="${USER_A:-alice}"
USER_A_PASSWORD="${USER_A_PASSWORD:-Tn8mY3qVrJ2x!E2e}"
USER_B="${USER_B:-admin}"
USER_B_PASSWORD="${USER_B_PASSWORD:-admin}"
QUERY=""
EXPECT_DIFFERENT=0

log() { printf '\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n' "$*" >&2; }
success() { printf '\033[1;32m%s\033[0m\n' "$*" >&2; }
fail() { printf '\033[1;31m[ERROR] %s\033[0m\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --query) QUERY="$2"; shift 2 ;;
    --expect-different) EXPECT_DIFFERENT=1; shift ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

command -v jq >/dev/null || fail "jq is required"

# app_password USER PASSWORD — a fresh app password, created the way a person
# would get one (Nextcloud mints it from the login password).
TOKENS=()
app_password() {
  local user="$1" password="$2" token
  token="$(docker exec -e NC_PASS="$password" -u www-data "$NEXTCLOUD_CONTAINER" \
    php occ user:auth-tokens:add --password-from-env --name "cassini-mcp-validate" "$user" 2>/dev/null \
    | grep -oE '[A-Za-z0-9]{72}' | head -1)"
  [ -n "$token" ] || fail "could not create an app password for $user"
  TOKENS+=("$user")
  printf '%s' "$token"
}
cleanup() {
  for user in "${TOKENS[@]}"; do
    docker exec -u www-data "$NEXTCLOUD_CONTAINER" php occ user:auth-tokens:list --output=json "$user" 2>/dev/null \
      | jq -r '.[] | select(.name == "cassini-mcp-validate") | .id' \
      | while read -r id; do
          docker exec -u www-data "$NEXTCLOUD_CONTAINER" php occ user:auth-tokens:delete "$user" "$id" >/dev/null 2>&1 || true
        done
  done
}
trap cleanup EXIT

# rpc AUTH_ARGS... -- METHOD PARAMS_JSON — one JSON-RPC call; prints the body.
rpc() {
  local args=() method params
  while [ "$1" != "--" ]; do args+=("$1"); shift; done
  shift
  method="$1" params="$2"
  curl -sS -m 300 -X POST "$ENDPOINT" "${args[@]}" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H 'MCP-Protocol-Version: 2025-06-18' \
    --data "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$method\",\"params\":$params}"
}
call() { # call AUTH_ARGS... -- TOOL ARGS_JSON — prints the tool result
  local args=()
  while [ "$1" != "--" ]; do args+=("$1"); shift; done
  shift
  rpc "${args[@]}" -- tools/call "{\"name\":\"$1\",\"arguments\":$2}" | jq -c '.result'
}

log "Creating app passwords for $USER_A and $USER_B"
TOKEN_A="$(app_password "$USER_A" "$USER_A_PASSWORD")"
TOKEN_B="$(app_password "$USER_B" "$USER_B_PASSWORD")"
BEARER_A=(-H "Authorization: Bearer $TOKEN_A")
BEARER_B=(-H "Authorization: Bearer $TOKEN_B")

log "1. Credentials through the proxy"
status="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$ENDPOINT" -H 'Content-Type: application/json' --data '{}')"
[ "$status" != "200" ] || fail "an anonymous POST was answered 200"
success "no credential: $status, never reaches Cassini"
rpc "${BEARER_A[@]}" -- tools/list '{}' | jq -e '.result.tools | length > 0' >/dev/null || fail "bearer app password refused"
success "bearer app password: accepted"
rpc -u "$USER_A:$TOKEN_A" -- tools/list '{}' | jq -e '.result.tools | length > 0' >/dev/null || fail "basic app password refused"
success "basic app password: accepted"

log "2. Stateless JSON"
ctype="$(curl -s -o /dev/null -w '%{content_type}' -X POST "$ENDPOINT" "${BEARER_A[@]}" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -H 'MCP-Protocol-Version: 2025-06-18' \
  --data '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}')"
case "$ctype" in application/json*) ;; *) fail "reply content type is $ctype, want application/json" ;; esac
get="$(curl -s -o /dev/null -w '%{http_code}' "$ENDPOINT" "${BEARER_A[@]}")"
[ "$get" != "200" ] || fail "GET was answered 200"
success "POST -> $ctype; GET -> $get"

log "3. The tools"
tools="$(rpc "${BEARER_A[@]}" -- tools/list '{}')"
names="$(jq -r '[.result.tools[].name] | sort | join(",")' <<<"$tools")"
[ "$names" = "annotations,context,list,rooms,search,tags" ] || fail "tools are $names"
jq -e '[.result.tools[] | .annotations.readOnlyHint == true] | all' <<<"$tools" >/dev/null || fail "a tool is not marked read-only"
success "$names, all read-only"

log "4. Each caller's own list"
list_a="$(call "${BEARER_A[@]}" -- list '{}')"
list_b="$(call "${BEARER_B[@]}" -- list '{}')"
jq -e '.isError != true' <<<"$list_a" >/dev/null || fail "list failed for $USER_A: $list_a"
jq -e '.isError != true' <<<"$list_b" >/dev/null || fail "list failed for $USER_B: $list_b"
ids_a="$(jq -r '[.structuredContent.meetings[] | "\(.id) \(.dateLabel) \(.title)"] | join("\n")' <<<"$list_a")"
ids_b="$(jq -r '[.structuredContent.meetings[] | "\(.id) \(.dateLabel) \(.title)"] | join("\n")' <<<"$list_b")"
printf '%s sees:\n%s\n%s sees:\n%s\n' "$USER_A" "${ids_a:-  (nothing)}" "$USER_B" "${ids_b:-  (nothing)}" >&2
if [ "$EXPECT_DIFFERENT" = 1 ]; then
  [ "$ids_a" != "$ids_b" ] || fail "$USER_A and $USER_B see the same list"
  success "the two lists differ"
fi

if [ -n "$QUERY" ]; then
  log "5. search: references, never text"
  search_args="$(jq -cn --arg q "$QUERY" '{query: $q}')"
  found="$(call "${BEARER_A[@]}" -- search "$search_args")"
  jq -e '.isError != true' <<<"$found" >/dev/null || fail "search failed: $found"
  jq -e '[.structuredContent.hits[] | has("snippet") or has("text")] | any | not' <<<"$found" >/dev/null \
    || fail "a hit carries transcript text"
  jq -r '.structuredContent.hits[] | "  \(.meetingId) \(.speakerId) \(.startMs)ms"' <<<"$found" >&2
  success "$(jq '.structuredContent.hits | length' <<<"$found") hit(s), no text"
fi

newest="$(jq -r '.structuredContent.meetings[0].id // empty' <<<"$list_a")"
if [ -n "$newest" ]; then
  log "6. context for $newest"
  whole="$(rpc "${BEARER_A[@]}" -- tools/call "$(jq -cn --arg id "$newest" '{name: "context", arguments: {ids: [$id]}}')")"
  jq -e '.result.isError != true and .result.structuredContent.version == "cassini.meetings.context.v1"' <<<"$whole" >/dev/null \
    || fail "context failed: $(head -c 400 <<<"$whole")"
  success "whole: $(wc -c <<<"$whole") bytes, $(jq '.result.structuredContent.meetings[0].segments | length' <<<"$whole") passages"
  windowed="$(call "${BEARER_A[@]}" -- context "$(jq -cn --arg id "$newest" '{ids: [$id], fromMs: 0, toMs: 60000}')")"
  jq -e '.structuredContent.meetings[0].window.toMs == 60000' <<<"$windowed" >/dev/null || fail "the window was not applied: $windowed"
  success "windowed 0-60s: $(jq '.structuredContent.meetings[0].segments | length' <<<"$windowed") passages"
fi

success "MCP server validated against $ENDPOINT"
