# D-395 — Execution Progress

- ✅ Slice 1 — ExApp HPB-internal config parity
- ✅ Slice 2 — Docs/env setup alignment
- ✅ Slice 3 — Installed ExApp harness setup
- ✅ Slice 4 — Private 1:1 installed-ExApp validation script
- 🔄 Slice 5 — Archive preservation hardening / regression coverage
- ⏳ Slice 6 — Final deliverables and cleanup

## Validation log

- Slice 1: `cd cassini-operator && go test ./...` ✅
- Slice 1: `xmllint --noout appinfo/info.xml` ✅
- Slice 1 note: `python3` XML parse was attempted first but local Python 3.14 `pyexpat` is broken on the host; validation was rerun with `xmllint` successfully.
- Slice 2: `rg` confirmed production docs no longer claim public-only/group-1:1-impossible recording ✅
- Slice 2: checked referenced doc/script paths exist ✅
- Slice 3: `bash -n harness/bin/manual-test-setup.sh` ✅
- Slice 3: `./harness/bin/test-exapp-image-ref.sh` ✅
- Slice 3: first VM attempt exposed existing `spreedtest-vm`/`deployment` stacks holding ports; cleaned those stale stacks and reran.
- Slice 3: second VM attempt exposed that restarting Nextcloud before initial install could leave NC uninstalled; fixed by waiting for `occ status` before restart.
- Slice 3: VM validation passed: `multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && SPREED_PROFILE=full ./harness/bin/manual-test-setup.sh --build'` ✅
- Slice 4: `bash -n harness/bin/validate-installed-exapp-private-talk.sh` ✅
- Slice 4: VM validation passed after harness rendered signaling backends for the VM IP and the helper trusted the VM host: `multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && ./harness/bin/validate-installed-exapp-private-talk.sh --nextcloud-host 192.168.252.29 --duration 60'` ✅
  - Existing catalog before final run: `01KW1WGDK5F6T7CND3767ZN69Q`
  - New job 1: `01KW1XSN0T64BBYC8XKBFB2WVG`
  - New job 2: `01KW1XVZWZCMW25D42HX7TX3HE`
