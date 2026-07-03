# Talk recording — troubleshooting: VM browser access

## Symptom

The D-395 installed-ExApp harness started successfully inside `dev-vm`, but the Mac browser could not access Nextcloud through the VM IP.

Playwright reproduced the failure at:

```text
http://192.168.252.29:28080/
```

Nextcloud returned:

```text
Access through untrusted domain
```

## Root Cause

The manual D-395 harness is run inside the Multipass VM, but the browser runs on the Mac host. That means the browser must use the VM-reachable URL:

```text
http://<vm-ip>:28080/
```

Before this fix, `harness/bin/manual-test-setup.sh` used the shared `harness/bin/bootstrap.sh`, which only added these local/operator-facing domains to Nextcloud:

```text
host.docker.internal
<docker-compose-gateway>
```

It did not add the VM IP (`192.168.252.29`) to `trusted_domains`. Nextcloud therefore rejected Mac-host browser requests before the login page could load.

This was easy to miss because the private Talk validation helper can repair trust for its own `--nextcloud-host` path later. The browser flow failed immediately after harness setup, before that helper had a chance to add the host.

## Fix Applied

The shared manual harness now behaves correctly when invoked inside a VM:

```text
harness/bin/common.sh
```

Detects a routable VM source IP with `ip route get` when `systemd-detect-virt` reports a VM, then exports it as `CASSINI_HARNESS_HOST`. On non-VM local runs it keeps the old `127.0.0.1` default.

```text
harness/compose.yml
```

Includes `${CASSINI_HARNESS_HOST}` in `NEXTCLOUD_TRUSTED_DOMAINS` for fresh Nextcloud installs.

```text
harness/bin/bootstrap.sh
```

Normalizes `CASSINI_HARNESS_HOST` and appends it to Nextcloud `trusted_domains` at a high index so it does not clobber image-supplied domains such as `nextcloud`.

```text
harness/bin/manual-test-setup.sh
```

Prints browser-facing URLs using `CASSINI_HARNESS_HOST`, so VM runs now say to open `http://192.168.252.29:28080/` instead of the VM-local `http://127.0.0.1:28080/`.

## Working Steps

From the Mac host repo:

```bash
multipass list
multipass info dev-vm
```

Confirm the shared harness detects the browser-facing VM IP:

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && source harness/bin/common.sh && printf "%s\n" "$CASSINI_HARNESS_HOST"'
```

Expected output for the current VM:

```text
192.168.252.29
```

Start the installed ExApp harness inside the VM:

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && SPREED_PROFILE=full ./harness/bin/manual-test-setup.sh'
```

Expected setup evidence:

```text
System config value trusted_domains => 12 set to string 192.168.252.29
✓ ExApp registration completed
✓ status reports Talk recording + signaling secrets configured
```

Open from the Mac browser:

```text
http://192.168.252.29:28080/
```

Log in with:

```text
admin / admin
```

Then open Talk:

```text
http://192.168.252.29:28080/index.php/apps/spreed/
```

## One-Off Recovery For An Existing Pre-Fix Stack

If the stack is already running and only the trusted domain is missing, either rerun the harness or add the VM IP manually inside the VM:

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && docker compose -p cassini-exapp-test -f harness/compose.yml exec -T -u www-data nextcloud php occ config:system:set trusted_domains 12 --value="192.168.252.29"'
```

After that, refresh the Mac browser at:

```text
http://192.168.252.29:28080/
```

## Validation Performed

Syntax checks passed:

```bash
bash -n harness/bin/common.sh harness/bin/bootstrap.sh harness/bin/manual-test-setup.sh
```

VM host detection returned `192.168.252.29`:

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && source harness/bin/common.sh && printf "%s\n" "$CASSINI_HARNESS_HOST"'
```

The full installed-ExApp harness completed in `dev-vm` with Cassini registered and enabled:

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && SPREED_PROFILE=full ./harness/bin/manual-test-setup.sh'
```

Playwright from the Mac host then reached:

```text
http://192.168.252.29:28080/login
http://192.168.252.29:28080/index.php/apps/spreed/
```

The Talk UI loaded with page title:

```text
Talk - Nextcloud
```

## Viewer File Serving Failure

After browser access was fixed, the standalone ExApp viewer still failed at:

```text
http://192.168.252.29:28080/index.php/apps/app_api/proxy/gocassini/viewer/
```

The UI showed:

```text
Unexpected token '<', "<!doctype "... is not valid JSON
```

Playwright showed the failing request:

```text
GET /index.php/apps/app_api/proxy/gocassini/viewer/catalog.json -> 200 OK
```

but the response body was the viewer `index.html`, not `catalog.json`.

Root cause: the viewer SPA is served at `/viewer/`, and the standalone bundle resolves the catalog as `./catalog.json`. The operator's old `/viewer/*` handler used SPA fallback for unknown paths, so `/viewer/catalog.json` and `/viewer/meetings/*.opus` were handled as viewer routes instead of published archive files.

Fix: `cassini-operator/internal/operator/exapp.go` now serves these standalone-viewer archive paths from the published site root before falling back to the SPA:

```text
/viewer/catalog.json
/viewer/meetings/*
```

This preserves `/viewer/assets/*` and deep viewer routes as SPA assets/routes.

## Viewer Portable Transcript Failure On VM HTTP Origin

Once file serving was fixed, opening a portable `.opus` meeting on the Mac browser showed:

```text
Cannot read properties of undefined (reading 'digest')
```

Root cause: `http://192.168.252.29:28080` is not a secure browser origin, so `crypto.subtle` is unavailable. The portable Opus transcript verifier used `crypto.subtle.digest` for SHA-256 verification.

Fix: `cassini-viewer/src/viewer/portable.ts` now keeps SHA-256 verification but falls back to a small pure TypeScript SHA-256 implementation when WebCrypto is unavailable.

## Validation Helper Fail-Early Fix

During an exploratory longer run, a failed first job exposed that `validate-installed-exapp-private-talk.sh` did not actually stop at the top level. The failure happened inside command substitution, so the parent shell continued polling for an empty job id.

Fix: the helper now stores job ids in top-level variables and calls the wait functions directly, so `fail` exits the main helper immediately.

The helper also treats a published portable `.opus` catalog entry as visible even when ASR returns zero segments. That checks the viewer-serving contract without blocking forever on a valid but empty transcript artifact.

## Viewer Fix Validation

Validated with a rebuilt installed ExApp in `dev-vm`:

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && SPREED_PROFILE=full ./harness/bin/manual-test-setup.sh --build'
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && ./harness/bin/validate-installed-exapp-private-talk.sh --nextcloud-host 192.168.252.29 --duration 60 --job-timeout 900'
```

Final successful jobs:

```text
01KW2KB4BC1HA31054YA25WBB1
01KW2KDNF89F4YBZ1549T3R6Y6
```

Playwright confirmed:

```text
GET /viewer/catalog.json -> 200 OK
GET /viewer/meetings/01KW2KB4BC1HA31054YA25WBB1.opus -> 206 Partial Content
GET /viewer/meetings/01KW2KDNF89F4YBZ1549T3R6Y6.opus -> 206 Partial Content
```

The viewer listed both jobs and both opened from the Mac browser without console errors. Job `01KW2KB4BC1HA31054YA25WBB1` displayed a transcript; job `01KW2KDNF89F4YBZ1549T3R6Y6` opened as a valid portable artifact with an empty transcript.
