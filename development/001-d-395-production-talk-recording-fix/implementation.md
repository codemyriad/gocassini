# D-395 — Implementation

Status: implemented and validated in `dev-vm` on 2026-06-26.

## What changed

### Slice 1 — ExApp HPB-internal config parity

- Added `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` to `appinfo/info.xml` deploy options.
- Added `/operator/status` boolean field `talk.signaling_internal_secret_configured`.
- Added status tests for recording secret, internal signaling secret, backend URL override presence, and no secret leakage.

### Slice 2 — Docs/env setup alignment

- Updated `docs/exapp-install.md` for HPB-internal/private/group/1:1 recording.
- Documented both secrets and the AppAPI env allow-list/redeploy behavior.
- Rewrote `docs/exapp-test-locally.md` around image-only, direct AppAPI install, and production-shaped AppAPI/HaRP/Talk tiers.
- Updated D-395 env and production deployment notes.

### Slice 3 — Installed ExApp harness setup

- `harness/bin/manual-test-setup.sh` now installs Cassini as an ExApp by default.
- Added `--no-install` opt-out for the old manual registration flow.
- Registration uses AppAPI `--test-deploy-mode --wait-finish` and passes:
  - `CASSINI_TALK_RECORDING_SECRET`
  - `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`
  - optional LLM/operator env values when set
- The script verifies welcome/status/control-panel/viewer routes after install.
- The full-profile harness now starts signaling/Janus/NATS/coturn services and renders a signaling config that accepts the VM/LAN host as a Talk backend.
- The script waits for the initial Nextcloud install before restarting it for Docker socket group membership.

### Slice 4 — Private 1:1 installed-ExApp validation

- Added `harness/bin/validate-installed-exapp-private-talk.sh`.
- The helper:
  - validates installed ExApp status through AppAPI proxy;
  - trusts the provided VM host in local Nextcloud when possible;
  - runs `./bin/cassini dev play-private --scaffold-only`;
  - runs two admin + Erlich private recording jobs through Talk;
  - waits for installed ExApp jobs to succeed;
  - checks published catalog/transcript metadata through the AppAPI proxy.

### Slice 5 — Archive preservation

- The validation helper captures existing catalog IDs, runs two new jobs, and fails if any previous/new ID is missing from the final catalog.
- Added manual archive inspection commands to `docs/exapp-test-locally.md`.

## Validation performed

- `cd cassini-operator && go test ./...` ✅
- `xmllint --noout appinfo/info.xml` ✅
- `bash -n harness/bin/manual-test-setup.sh harness/bin/validate-installed-exapp-private-talk.sh` ✅
- `./harness/bin/test-exapp-image-ref.sh` ✅
- VM installed-harness setup ✅

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && SPREED_PROFILE=full ./harness/bin/manual-test-setup.sh --build'
```

- VM private installed-ExApp validation ✅

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && ./harness/bin/validate-installed-exapp-private-talk.sh --nextcloud-host 192.168.252.29 --duration 60'
```

Final validation preserved previous catalog ID `01KW1WGDK5F6T7CND3767ZN69Q` and added:

- `01KW1XSN0T64BBYC8XKBFB2WVG`
- `01KW1XVZWZCMW25D42HX7TX3HE`

The helper reported `catalog_entries=3`.

## Notes from execution

- The first VM harness attempt failed because older `spreedtest-vm`/`deployment` stacks held port `28080`; stopping those stale stacks fixed the environmental conflict.
- Restarting the Nextcloud container before the initial install finished could leave it uninstalled; `manual-test-setup.sh` now waits for `occ status` first.
- When validating through the VM IP, Nextcloud must trust that host and the signaling server must accept it as a backend URL; the helper and harness now cover this local-production case.
