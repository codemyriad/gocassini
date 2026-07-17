---
shaping: true
---

# Spike X2 — Warning surface and scenario pyramid

## Context

The user explicitly asked for a pyramid of detailed scenarios: which are hard failures and which are suggested warnings. The ticket gives a candidate warning list, but several adjacent scenarios are visible in the code/docs and need triage.

## Goal

Classify dev-stack scenarios by severity, identify the warning set worth shipping in [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan), and provide a checklist for optional warning surface.

## Questions

| # | Question |
|---|----------|
| **X2-Q1** | Which existing scenarios must remain hard failures? |
| **X2-Q2** | Which ticket candidates are valid warning scenarios after code investigation? |
| **X2-Q3** | Which additional warnings are worth considering, and which should be deferred to avoid noise? |
| **X2-Q4** | What exact trigger/message shape should tests pin? |

## Acceptance

Spike is complete when warnings are separated from hard failures, proposed warning messages are deterministic, and optional warning surface is captured as an in/out checklist.

---

## Scenario pyramid

Read bottom-up: lower layers are stronger invariants. A lower-layer failure stops before warnings exist. Upper layers are valid plans that should still execute, with warnings.

```text
        P5  Destructive dry-run intent warnings
            - up --reset, down --volumes, down --full

      P4  Valid but likely unintended combo warnings
          - installed ExApp but legacy/direct recording backend
          - remote-https media host is private/RFC1918
          - non-none recording backend with no media services

    P3  Silent ignored / overridden input warnings
        - SPREED_PROFILE overridden by explicit service mode
        - image mode selected while Cassini install is none
        - patch mode selected while no installed ExApp patch can run
        - media/signaling remote values with no media stack

  P2  Runtime hard failures after plan resolution
      - Docker unavailable, existing resources conflict, missing local image,
        AppAPI registration failure, secret mismatch

P1  Plan validation hard failures
    - invalid enum/topology/public-mode/recording/cassini combinations

P0  Parse and command-scope hard failures
    - unknown flags, disagreeing aliases, flags on wrong subcommands
```

### Hard-failure scenarios (must not become warnings)

| ID | Scenario | Why hard failure | Current owner |
|----|----------|------------------|---------------|
| H1 | Unknown flag or malformed flag usage | CLI cannot know intended config | Go flag parser |
| H2 | `--services` and `--service-mode` disagree | Contradictory aliases | `parseDevStackFlags` |
| H3 | Lifecycle flags on wrong command (`--reset` on `down`, `--full` on `up`, etc.) | Command cannot apply flag | `resolveDevStackPlan` |
| H4 | Mutually exclusive lifecycle flags (`--resume --reset`; `down --suspend --volumes`) | Contradictory lifecycle intent | `resolveDevStackPlan` |
| H5 | Invalid enum values for modes | Unsupported configuration | `validateDevStackPlan` |
| H6 | `--public-host` includes scheme/path | Ambiguous value shape | `validateDevStackPlan` |
| H7 | Remote inputs under implicit `local-http` | Would silently change topology if accepted | remote-input contradiction check |
| H8 | `remote-https` missing HTTPS public URL/host | Remote browser URL cannot be constructed | `validateDevStackPlan` |
| H9 | `remote-https` missing `media-host` | Remote WebRTC media address required by mode | `validateDevStackPlan` |
| H10 | `remote-https` `media-host` is loopback | Remote browser cannot reach loopback on harness host | `isLoopbackHost` |
| H11 | `remote-https` signaling URL is non-HTTPS | Browser-facing signaling endpoint violates remote HTTPS mode | `validateDevStackPlan` |
| H12 | `lan-http` public URL is non-HTTP | Mode contract violation | `validateDevStackPlan` |
| H13 | `--services full-remote` without `--public-mode remote-https` | Full-remote service mode depends on remote config | `validateDevStackPlan` |
| H14 | `--recording-backend installed-exapp` without `--cassini installed-exapp` | Backend target does not exist | `validateDevStackPlan` |
| H15 | `direct-operator` / `installed-exapp` backend with `core`/`appapi` services | These explicit recording modes need media services | `validateDevStackPlan` |
| H16 | `--cassini installed-exapp --services core` | AppAPI/HaRP services absent | `validateDevStackPlan` |
| H17 | `--build` / image mode `build` with `--cassini none` | Build target is impossible/unused enough to be rejected already | `validateDevStackPlan` |
| H18 | Docker/AppAPI/runtime setup failures | Only knowable while mutating stack | harness shell scripts |

### Suggested warning scenarios

| ID | Suggested | Trigger | Message shape | Rationale |
|----|:--------:|---------|---------------|-----------|
| W1 | IN | `SPREED_PROFILE` env is set and differs from the profile forced by `--services core|appapi` | `SPREED_PROFILE=full is ignored because service mode appapi forces SPREED_PROFILE=default.` | Ticket candidate; real silent override in `resolveDevStackSpreedProfile` / shell `stack-env.sh` |
| W2 | IN | `SPREED_PROFILE` env is set and differs from the profile forced by `--services full|full-remote` | `SPREED_PROFILE=default is ignored because service mode full forces SPREED_PROFILE=full.` | Same override class as W1; less common but same mechanism |
| W3 | IN | `--exapp-image-mode pull|reuse-local` or `CASSINI_HARNESS_EXAPP_IMAGE_MODE=pull|reuse-local` is explicitly set while `--cassini none` | `ExApp image mode pull is ignored because Cassini mode is none.` | Ticket candidate; default `reuse-local` must not warn unless user set it |
| W4 | IN | `--patch force|none` or `CASSINI_HARNESS_PATCH_MODE=force|none` is explicitly set while no installed ExApp patch can run (`cassini none`) | `Patch mode force is ignored because Cassini mode is none; no AppAPI CSP patch will run.` | Ticket candidate, generalized to actual patch predicate (`cassini installed-exapp`) |
| W5 | IN | resolved service mode has no media stack (`core`/`appapi`, or legacy-default with `SPREED_PROFILE!=full`) and remote media/signaling values are present or derived | `Media/signaling remote inputs are ignored because service mode appapi does not start the Talk media stack.` | Ticket candidate; avoids implying `media_host` / `signaling_url` are active when no Janus/signaling/coturn services start |
| W6 | IN | `--cassini installed-exapp` with `--recording-backend legacy` | `Cassini is installed as an ExApp, but Talk recording uses the legacy backend; the installed ExApp will not receive recording callbacks.` | Ticket candidate; likely mistake when validating installed ExApp recording |
| W7 | IN | `--cassini installed-exapp` with `--recording-backend direct-operator` | Same as W6 but `direct-operator` | Same bypass class as legacy, but could be intentional for UI/install debugging |
| W8 | IN | `remote-https` with literal RFC1918 IPv4 `media-host` (`10/8`, `172.16/12`, `192.168/16`) | `remote-https media host 192.168.1.10 is private/RFC1918; browsers outside that private network will not reach WebRTC media.` | Ticket candidate; passes existing non-loopback check but often fails from truly remote browsers |
| W9 | IN | `up --reset` or `CASSINI_HARNESS_EXISTING=reset` | `up --reset will remove and recreate Docker Compose resources for the resolved project; installed ExApp state is also removed in installed-exapp mode.` | Ticket candidate; destructive dry-run visibility |
| W10 | IN | `down --volumes` | `down --volumes will remove current project containers and volumes; installed ExApp state volumes may be removed if present.` | Ticket candidate; destructive dry-run visibility |
| W11 | IN | `down --full` | `down --full will remove all known harness-owned Compose and installed-ExApp resources, including volumes.` | Ticket candidate; broadest destructive action |
| W12 | IN | `recording-backend legacy` explicitly set with service mode lacking media stack | `Recording backend legacy will not be configured because service mode appapi does not start the Talk media stack; use --recording-backend none for install-only checks.` | Adjacent code truth: `bootstrap.sh` logs this skip; plan should reveal it before mutation |
| W13 | OUT by default | default `recording-backend legacy` with `--services core|appapi` but no explicit backend | Same as W12 | Avoid warning noise on service-only experiments unless the user selected the backend |
| W14 | OUT by default | `--cassini installed-exapp --patch none` | `Patch mode none may leave control-panel/viewer scripts blocked by AppAPI CSP on current stock installs.` | True per [D-516](https://linear.app/code-myriad/issue/D-516/cassini-render-control-panelviewer-without-the-appapi-csp-patch-adopt), but `patch none` is a documented intentional validation mode; likely too noisy for [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) |
| W15 | OUT by default | explicit non-remote `--public-mode local-http|lan-http` masks ambient remote env | `Explicit local-http masked remote harness env vars ...` | Existing behavior is deliberate to make local E2E deterministic; warning may annoy CI/dev shells |
| W16 | OUT by default | `remote-https` public URL/host is RFC1918/private | Similar to W8 but public URL | Private LAN/VPN public URLs are often intentional; ticket only names media host |
| W17 | OUT by default | `--exapp-image-mode pull` with `--cassini installed-exapp` | Pulls published image, not local code | Intentional release/install testing path; not a warning without stronger signal |

## Message and trigger notes

### User-provided intent versus defaults

Warnings W3/W4 must not fire on the default plan:

```text
cassini: none
exapp_image_mode: reuse-local   # default
patch: auto                     # default
```

That plan should remain:

```text
validation: ok
```

Trigger only when the relevant flag was explicit or the env var was non-empty. This preserves the ticket's `ok` versus `warnings` distinction.

### Media stack predicate

The Go helper should mirror shell truth:

```text
media selected if:
  service mode is full or full-remote
  OR resolved SPREED_PROFILE is full
```

That keeps `legacy-default` compatible: default `legacy-default` resolves `SPREED_PROFILE=full`, so it has media. `core` and `appapi` force `default`, so they do not.

### RFC1918 predicate

Use literal IP parsing only. Do not do DNS in plan resolution. Suggested Go mechanism:

```text
net/netip.ParseAddr(trim brackets from host)
  -> 10.0.0.0/8
  -> 172.16.0.0/12
  -> 192.168.0.0/16
```

Do not include Tailscale CGNAT (`100.64.0.0/10`) in the warning; [D-493](https://linear.app/code-myriad/issue/D-493/harness-modular-cassini-dev-stack-setup-unify-e2e-bring-up)'s remote guide uses Tailscale-style hosts and those can be reachable by the remote browser.

## Checklist for user mark-up

Suggested execution set is **W1-W12 in**, **W13-W17 out**. The same checklist is copied into `open_questions.md` for response.

- [x] W1 — `SPREED_PROFILE` ignored by `core|appapi` service mode.
- [x] W2 — `SPREED_PROFILE` ignored by `full|full-remote` service mode.
- [x] W3 — explicit ExApp image mode ignored because `--cassini none`.
- [x] W4 — explicit patch mode ignored because `--cassini none`.
- [x] W5 — media/signaling remote inputs ignored because no Talk media stack.
- [x] W6 — installed ExApp selected but recording backend is `legacy`.
- [x] W7 — installed ExApp selected but recording backend is `direct-operator`.
- [x] W8 — `remote-https` media host is RFC1918/private IPv4.
- [x] W9 — `up --reset` destructive intent.
- [x] W10 — `down --volumes` destructive intent.
- [x] W11 — `down --full` destructive intent.
- [x] W12 — explicit non-`none` recording backend ignored because no media stack.
- [ ] W13 — default `legacy` recording backend with no media stack (defer to avoid default noise).
- [ ] W14 — `--patch none` with installed ExApp may CSP-block UI (defer to [D-516](https://linear.app/code-myriad/issue/D-516/cassini-render-control-panelviewer-without-the-appapi-csp-patch-adopt) / intentional validation mode).
- [ ] W15 — explicit local mode masks remote env (defer; intentional deterministic behavior).
- [ ] W16 — private/RFC1918 public URL/host in `remote-https` (defer; LAN/VPN may be valid).
- [ ] W17 — `--exapp-image-mode pull` with installed ExApp pulls published image (defer; intentional release path).

## Spike conclusion

The warning pyramid is viable if warnings are provenance-aware. The highest-value [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) set is the ticket candidates plus two adjacent code-truth warnings: direct-operator bypass with installed ExApp (W7) and explicit non-none recording backend skipped by no-media services (W12). Keep broader production/runtime and intentional validation warnings out unless explicitly requested.
