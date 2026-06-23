---
shaping: true
---

# D-283 — Validation handoff

Implementation summary companion:

- `planning/initiatives/mvp/D-283-nextcloud-internal-audio-capture/implementation.md`

## Status

- **I1–I3 are the product implementation.**
- **I4 is not more recorder/operator feature work.**
- Treat **I4** as a validation/handoff slice: minimal harness wiring, a runnable proof flow, and a narrow debug checklist for the internal-bootstrap seam.

So for MVP, no further recorder/operator code is required beyond I1–I3.
What remains is making sure a supported HPB harness advertises and accepts the internal-client path.

If a local Docker setup cannot provide one signaling URL that both Nextcloud and the host-run recorder can reach, treat that as a harness-topology issue, not additional D-283 product work.

## Minimal wiring required in a valid harness

A teammate validating D-283 needs all of the following:

1. **Standalone signaling + HPB/MCU** enabled.
2. A signaling **internal client secret** configured in signaling:
   - `harness/config/signaling.conf`
   - `[clients]`
   - `internalsecret = ...`
3. The same secret exposed to Cassini as:
   - `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`
4. The existing recording secret exposed to Cassini as:
   - `CASSINI_TALK_RECORDING_SECRET`
5. Nextcloud configured to advertise a **reachable standalone signaling URL** in signaling settings.
   - The exact URL is harness-specific.
   - The important rule is: it must be reachable from both **Nextcloud** and the **host-run recorder**.
   - If the default Docker gateway URL works in your harness, keep it.
   - If you need a shared alias instead, set `HARNESS_SIGNALING_HOST` before bootstrap and make sure both sides can resolve it.

## Repo-side minimal support added for handoff

This repo now carries the minimum handoff wiring:

- `harness/config/signaling.conf`
  - adds `[clients].internalsecret`
- `harness/bin/common.sh`
  - exposes `SIGNALING_INTERNAL_SECRET`
  - allows optional `HARNESS_SIGNALING_HOST` override when a shared host/container signaling alias is needed
  - fixes `occ_has` so bootstrap can use `talk:signaling:add` when available
- `harness/bin/bootstrap.sh`
  - rewires external signaling through `talk:signaling:add`
  - clears the old legacy `signaling_servers` value first when needed
  - uses tolerant OCC calls for idempotent bootstrap steps
- `deployment/.env`
  - includes `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`

## Minimal proof flow

From the repo root on a known-good harness:

```bash
source harness/bin/common.sh

docker compose -p spreedtest -f harness/compose.yml up -d nextcloud signaling
# Optional: export HARNESS_SIGNALING_HOST=signaling.localhost if your harness
# needs a shared host/container alias instead of the default gateway URL.
./harness/bin/bootstrap.sh

CALL_URL="$(./harness/bin/create-room.sh --name "D-283 validation" | tail -n1)"
./bin/cassini dev player video --call-url "$CALL_URL" --duration 20 &
PLAYER_PID=$!

CASSINI_TALK_RECORDING_SECRET="$CASSINI_TALK_RECORDING_SECRET" \
CASSINI_TALK_SIGNALING_INTERNAL_SECRET="$SIGNALING_INTERNAL_SECRET" \
./bin/cassini record --call "$CALL_URL" --duration 15 --out /tmp/d283-validation.run

wait "$PLAYER_PID"
```

Optional explicit fallback check:

```bash
CASSINI_TALK_RECORDING_SECRET="$CASSINI_TALK_RECORDING_SECRET" \
./bin/cassini record --call "$CALL_URL" --talk-auth-mode guest-participant --duration 15 --out /tmp/d283-guest-validation.run
```

## What counts as success

For D-283, the important proof is that the default internal path is actually exercised:

1. `cassini record --call ...` selects `hpb-internal` by default.
2. Cassini fetches signaling settings through recording auth.
3. Cassini connects to standalone signaling.
4. Cassini sends internal `hello`.
5. Cassini sends `internal/incall`.
6. Cassini joins the room without a Nextcloud participant session id.
7. Cassini discovers remote participants and reuses the existing subscriber / `requestoffer` / `OnTrack` path.

A final media artifact is ideal, but the main D-283 handoff question is whether the **bootstrap/auth seam** is working on a supported harness.

## Interpreting failures

Use this table to keep follow-up narrow:

| Failure | Meaning | Next place to inspect |
|---|---|---|
| `missing CASSINI_TALK_SIGNALING_INTERNAL_SECRET` | Cassini deployment config is incomplete | operator/CLI env |
| `signaling settings missing signaling server (standalone signaling required)` | Nextcloud is still advertising internal signaling or the wrong server URL | harness bootstrap + Talk signaling config |
| `invalid_client_type` during signaling `hello` | signaling did not load `internalsecret` | signaling config + signaling restart |
| `invalid_token` or auth failure during internal `hello` | Cassini secret and signaling `internalsecret` do not match | shared secret wiring |
| join succeeds but no participants/tracks arrive | likely runtime invalidation in room event timing/payload handling | room `join`, participants events, subscriber creation, `requestoffer` |
| `no remuxable streams found` | not automatically an auth/bootstrap failure | verify publishers actually sent media; then inspect subscriber/track flow |

## Session-artifact inspection

If a run fails after starting, inspect the partial run bundle:

- `sessions/<session_id>/session.json`
- `sessions/<session_id>/events.ndjson`
- `sessions/<session_id>/streams/`

That is the preferred seam for checking whether the failure happened in:

- signaling settings fetch
- internal `hello`
- internal `incall`
- room join / participants discovery
- subscriber setup / offer request

## MVP conclusion

For MVP, D-283 should be considered **implemented by I1–I3**.

I4 remains valuable, but only as:

- harness wiring
- validation instructions
- a narrow runtime-debug handoff

It is a precaution against downstream runtime invalidation, not a sign that more product-shape work is required.
