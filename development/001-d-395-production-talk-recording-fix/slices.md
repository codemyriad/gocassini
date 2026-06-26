---
shaping: true
---

# D-395 — Implementation Slices

Execution is not started. When execution is approved, follow project convention: commit the planning first, then implement slice-by-slice, validate, commit each slice, and keep `progress.md` updated with icons.

Pre-execution exploration added after user Q1 response:

- `spike-x3-nextcloud-talk-appapi-config-flow.md` explains Talk recording config, AppAPI deploy env filtering, ExApp env mutability, and secret rotation implications.

## Slice 1 — ExApp HPB-internal config parity

**Goal:** Installed ExApp can receive the same HPB-internal secret that `deployment/` already passes, through AppAPI's declared deploy-option allow-list.

**Changes:**

- `appinfo/info.xml`
  - add `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` under `<environment-variables>`.
  - use admin-facing wording: secret must match standalone signaling server `internalsecret`; required for HPB-internal/private/group/1:1 Talk recording.
- `cassini-operator/internal/operator/status.go`
  - add boolean-only reporting for internal signaling secret presence.
- `cassini-operator/internal/operator/status_test.go`
  - assert field true/false behavior and no secret leakage.

**Validation:**

- `go test ./...` in `cassini-operator`.
- XML sanity for `appinfo/info.xml` if local tooling exists.
- Status JSON contains the new boolean and no raw secret.

**Demoable result:** Admin can query `/operator/status` and see whether HPB-internal required config is present. The status still reports presence only; it does not and cannot prove the values match Nextcloud Talk config or signaling config without reading those secrets.

## Slice 2 — Docs/env setup alignment

**Goal:** Docs stop misleading admins and developers about the current production path.

**Changes:**

- Update `docs/exapp-install.md`:
  - HPB-internal is default target.
  - document `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` in app config table.
  - update registration examples with both Talk secrets.
  - replace public-room-only test with private/1:1-capable controlled test, while documenting fallback if still supported.
  - add URL reachability checks: Talk backend URL, `NEXTCLOUD_URL`, `overwrite.cli.url`, AppAPI proxy base.
  - document that changing `spreed.recording_servers.secret` does not update an already deployed ExApp container env.
  - document rotation/redeploy: Talk recording secret, signaling `internalsecret`, ExApp deploy env, and ExApp container recreation must be coordinated.
- Update `docs/exapp-test-locally.md`:
  - point production-shaped local validation at the installed AppAPI/HaRP harness.
  - clarify which existing CI scripts are direct-container vs installed-ExApp.
- Add/update development-dir docs:
  - `env-vars.md`
  - `production-deployment.md`

**Validation:**

- Link/path sanity by reading referenced scripts/docs.
- No obsolete claim that one-to-one/group recordings are impossible on the target path.

**Demoable result:** A future admin/developer can follow docs to supply all required env vars.

## Slice 3 — Installed ExApp harness setup

**Goal:** The normal harness start path leaves local Nextcloud with Cassini installed as an ExApp and Talk pointed at the AppAPI proxy.

**Changes:**

- Extend `harness/bin/manual-test-setup.sh` or add a sibling helper.
- Installing Cassini should be the default behavior when starting this harness; keep an explicit opt-out only if preserving the old manual-install flow is useful.
- If straightforward, make repeated runs reinstall/re-register with current code/env using AppAPI `--test-deploy-mode` while preserving data by default.
- Expected behavior for the chosen command:
  - build/tag local ExApp image as manifest-pinned `ghcr.io/codemyriad/gocassini:<image-tag>`;
  - start Nextcloud/db/AppAPI HaRP/reverse-proxy and full Talk services when `SPREED_PROFILE=full`;
  - register HaRP daemon and registry mapping;
  - register `gocassini` through `occ app_api:app:register --info-xml ... --test-deploy-mode --wait-finish`;
  - pass `--env CASSINI_TALK_RECORDING_SECRET=...`;
  - pass `--env CASSINI_TALK_SIGNALING_INTERNAL_SECRET=...`;
  - optionally pass `--env CASSINI_TALK_BACKEND_URL=...` only if observed URL reachability requires it;
  - cycle enable if needed for UI registration;
  - verify `/api/v1/welcome`, `/operator/status`, control panel, viewer, and menu assets.

**Validation in VM:**

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && SPREED_PROFILE=full ./harness/bin/manual-test-setup.sh --build'
```

**Demoable result:** Open `http://<vm-ip>:28080/`, log in as `admin/admin`, and see Cassini + Cassini Admin entries.

## Slice 4 — Private 1:1 installed-ExApp validation script

**Goal:** Reproducible private admin + Erlich recording through the installed ExApp.

**Changes:**

- Add a validation helper, name to settle during execution (for example `harness/bin/validate-installed-exapp-private-talk.sh`).
- Reuse `./bin/cassini dev play-private` rather than duplicating OCS/player logic.
- Validation helper should:
  - determine Nextcloud base URL / VM host;
  - capture current published catalog IDs through AppAPI proxy;
  - run `cassini dev play-private --scaffold-only --nextcloud-host <host>`;
  - run `cassini dev play-private --conversation admin --nextcloud-host <host> --duration <seconds>` for job 1;
  - poll AppAPI-proxied `/operator/jobs` as admin until job 1 reaches `done/succeeded`;
  - run a second separate `cassini dev play-private --conversation admin ...` job;
  - poll until job 2 reaches `done/succeeded`;
  - fetch catalog/transcript through AppAPI-proxied `/published/*` or viewer data route;
  - assert both new transcripts are non-empty and associated with their jobs;
  - assert previous catalog IDs remain and job 1 remains visible after job 2 publishes.

**Validation in VM:**

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && ./harness/bin/validate-installed-exapp-private-talk.sh --nextcloud-host 192.168.252.29 --duration 60'
```

**Demoable result:** Two private Talk recordings triggered by the Talk record path finish and both appear in the installed ExApp viewer.

## Slice 5 — Archive preservation hardening / regression coverage

**Goal:** D-395's "new publish doesn't delete existing recordings" criterion is machine-checked.

**Changes:**

- Prefer keeping this inside Slice 4 by making the validation helper run two separate jobs.
- If that makes Slice 4 too large, add a focused helper/test that creates two recordings and verifies the first remains after the second publish.
- Document manual inspection paths for AppAPI persistent storage:

```bash
docker exec nc_app_gocassini sh -lc 'find "$APP_PERSISTENT_STORAGE/operator/jobs/current" -maxdepth 1 -name "*.meeting" | sort'
docker exec nc_app_gocassini sh -lc 'python3 - <<PY
import json, os
p=os.path.join(os.environ["APP_PERSISTENT_STORAGE"], "site/published/catalog.json")
d=json.load(open(p))
print(len(d.get("meetings", [])))
print([m.get("id") for m in d.get("meetings", [])])
PY'
```

**Validation:**

- Run two installed-ExApp private recording jobs in one validation session.
- Confirm catalog IDs do not collapse and both new jobs remain visible.

**Demoable result:** The harness fails loudly if publish overwrites the archive with only the newest meeting.

## Slice 6 — Final deliverables and cleanup

**Goal:** Close the D-395 planning/execution loop with final user-facing runbooks.

**Changes:**

- Finalize `development/001-d-395-production-talk-recording-fix/tutorial.md`.
- Finalize `development/001-d-395-production-talk-recording-fix/env-vars.md`.
- Finalize `development/001-d-395-production-talk-recording-fix/production-deployment.md`.
- Create `implementation.md` with exact implemented changes and validations.
- Create/update `followups.md` for deferred storage/ACL/CI items.

**Validation:**

- Re-run the final tutorial commands in `dev-vm`.
- Ensure docs match actual command names/flags.

**Demoable result:** User can follow the tutorial to reproduce the installed ExApp 1:1 validation manually.
