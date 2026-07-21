---
shaping: true
---

# Spike X1 — Current dev-stack validation and output mechanics

## Context

[D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) asks for non-fatal warnings in `cassini dev stack plan` and `up`. Before proposing warning behavior, we need to know exactly where the current hard-failure boundary lives and how command output is structured.

## Goal

Describe the current resolver/validator/runner path well enough to place warning collection without weakening hard failures or duplicating shell-side behavior.

## Questions

| # | Question |
|---|----------|
| **X1-Q1** | Where are dev-stack flags parsed, resolved, validated, printed, and passed to scripts? |
| **X1-Q2** | Which scenarios are already hard failures? |
| **X1-Q3** | What provenance is available to decide whether a warning is about user intent versus a harmless default? |
| **X1-Q4** | How should `plan` and mutating commands surface warnings without changing exit-code semantics? |
| **X1-Q5** | Which runtime failures remain script-level hard failures, outside the plan warning collector? |

## Acceptance

Spike is complete when we can point to the code seams for parsing/resolution/validation/printing, list hard-failure scenarios, and identify the minimal data needed by a collector.

---

## Findings

### X1-Q1 — Code seams

Primary files:

| File | Role |
|------|------|
| `cassini-go-recorder/internal/cassini/dev.go` | CLI dispatcher for `cassini dev stack`; decides `plan`, `up`, `down`, `status`; returns exit codes |
| `cassini-go-recorder/internal/cassini/dev_stack.go` | Flag parsing, plan resolution, hard validation, env export, plan printing |
| `cassini-go-recorder/internal/cassini/dev_stack_test.go` | Current unit coverage for defaults, validation errors, output, script env mapping |
| `harness/bin/up.sh` / `down.sh` | Runtime stack mutation after Go plan resolution |
| `harness/bin/lib/stack-env.sh` | Shell-side env defaults/validation; should remain a safety net, not the warning source |
| `harness/bin/lib/stack.sh` | Runtime checks: Docker availability, existing resources, AppAPI/image/patch phases |

Current Go flow:

```text
runDevStack(command, args)
    |
    v
resolveDevStackPlan(command, args, osEnvLookup)
    |
    +--> parseDevStackFlags(command, args)
    |       - parses flags
    |       - records explicit flags in opts.set
    |       - normalizes --services / --service-mode alias
    |
    +--> resolve final plan values
    |       - explicit flag > CASSINI_HARNESS_* env > default
    |       - remote env masking if explicit non-remote --public-mode
    |       - --build overrides image mode to build
    |       - service mode normalizes legacy/default -> legacy-default
    |       - SPREED_PROFILE is derived from service mode
    |
    +--> validateDevStackPlan(plan)
    |       - hard failures only
    |
    `--> return plan + remaining args
            |
            +--> command == plan: printDevStackPlan(stdout, plan), exit 0
            +--> command == up: run harness/bin/up.sh with plan.env()
            +--> command == down: run harness/bin/down.sh with lifecycle args + plan.env()
            `--> command == status: run harness/bin/status.sh with plan.env()
```

The removed warning branch was exactly at the plan-printing seam. Commit `191a12d` changed `printDevStackPlan` from conditional `validation: ok` / `validation: warnings:` to unconditional `validation: ok` because `ValidationWarnings` had no producer.

### X1-Q2 — Current hard failures

These scenarios are hard failures today and should remain hard failures. They reject impossible, contradictory, or unsupported inputs before any plan is printed.

| Layer | Scenario | Current mechanism | Outcome |
|-------|----------|-------------------|---------|
| Parse | Unknown flag / malformed flag value | Go `flag` parser in `parseDevStackFlags` | exit 2 |
| Parse | `--services` and `--service-mode` both set but disagree | explicit check after `fs.Visit` | exit 2 |
| Command scope | `stop` command | removed command guard in `runDevStack` | exit 2 with pointer to `down` |
| Command scope | `--resume` or `--reset` used outside `up` | `resolveDevStackPlan` command check | exit 2 |
| Command scope | `--suspend`, `--volumes`, or `--full` used outside `down` | `resolveDevStackPlan` command check | exit 2 |
| Command scope | `--resume` and `--reset` together | lifecycle mutual-exclusion check | exit 2 |
| Command scope | `down --suspend` with `--volumes` or `--full` | lifecycle conflict check | exit 2 |
| Enum | invalid `public-mode`, `services`, `cassini`, `recording-backend`, `exapp-image-mode`, `patch`, or `CASSINI_HARNESS_EXISTING` | `validateDevStackPlan` `oneOf` checks | exit 2 |
| Public host shape | `--public-host` includes scheme or path | `validateDevStackPlan` host checks | exit 2 |
| Local public mode | remote inputs present while public mode is `local-http` | remote input contradiction check before validation | exit 2, unless explicit non-remote flag masks ambient env |
| Remote public mode | `remote-https` lacks public URL/host | `validateDevStackPlan` | exit 2 |
| Remote public mode | `remote-https` public URL is not HTTPS | `validateDevStackPlan` | exit 2 |
| Remote media | `remote-https` lacks `media-host` | `validateDevStackPlan` | exit 2 |
| Remote media | `remote-https` media host is loopback | `isLoopbackHost` in `validateDevStackPlan` | exit 2 |
| Remote signaling | `remote-https` signaling public URL is present but not HTTPS | `validateDevStackPlan` | exit 2 |
| LAN public mode | `lan-http` public URL is present but not HTTP | `validateDevStackPlan` | exit 2 |
| Topology | `--services full-remote` without `--public-mode remote-https` | `validateDevStackPlan` | exit 2 |
| Recording | `--recording-backend installed-exapp` without `--cassini installed-exapp` | `validateDevStackPlan` | exit 2 |
| Recording | `direct-operator` or `installed-exapp` backend with `core`/`appapi` services | `validateDevStackPlan` | exit 2 |
| Cassini install | `--cassini installed-exapp` with `--services core` | `validateDevStackPlan` | exit 2 |
| Image build | `--build` / image mode `build` with `--cassini none` | `validateDevStackPlan` | exit 2 |

Hard-failure rule for [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan): **warnings must only be collected after `validateDevStackPlan(plan)` succeeds**. A scenario already in this table must not be reclassified as a warning.

### X1-Q3 — Provenance available for warnings

The collector can use:

| Data | Source | Why it matters |
|------|--------|----------------|
| `opts.set[flag]` | `parseDevStackFlags` records explicit flags | Avoid warning about harmless defaults |
| `lookup(env)` | same env lookup already used by resolver | Detect user-provided env intent such as `SPREED_PROFILE=full` or `CASSINI_HARNESS_EXAPP_IMAGE_MODE=pull` |
| final `plan` values | resolved plan | Decide whether an input is ignored or surprising in the final topology |
| command | `plan.Command` | Destructive warnings apply to `up --reset`, `down --volumes`, `down --full` only |
| lifecycle booleans | `plan.DownVolumes`, `plan.DownFull`, `plan.ExistingResourceMode` | Identify destructive actions without re-parsing args |

This is why the ticket's suggested signature is the right shape:

```go
collectDevStackWarnings(plan, opts, lookup) []string
```

A collector that only sees `plan` cannot distinguish default `exapp_image_mode: reuse-local` under default `cassini: none` from a user explicitly asking for `--exapp-image-mode reuse-local` under `--cassini none`. The former should stay `validation: ok`; the latter should warn.

### X1-Q4 — Output mechanics

`plan` output is stdout and is already structured like YAML-ish text. The old branch can be restored safely:

```text
validation: ok
```

or:

```text
validation:
  warnings:
    - SPREED_PROFILE=full is ignored because --services appapi forces SPREED_PROFILE=default
```

For mutating commands, printing the full plan would be a behavior change and would interleave with harness logs. The clean seam is a small stderr block before invoking the script:

```text
dev stack up: validation warnings:
  - ...
```

Then call `runDevScriptWithEnv` exactly as today and return its exit code. Warnings are informational; they must not turn a successful `up`/`down` into failure.

### X1-Q5 — Runtime hard failures outside this ticket

Some failures are only knowable when the shell scripts talk to Docker/Nextcloud/AppAPI. They should remain runtime hard failures, not plan warnings:

| Runtime area | Existing behavior |
|--------------|-------------------|
| Docker CLI / Compose / daemon missing | `harness_require_docker` returns non-zero |
| Existing resources with default `fail` mode | `harness_check_existing_resources_for_up` fails and suggests `--resume`, `--reset`, or `down --full` |
| `--resume` resources missing/running/mismatched | `harness_validate_resume_resources` fails |
| `--reset` / `down` Docker operations fail | shell script exits non-zero |
| `--cassini installed-exapp --exapp-image-mode reuse-local` but local image missing | `harness_prepare_exapp_image` fails and suggests `--build` or pull mode |
| AppAPI/patch/register/welcome route checks fail | AppAPI install phase fails |
| Recording/signaling secrets missing/mismatched at runtime | `harness_validate_recording_secrets` fails |

The warning collector can describe valid consequences before mutation, but it should not attempt Docker or network inspection.

## Spike conclusion

Implement warnings in Go, immediately after hard validation succeeds. Keep hard failures untouched. Use `opts.set` and env lookup to avoid default-noise. Restore the plan warning branch and add a small stderr warning block for `up`/`down` before script execution.
