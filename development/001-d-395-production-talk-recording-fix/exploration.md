---
shaping: true
---

# D-395 — Exploration Summary

Detailed spike notes:

- `spike-x1-exapp-suite-mismatches.md` — concrete mismatches between `deployment/` and installed ExApp.
- `spike-x2-installed-exapp-harness.md` — how to use the local harness/VM as a production-installed ExApp simulator.
- `spike-x3-nextcloud-talk-appapi-config-flow.md` — Nextcloud Talk recording secret flow, AppAPI env filtering/mutability, and secret-rotation implications.

## Main conclusion

The highest-confidence production mismatch is env-var declaration parity:

```text
deployment/compose.yml passes CASSINI_TALK_SIGNALING_INTERNAL_SECRET
appinfo/info.xml does not declare CASSINI_TALK_SIGNALING_INTERNAL_SECRET
AppAPI silently drops undeclared deploy env vars
Talk-triggered jobs default to hpb-internal
hpb-internal recorder startup requires CASSINI_TALK_SIGNALING_INTERNAL_SECRET
```

Therefore a normal installed ExApp can have the Talk recording secret configured and still fail every current default Talk recording because the signaling internal secret never reaches the container.

## Supporting conclusions

- `/operator/status` needs to report internal signaling secret presence; otherwise admins cannot diagnose the production setup without shell access.
- `docs/exapp-install.md` needs to be updated from guest/public-only wording to HPB-internal/default recording.
- Existing CI/harness coverage is split and incomplete for D-395:
  - install/UI proxy validation exists;
  - Talk recording validation exists;
  - private 1:1 playback validation exists;
  - but none proves private 1:1 Talk recording through an actually installed AppAPI/HaRP ExApp.
- `harness/bin/manual-test-setup.sh` is the right base for local production simulation, and the user wants installing the ExApp to be the default harness behavior.
- Changing Nextcloud Talk's `spreed.recording_servers.secret` does not update a running ExApp's `CASSINI_TALK_RECORDING_SECRET`; secret changes require coordinated Talk config + ExApp redeploy/reinstall.
- `app_api:app:update` reuses stored deploy options and has no `--env` option; existing pre-D-395 installs may need controlled reinstall/re-register with data preserved to inject the newly declared internal signaling secret.
- D-395 should validate current archive preservation with two separate recording jobs rather than redesigning the storage model.

## VM verification

`dev-vm` was verified running before execution planning:

```text
Name: dev-vm
State: Running
IPv4: 192.168.252.29
Docker: 29.1.3
Docker Compose: 2.40.3
```

Actual mounts:

```text
/Users/ivan/dev/cassini => /home/ubuntu/cassini
/Users/ivan/dev/cassini => /home/ubuntu/dev/workspace
```

The user-mentioned `/home/dev/ubuntu/workspace` path was not present.

## Recommended implementation direction

Implement Shape B from `shaping.md`:

1. Manifest/status parity for HPB-internal config.
2. Docs/env/deployment alignment.
3. Installed-by-default ExApp harness setup with required env vars and repeat-local-install support if straightforward.
4. Private admin + Erlich 1:1 validation through Talk → AppAPI proxy → installed ExApp.
5. Two-job archive preservation assertion.
