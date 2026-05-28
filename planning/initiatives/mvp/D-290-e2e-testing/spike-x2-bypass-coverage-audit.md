---
shaping: true
---

# X2 Spike: Audit bypass-test assertions before retirement

## Context

Shape C's Slice 4 retires the three bypass-based tests (`ci-e2e-exapp.sh`, `ci-e2e-install-exapp.sh`, `ci-e2e-talk-record-roundtrip.sh` CPU + CUDA). R3.2 requires that any unique coverage they provide be preserved — extracted into Go unit tests, or added as assertions in Shape C's phases. The R3.2 ❌ in the Fit Check is the flag this spike lifts.

## Goal

Enumerate every assertion in the three scripts; classify each as one of:

- **Absorbed** — Shape C's full-path test (or a kept independent test) provides equivalent coverage. Safe to delete with the script.
- **Migrate to unit test** — load-bearing assertion not exercised by full-path. Becomes a Go unit test, usually in `cassini-operator/internal/operator/`.
- **Add to Shape C** — best-fit as an extra assertion inside one of C's phases.
- **Drop** — irrelevant (e.g. control-panel out of scope per R7.5, or implementation-detail of the bypass not needed by the proxy path).

## Method

Pure reading. Grep `fail`, `assert_status`, `grep -q`, etc. in each script; classify each hit.

## Audit

### `ci-e2e-exapp.sh` (operator HTTP plane + AppAPI middleware; entrypoint bypassed)

| # | Assertion | Classification | Migration target |
|---|---|---|---|
| E1 | `GET /operator/jobs` WITHOUT `AUTHORIZATION-APP-API` → 401 | **Migrate to unit test** | Go test on AppAPI middleware: negative-auth case. Full-path always sends valid auth (NC + AppAPI provide it), so this case is never exercised at the full-path level. |
| E2 | `GET /operator/jobs` WITH malformed `AUTHORIZATION-APP-API` → 401 | **Migrate to unit test** | Same target as E1. |
| E3 | `GET /operator/jobs` WITH wrong `APP_SECRET` → 401 | **Migrate to unit test** | Same target as E1. |
| E4 | `GET /operator/jobs` WITH valid auth → 200 | Absorbed | Shape C's viewer + admin operator-jobs traffic during install proves 200 through the chain. |
| E5 | `PUT /enabled?enabled=1` → 200 | Absorbed | AppAPI install hits this lifecycle callback. |
| E6 | `PUT /enabled?enabled=0` → 200 | Absorbed | Same; AppAPI disable in I4-style cycle. |
| E7 | `POST /init` → 200 | Absorbed | AppAPI install hits `/init`. |
| E8 | `POST /init` (replay, idempotent) → 200 | **Migrate to unit test** | Go test on `initHandler`: invoke twice, assert both 200. Replay is a contract that the install path doesn't naturally exercise twice. |
| E9 | `GET /control-panel/` WITH admin auth → 200 | **Drop** | Control-panel out of scope (R7.5; control-panel rewrite is a separate unit of work). |
| E10 | `GET /control-panel/<spa-route>` SPA fallback → 200 | **Drop** | Same. |
| E11 | `GET /viewer/` WITH user auth → 200 | Absorbed | Shape C's viewer phase. |
| E12 | `GET /operator/jobs` after `docker restart` → 200 | **Migrate to unit test** | State-persistence contract (operator data survives restart). Go test on the persistence layer is faster + tighter than re-doing a container restart in CI. |

### `ci-e2e-install-exapp.sh` (AppAPI install handshake; manual-install daemon bypasses HaRP)

| # | Assertion | Classification | Migration target |
|---|---|---|---|
| I1 | Bootstrap NC + AppAPI succeeds | Absorbed | H (install harness). |
| I2 | `/heartbeat` reaches 200 within 30 attempts | Absorbed | Install reaching `[enabled]` implies heartbeat succeeded. |
| I3 | `app_api:app:register` succeeds; no `heartbeat check failed` in register log | Absorbed | H's `--wait-finish` makes the same guarantee. |
| I4 | Container state `"enabled":true` after `disable→enable` cycle | **Add to Shape C** | Add a one-time `disable→enable` after install in C's install phase, asserting the PUT `/enabled` callback fires both ways. Small addition; live integration assertion. |
| I5a | admin can `GET control-panel/` → 200 | **Drop** | Control-panel out of scope. |
| I5b | admin can `GET operator/jobs` → 200 | Absorbed | Implied by install integration. |
| I5c | admin can `GET viewer/` → 200 | Absorbed | Shape C's viewer phase (admin is also a user). |
| I6 | non-admin can `GET viewer/` → 200 | Absorbed | Shape C's viewer phase asserts `alice` (non-admin). |
| I7a | non-admin `GET control-panel/` → 404 | **Drop** | Control-panel out of scope. |
| I7b | non-admin `GET operator/jobs` → 404 | **Add to Shape C** | Forbidden-route ACL assertion. Tiny addition to viewer phase: curl as `alice`, assert 404. |

### `ci-e2e-talk-record-roundtrip.sh` (Talk recording roundtrip; direct-to-operator wiring bypasses AppAPI proxy + HaRP) — CPU and GPU variants

| # | Assertion | Classification | Migration target |
|---|---|---|---|
| T1 | NC up, healthcheck OK | Absorbed | H. |
| T2 | Bootstrap succeeds | Absorbed | H. |
| T3 | Operator `/heartbeat` reaches 200 | Absorbed | Implied by install reaching `[enabled]`. |
| T4 | Compose-network gateway resolves | **Drop** | Implementation detail of the direct-to-operator wiring; not needed when routing via proxy. |
| T5 | Talk welcome from NC → gocassini works | Absorbed | X1 already proved this via proxy; H or Shape C's install phase keeps the smoke. |
| T6 | Scenario media exists | Absorbed | Shape C's record phase needs the same fixtures. |
| T7 | OCS room creation succeeds | Absorbed | Shape C's record phase. |
| T8 | OCS recording start accepted by Talk | Absorbed | Shape C's record phase (post-Slice-0). |
| T9 | Operator logs `record started id=` within 30s | Absorbed | Shape C's record phase asserts the same marker. |
| T10 | Published bundle appears in `/srv/cassini-site/published/meeting-*/` | Absorbed | Shape C's record phase. |
| T11 | Levenshtein ratio ≥ `MIN_LEVENSHTEIN` against scenario expected text | Absorbed | Independent test `ci-e2e-v3-transcript-verify.sh` (kept per R6.2) covers transcript-quality assertion against bundled LibriSpeech. The lantern-festival multi-bot scenario can be added as a Shape C record-phase assertion if multi-bot transcript quality is valuable — defer the decision to Slice 4. |

## Migration summary

**Track A audit (2026-05-28): all five Go-unit-test migration items already covered by existing tests.** The bypass-script duplicates are pure redundancy and can be deleted in Slice 4 without writing new Go code. Two items still need to be added as Shape C phase assertions (I4, I7b).

| Origin | Status | Existing coverage |
|---|---|---|
| 🟡 E1 (missing `AUTHORIZATION-APP-API` → 401) | ✅ already covered | `cassini-operator/internal/operator/appapi/middleware_test.go:94` `TestMiddlewareMissingHeader` |
| 🟡 E2 (malformed header → 401) | ✅ already covered | `cassini-operator/internal/operator/appapi/middleware_test.go:108` `TestMiddlewareMalformedBase64` |
| 🟡 E3 (wrong `APP_SECRET` → 401) | ✅ already covered | `cassini-operator/internal/operator/appapi/middleware_test.go:122` `TestMiddlewareWrongSecret` |
| 🟡 E8 (`/init` replay idempotency) | ✅ already covered | `cassini-operator/internal/operator/lifecycle_test.go:177` `TestInitIdempotent` |
| 🟡 E12 (state survives container restart) | ✅ already covered at unit level | `cassini-operator/internal/operator/lifecycle_test.go:138` `TestEnabledStateSurvivesRestart` (lifecycle JSON) + `run_test.go:45` `TestOpenStoreBaselinesLegacySchemaDatabase` (jobs SQLite re-open) + `run_test.go:120` `TestJobsHandlerReturnsEmptyArray` (handler returns 200 against the re-opened store). The "after `docker restart`" version in the bypass test was an integration test of those primitives — once they're unit-tested, the integration version is duplication. |
| I4 (disable→enable lifecycle cycle) | **Add to Shape C install phase** | One `occ app_api:app:disable gocassini` then `occ app_api:app:enable gocassini`, assert state stays `enabled`. |
| I7b (non-admin gets 404 on `/operator/jobs`) | **Add to Shape C viewer phase** | Curl as `alice`, expect 404. |

Verified 2026-05-28 by running the existing test suite:

```
$ go test ./internal/operator/... -run "TestMiddlewareMissingHeader|TestMiddlewareMalformedBase64|TestMiddlewareWrongSecret|TestInitIdempotent|TestOpenStoreBaselinesLegacySchemaDatabase|TestJobsHandlerReturnsEmptyArray|TestEnabledStateSurvivesRestart" -v
... 7 PASS ...
```

**Lantern-festival multi-bot transcript Levenshtein (T11) is conditionally interesting** — `ci-e2e-v3-transcript-verify.sh` already covers transcript quality against LibriSpeech, but the multi-bot case is qualitatively different. Decision deferred to Slice 4; the scenario fixtures stay in the repo regardless.

## Acceptance

X2 complete. We can describe exactly what coverage the bypass tests give beyond Shape C, and we have a concrete migration target for each piece. The R3.2 ⚠️ in the Fit Check is lifted; Slice 4 (retire bypass tests) has these preconditions before it can land safely:

1. ~~The 3 Go unit tests for E1/E2/E3, E8, E12 are written and green.~~ **Already covered by the existing test suite** (see Migration summary above). No new tests required.
2. Shape C's install phase includes the disable→enable cycle (from I4).
3. Shape C's viewer phase includes the forbidden-route assertion (from I7b).

## Status

- 2026-05-28: Audit complete. Migration list captured above.
- 2026-05-28 (Track A): Re-audit of "migrate to unit test" items found all five already covered by existing tests in `cassini-operator/internal/operator/{appapi/middleware_test.go, lifecycle_test.go, run_test.go}`. No new Go code needed for Slice 4 preconditions; only Shape C phase additions (I4, I7b) remain.
