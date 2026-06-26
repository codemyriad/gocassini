---
shaping: true
---

# Spike X2 — Local Harness as Production-Installed ExApp

## Context

The user wants to validate D-395 inside `dev-vm`: run the local Nextcloud harness with Talk, install Cassini as an ExApp, open the ExApp UI, then run the 1:1 private admin + Erlich flow and see the transcript in the viewer.

## Goal

Determine how the existing harness can simulate production closely enough and what gaps must be closed.

## Questions

| # | Question |
|---|---|
| **X2-Q1** | Which existing harness path installs the ExApp instead of running a direct standalone operator? |
| **X2-Q2** | Does that path configure Talk to target the AppAPI proxy route? |
| **X2-Q3** | Does that path pass the env vars required by the installed ExApp? |
| **X2-Q4** | Which existing 1:1 private test assets can be reused? |
| **X2-Q5** | What is the current `dev-vm` state and mount path? |

## Findings

### X2-Q1 — Installed ExApp harness path

`harness/bin/manual-test-setup.sh` is the closest existing production-shaped path:

- starts Nextcloud, database, AppAPI HaRP, and a reverse proxy;
- installs/enables `app_api`;
- registers a HaRP deploy daemon;
- tags the local image as `ghcr.io/codemyriad/gocassini:<info.xml image-tag>` so AppAPI can consume `info.xml` verbatim;
- copies `appinfo/info.xml` into the Nextcloud container;
- prints an `occ app_api:app:register ... --info-xml ... --test-deploy-mode --wait-finish` command.

This is the right base because AppAPI spawns the ExApp and fronts it with the same route/env declaration mechanics production uses.

Gap:

- it prepares the environment but does not automatically install the ExApp, so after setup the user cannot immediately open the harness and see Cassini installed.

Planned change after user Q3 response:

- make installed ExApp registration the default harness setup behavior (with an opt-out only if preserving the old manual-install flow is useful);
- use `--test-deploy-mode` or equivalent for repeat local setup/reinstallation if straightforward;
- registration should wait for completion, cycle enable if needed, and verify menu/proxy surfaces.

### X2-Q2 — Talk recording backend route

`manual-test-setup.sh` exports:

```bash
CASSINI_TALK_RECORDING_URL="http://reverse-proxy/index.php/apps/app_api/proxy/gocassini"
```

Then `harness/bin/bootstrap.sh` writes Talk's `spreed.recording_servers` to that URL when `SPREED_PROFILE=full`.

That means Talk's native record button targets:

```text
Talk → reverse-proxy → Nextcloud AppAPI proxy → HaRP → installed ExApp
```

This is the desired production-like route.

### X2-Q3 — Missing env propagation

The printed registration command currently has no `--env` values. Even after `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` is declared in `info.xml`, the local installed ExApp still needs registration-time env options, at least:

```bash
--env CASSINI_TALK_RECORDING_SECRET="$CASSINI_TALK_RECORDING_SECRET"
--env CASSINI_TALK_SIGNALING_INTERNAL_SECRET="$SIGNALING_INTERNAL_SECRET"
```

Depending on observed URL reachability, the harness may also need:

```bash
--env CASSINI_TALK_BACKEND_URL="http://reverse-proxy"
```

Suggested default for the harness:

- set recording + internal signaling secrets always during default installed harness setup;
- leave `CASSINI_TALK_BACKEND_URL` empty unless validation shows Talk's `Talk-Recording-Backend` header advertises a URL the ExApp cannot dial;
- document the override and expose a harness env knob for it.

### X2-Q4 — Private 1:1 assets and command path

The repo already contains the private playback command:

```bash
./bin/cassini dev play-private --scaffold-only --nextcloud-host <host>
./bin/cassini dev play-private --conversation admin --duration 60
```

Relevant implementation:

- `cassini-go-recorder/internal/cassini/dev_play_private.go`
- scaffold state: `harness/runtime/play-private-scaffold.json`
- fixture: `harness/scenarios/synthetic-pied-piper.v1.json`
- participant: `cassini-erlich` / display name `Erlich Bachman`

Existing ignored runtime evidence (`harness/runtime/play-private-vm-validation-report.md`) shows this worked in VM against the standalone operator. D-395 should reuse the same player path but point Talk recording at the installed ExApp.

Planned validation helper:

- scaffold private users/conversations against the installed harness;
- run `play-private --conversation admin` so Talk recording is triggered through OCS and Talk calls the configured recording backend;
- poll installed ExApp `/operator/jobs` through AppAPI as admin until the new job is done;
- verify the viewer/published catalog through AppAPI as a user/admin contains a transcript for the new meeting;
- assert pre-existing catalog IDs remain.

### X2-Q5 — VM state

Planning-time check:

```text
Name: dev-vm
State: Running
IPv4: 192.168.252.29
Ubuntu: 24.04.4 LTS
Docker: 29.1.3
Docker Compose: 2.40.3
```

Actual mounts:

```text
/Users/ivan/dev/cassini => /home/ubuntu/cassini
/Users/ivan/dev/cassini => /home/ubuntu/dev/workspace
```

The user-provided path `/home/dev/ubuntu/workspace` does not exist. The implementation tutorial should use `/home/ubuntu/dev/workspace` unless we intentionally remount the VM.

Currently running containers include a VM Talk stack (`spreedtest-vm-*`) and the standalone deployment suite (`deployment-*`). Execution should start by tearing down or isolating any stacks that can conflict with ports/projects.

## Conclusion

The harness can simulate production if `manual-test-setup.sh` performs an installed ExApp registration with required env vars by default and we add a validation helper that reuses `cassini dev play-private` through Talk's record button. Existing e2e scripts are useful reference material but do not currently prove the installed ExApp Talk path.
