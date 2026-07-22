# Follow-ups — [D-514](https://linear.app/code-myriad/issue/D-514/dev-stack-collect-and-surface-non-fatal-config-warnings-in-plan)

The accepted scope implemented W1-W12. These scenarios were deliberately left out:

| Item | Reason |
|------|--------|
| Warn when the **default** legacy recording backend is unused by `core`/`appapi` | Would make common service-only plans noisy without explicit backend intent |
| Warn that `--patch none` may leave installed-ExApp UI blocked by CSP | `none` is an intentional validation mode; the production fix belongs to [D-516](https://linear.app/code-myriad/issue/D-516/cassini-render-control-panelviewer-without-the-appapi-csp-patch-adopt) |
| Warn when explicit local mode masks ambient remote env | Masking is intentional and keeps local CI/dev commands deterministic |
| Warn about a private/RFC1918 **public URL/host** | LAN and VPN public endpoints are often intentional; only the media host warning has strong reachability signal |
| Warn that installed-ExApp pull mode uses a published image | Pull is the documented release/install validation path, not likely unintended |
| Runtime Docker/image/AppAPI/network preflight warnings | These remain runtime hard failures in harness scripts; no network/Docker probing was added to plan resolution |
| Operator-facing production health warnings | Owned by [D-502](https://linear.app/code-myriad/issue/D-502/cassini-detect-misconfiguration-and-warnguide-the-operator-recording), not the dev harness plan |

The tests explicitly pin the first five exclusions so later collector changes cannot silently expand the warning surface.
