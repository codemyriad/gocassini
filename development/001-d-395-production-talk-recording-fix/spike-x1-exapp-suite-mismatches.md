---
shaping: true
---

# Spike X1 — ExApp vs `deployment/` Mismatches

## Context

The standalone `deployment/` suite is treated as the proven working surface. Production uses a Nextcloud AppAPI ExApp: one AppAPI-managed container, env vars filtered by `appinfo/info.xml`, AppAPI/HaRP proxying, and `APP_PERSISTENT_STORAGE`.

## Goal

Identify concrete mismatches that can explain why production-installed ExApp Talk recording fails while `deployment/` works, and define what would need to change.

## Questions

| # | Question |
|---|---|
| **X1-Q1** | Which required Talk env vars are present in `deployment/` but not settable through the ExApp manifest? |
| **X1-Q2** | Does current code make HPB-internal capture the default path for Talk-triggered jobs? |
| **X1-Q3** | Does admin status expose enough information to diagnose the missing config? |
| **X1-Q4** | Are docs aligned with the current HPB-internal behavior? |
| **X1-Q5** | Are storage paths equivalent enough to preserve recordings, or does ExApp require a storage redesign? |

## Findings

### X1-Q1 — Env-var parity

`deployment/compose.yml` passes the HPB-internal secret directly:

```yaml
CASSINI_TALK_RECORDING_SECRET: ${CASSINI_TALK_RECORDING_SECRET:-}
CASSINI_TALK_SIGNALING_INTERNAL_SECRET: ${CASSINI_TALK_SIGNALING_INTERNAL_SECRET:-}
CASSINI_TALK_BACKEND_URL: ${CASSINI_TALK_BACKEND_URL:-}
```

`appinfo/info.xml` currently declares:

- `CASSINI_TALK_RECORDING_SECRET`
- `CASSINI_TALK_BACKEND_URL`
- `OPENROUTER_API_KEY`
- `LLM_BASE_URL`
- `LLM_MODEL`
- `CASSINI_OPERATOR_API_TOKEN`

It does **not** declare `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.

Because AppAPI drops undeclared deploy options, a normal installed ExApp cannot receive the HPB-internal secret through the supported registration/admin UI path.

Required change:

- add `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` to `<environment-variables>` with admin-facing copy that explains it must match the standalone signaling server `internalsecret`.

### X1-Q2 — Default Talk auth mode

`cassini-operator/internal/operator/run.go` sets:

```go
defaultTalkAuthMode = talkAuthModeHPBInternal
```

`cassini-operator/internal/operator/talk_backend.go` creates Talk-started jobs with:

```go
TalkAuthMode: talkAuthModeHPBInternal,
```

`cassini-go-recorder/internal/talk/recorder.go` rejects HPB-internal mode if either secret is absent:

```go
talk auth mode hpb-internal requires CASSINI_TALK_RECORDING_SECRET to be set
talk auth mode hpb-internal requires CASSINI_TALK_SIGNALING_INTERNAL_SECRET to be set
```

Required change:

- keep HPB-internal as the production target, but make ExApp registration capable of supplying its required secret.
- do not silently fall back to guest-participant for production; that would reintroduce the private/1:1 limitation.

### X1-Q3 — Status/doctor gap

`cassini-operator/internal/operator/status.go` currently reports:

```json
{
  "talk": {
    "secret_configured": true|false,
    "backend_url_override_configured": true|false
  }
}
```

It does not report internal signaling secret presence. That means an admin can see a green-ish installed ExApp while the default recording path will fail on the child recorder.

Required change:

- extend `statusTalk` with a boolean-only field, e.g. `signaling_internal_secret_configured`.
- tests must assert the raw secret does not appear in the status response.
- optional but useful: include a `required_for_hpb_internal`/detail or documented interpretation so admins know why a false value matters.

### X1-Q4 — Docs mismatch

`docs/exapp-install.md` still says:

- controlled test should use a public room;
- recorder joins as an anonymous guest;
- group and one-to-one conversations cannot be recorded.

That contradicts current code and desired D-395 behavior: HPB-internal is the default target specifically so private/1:1 Talk recording can work.

Required change:

- update install docs to describe HPB-internal as the default production path.
- document both secrets:
  - Talk recording backend secret (`spreed.recording_servers.secret`)
  - signaling internal secret (`nextcloud-spreed-signaling` `internalsecret`)
- describe URL reachability requirements for `CASSINI_TALK_BACKEND_URL`, `NEXTCLOUD_URL`, and `overwrite.cli.url`.

### X1-Q5 — Storage model

The storage topology differs, but the intended source-of-truth remains equivalent enough for D-395:

| Concern | `deployment/` | ExApp |
|---|---|---|
| Job DB/work root | `cassini_operator_state` → `/var/lib/cassini-operator` | `$APP_PERSISTENT_STORAGE/operator/*` via default redirection |
| Published site | `cassini_published_site` → `/srv/cassini-site/published` | `$APP_PERSISTENT_STORAGE/site/published` |
| Viewer serving | separate `cassini-viewer` container | operator serves `/viewer/*` + `/published/*` |

D-395 does not need a storage redesign. It does need validation that the published catalog does not collapse when a new meeting is added.

Required validation:

- before a private 1:1 recording, capture current `/published/catalog.json` IDs through the installed ExApp route;
- after publish, assert previous IDs are still present and the new transcript exists;
- optionally inspect `$APP_PERSISTENT_STORAGE/operator/jobs/current/*.meeting` inside the ExApp container during manual debugging.

## Conclusion

The main mismatch is concrete and fixable: `deployment/` passes `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`; installed ExApp cannot because `info.xml` omits it. The supporting gaps are status/docs/harness validation, not a need to change the core recorder model.
