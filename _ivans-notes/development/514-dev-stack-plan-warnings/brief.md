# Brief: dev stack plan warnings

**Linear:** [D-514](https://linear.app/code-myriad/issue/[D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan)/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) (Medium, In Progress)
**Project:** Cassini Stable 1.0.0
**Team:** Dev
**Assignee:** Ivan Kušt
**Created by:** Chiruzzi Marco

## Instruction

> I need you to read the ticket [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan), reason about it, investigate it and suggest the implementation.
> Alongside the plan include the thorough suggestions on soft warnings valid of surfacing (as the ticket describes).
> Create a pyramid of detailed scanarios:
> - which are hard failure
> - which are warnings (suggested)
>
> Perform the full planning, and stand by for execution. Ask me open questions, if any, also, if you're suggesting a large surface of warnings, create a checklist which I can mark as in/out.

## Linear source summary

[D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) follows PR #111 / [D-493](https://linear.app/code-myriad/issue/D-493/harness-modular-cassini-dev-stack-setup-unify-e2e-bring-up). PR #111 introduced `cassini dev stack` and had a `devStackPlan.ValidationWarnings []string` field plus a `printDevStackPlan` branch that could print:

```text
validation:
  warnings:
    - ...
```

That field was never populated, so the branch was unreachable and every plan printed `validation: ok`. Commit `191a12d` (`refactor(dev-stack): drop never-populated ValidationWarnings`) removed the dead field and branch. [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) asks to reintroduce the concept properly: collect non-fatal warnings for valid but probably unintended dev stack configs, print them under `validation:`, and keep warnings non-fatal.

The ticket distinguishes:

- **Errors:** invalid config; already handled in `validateDevStackPlan`; fail early with exit 2 and do not print a plan.
- **Warnings:** valid config with ignored/overridden inputs, surprising consequences, or destructive intent; do not change exit code.

Ticket candidate warnings:

1. `SPREED_PROFILE=full` with `--services core|appapi` is silently forced to `default`.
2. `--exapp-image-mode pull|reuse-local` with `--cassini none` is meaningless because no ExApp is installed.
3. `--patch force|none` with a mode lacking AppAPI / no installed Cassini has nothing to patch.
4. `--signaling-public-url` / `--media-host` when the resolved profile has no Talk media stack (`core` / `appapi`) is ignored.
5. `--cassini installed-exapp` + `--recording-backend legacy` records through the legacy/direct path, not the installed ExApp.
6. `remote-https` with an RFC1918 `--media-host` passes the non-loopback check but remote browsers usually cannot reach media.
7. Destructive dry-run intent: `up --reset`, `down --volumes`, `down --full` should say what they remove.

Ticket implementation hint:

- Add `collectDevStackWarnings(plan, opts, lookup) []string`, call it in `resolveDevStackPlan`, store on the plan.
- Restore the `printDevStackPlan` `validation:` / `warnings:` output.
- Add table-driven `dev_stack_test.go` coverage for each warning and for `validation: ok`.

## Related Linear tickets

| Ticket | Relationship | Included? |
|--------|--------------|-----------|
| [D-493](https://linear.app/code-myriad/issue/[D-493](https://linear.app/code-myriad/issue/D-493/harness-modular-cassini-dev-stack-setup-unify-e2e-bring-up)/harness-modular-cassini-dev-stack-setup-unify-e2e-bring-up) | Predecessor that introduced the typed `cassini dev stack` resolver and unified bring-up. [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) is a follow-up to make its dry-run validation truthful. | Yes, substrate only |
| [D-502](https://linear.app/code-myriad/issue/[D-502](https://linear.app/code-myriad/issue/D-502/cassini-detect-misconfiguration-and-warnguide-the-operator-recording)/cassini-detect-misconfiguration-and-warnguide-the-operator-recording) | Runtime/operator-facing health warnings for production/config drift. Same warning philosophy, different layer. | No — defer; [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) is plan-time harness warnings |
| [D-515](https://linear.app/code-myriad/issue/[D-515](https://linear.app/code-myriad/issue/D-515/decouple-the-demo-sandbox-from-the-cidev-harness)/decouple-the-demo-sandbox-from-the-cidev-harness) | Sandbox should stop depending on harness. [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan) touches harness/dev-stack only; avoid sandbox warning scope. | No |
| [D-516](https://linear.app/code-myriad/issue/[D-516](https://linear.app/code-myriad/issue/D-516/cassini-render-control-panelviewer-without-the-appapi-csp-patch-adopt)/cassini-render-control-panelviewer-without-the-appapi-csp-patch-adopt) | Explains why `--patch none` with installed ExApp may leave embedded UI CSP-blocked. A possible warning, but likely better deferred/optional. | Checklist item, suggested out |
| [D-447](https://linear.app/code-myriad/issue/[D-447](https://linear.app/code-myriad/issue/D-447/exapp-zero-config-install-self-provision-talk-recording-servers)/exapp-zero-config-install-self-provision-talk-recording-servers) | Zero-config install and recording setup. Adjacent to operator health warnings, not the dev-stack plan warning mechanism. | No |
| [D-453](https://linear.app/code-myriad/issue/[D-453](https://linear.app/code-myriad/issue/D-453/cie2e-record-through-the-installed-exapp-not-the-direct-operator)/cie2e-record-through-the-installed-exapp-not-the-direct-operator) | Consumes the unified bring-up for CI. Warnings must not break required gates or change exit codes. | Constraint only |

## Scope

In scope:

- Dev-stack resolver and plan output in `cassini-go-recorder/internal/cassini/dev_stack.go`.
- `runDevStack` warning surfacing for mutating commands (`up`, `down`) without changing exit codes.
- Go unit tests in `cassini-go-recorder/internal/cassini/dev_stack_test.go`.
- Warning scenario taxonomy and checklist for in/out selection.

Out of scope:

- Runtime/operator health UI or recording request errors ([D-502](https://linear.app/code-myriad/issue/D-502/cassini-detect-misconfiguration-and-warnguide-the-operator-recording)).
- Sandbox/runtime harness split ([D-515](https://linear.app/code-myriad/issue/D-515/decouple-the-demo-sandbox-from-the-cidev-harness)).
- Production CSP/native AppAPI UI mechanism ([D-516](https://linear.app/code-myriad/issue/D-516/cassini-render-control-panelviewer-without-the-appapi-csp-patch-adopt)), beyond optional warning text.
- Changing any currently hard-failing validation to a warning.
