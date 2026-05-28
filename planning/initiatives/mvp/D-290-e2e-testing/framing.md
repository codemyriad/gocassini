---
shaping: true
---

# D-290 E2E testing — Framing

## Source

> **Linear D-290 — E2E testing** (Ivan, 2026-05-28, draft, body trails off):
>
> We need e2e tests that test the full Cassini flow (incl. Nextcloud).
>
> @silviot had expressed that he'd like to do this, employing his forte in writing tests and fitting them into the whole CI workflow (optimisations and such).
>
> The way I see this is as:
>
> * core: ensure we have a reasonable test coverage with e2e tests
> * nice-to-have: CI optimisations - run times and all
>
> I consider this a different…

> **PR #40 — "App Store packaging + install dogfood + production-bug fixes for Cassini ExApp"** (silviot, 2026-05-21, body excerpt):
>
> Known follow-up (separate PR): The existing CI install-e2e doesn't exercise the new code paths (`exapp-start.sh` wrapper, HaRP daemon, reverse-proxy). That's how the frpc bug sat green for so long. A follow-up should add a CI job that runs `manual-test-setup.sh` + `app_api:app:register --info-xml --test-deploy-mode --wait-finish` so HaRP-integration regressions surface in CI rather than at publish time.

> **Linear D-286 — "AppAPI HaRP install loops on heartbeat"** (closed 2026-05-22, excerpt):
>
> [The bug] sat undetected because the existing `ci-e2e-install-exapp.sh` uses the `manual-install` daemon and bypasses HaRP entirely. The branch's `manual-test-setup.sh` reproduced it on first run.

> **Conversation with @silviot — scope decisions (2026-05-28):**
>
> - **Scope:** Install + full recording roundtrip via the AppAPI proxy + HaRP — including the Talk record button → operator → transcript → viewer — not just install.
> - **CI efficiency:** in scope of D-290, as a later slice (after coverage lands).
> - **Bypasses:** replace existing bypass-based tests with the full-prod-path versions; don't keep both.

---

## Problem

Today's CI e2e suite is **layered with intentional bypasses**, each individually honest but cumulatively dishonest:

- `ci-e2e-exapp.sh` runs the operator image but bypasses the entrypoint — no frpc, no HaRP.
- `ci-e2e-install-exapp.sh` uses AppAPI's `manual-install` daemon — bypasses HaRP and the reverse proxy.
- `ci-e2e-talk-record-roundtrip.sh` points Talk's `recording_servers` directly at the operator container — bypasses the AppAPI proxy and HaRP.
- `ci-e2e-v3-transcript-verify.sh` exercises transcription in isolation — no install path.

Each script's top comment says what it skips, but the **union of bypasses means no CI job exercises the production install + recording path**. Production bugs that live in the bypassed code — the frpc plugin-type bug (PR #40), the healthcheck `curl -f` bug (PR #40), the HaRP heartbeat ordering bug (D-286) — sat green in CI for weeks. Each was found by hand against `manual-test-setup.sh` (the local dogfood harness introduced in PR #40), not by an automated test. The next bug in this code will follow the same path.

Beyond coverage, CI itself is **not yet optimised for the workload**: per-step granularity is uneven (implicit `docker compose up` pulls hide pull-vs-startup timing), caching is partial, and per-job runtime hasn't been profiled against a target.

## Outcome

A regression in the production install + recording path **fails loudly in CI on the PR that introduces it**, not at publish time or in a partner's deployment. Concretely, on every PR:

- AppAPI installs Cassini through the HaRP daemon end-to-end (the exact code path the App Store "Install" button triggers), and the install reports `[enabled]`.
- A Talk room with the record button enabled routes the recording trigger through the AppAPI proxy → HaRP → operator, produces a recording, transcribes it, and the resulting meeting is readable in the viewer through the proxy.
- The bypassed-by-design tests are retired in favour of these full-path versions; we don't carry two parallel suites.

Secondarily, CI run time is predictable and frugal: per-job runtimes profiled and kept within a soft target; implicit slow steps split out so the UI shows where time is going; layer and model caches reused across jobs on `george`.

The user-visible test of success: a teammate (or an admin in a pilot deployment) clicking "Install" in NC's Apps page and then "Record" in a Talk call works on the version CI just signed off on, every time — and if it ever doesn't, CI catches it on the PR that broke it.
