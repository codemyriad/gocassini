# Installable Nextcloud App Roadmap

This document is for Cassini product and development planning. It describes what
is still needed for Cassini to feel like a normal installable Nextcloud app,
rather than a backend service that happens to integrate with Talk.

## Product Goal

An administrator should be able to install Cassini from Nextcloud's AppAPI
External Apps flow, provide the Talk recording secret and backend settings,
choose a CPU or CUDA deploy daemon, verify the installation, and then find the
admin control panel and user viewer from the Nextcloud UI.

The desired user story is:

1. Install Cassini as an AppAPI ExApp.
2. Confirm the container is healthy.
3. Configure Talk to use Cassini's AppAPI proxy URL.
4. Start and stop a test recording from Talk.
5. See the recording in Cassini's viewer.
6. Upgrade or roll back from a known image tag.

## Current State

Cassini already has most of the raw pieces:

- `appinfo/info.xml` declares a real AppAPI ExApp named `gocassini`.
- The manifest declares the Docker image
  `ghcr.io/codemyriad/gocassini:latest`.
- The manifest declares install-time environment variables:
  - `CASSINI_TALK_RECORDING_SECRET`
  - `CASSINI_TALK_BACKEND_URL`
- The manifest declares AppAPI routes for:
  - admin control panel: `/control-panel`
  - admin operator API: `/operator/*`
  - logged-in user viewer: `/viewer`
  - logged-in published archive: `/published/*`
  - public Talk recording-backend endpoints:
    `/api/v1/welcome` and `/api/v1/room/<token>`
- `deployment/Dockerfile.exapp` builds a CPU ExApp image with the operator,
  recorder, control panel, viewer, publisher, bundled CPU model, and AppAPI
  entrypoint.
- `deployment/Dockerfile.exapp.cuda` builds a CUDA ExApp image with CUDA runtime
  libraries, CUDA sherpa/onnxruntime libraries, a fp32 Parakeet model, and
  `CASSINI_STT_DEVICE=cuda`.
- `deployment/exapp-start.sh` expects AppAPI/HaRP variables and starts both the
  FRP client and operator.
- `docs/exapp-install.md` exists, but part of it is stale: it still says CUDA
  variants are CPU-only compatibility placeholders, while the tree now contains
  a real CUDA ExApp Dockerfile.

## Main Gaps

### Install Flow Is Not Yet Productized

The AppAPI pieces exist, but the app still needs a clear, tested install path
that an administrator can follow without reading source code.

The install guide should cover:

- required Nextcloud and AppAPI versions;
- deploy daemon prerequisites;
- CPU daemon versus CUDA daemon behavior;
- required environment variables;
- persistent storage expectations;
- Talk `recording_servers` configuration;
- verification steps;
- rollback.

The guide should show both UI and OCC installation paths. The OCC path is
important for reproducible infrastructure:

```bash
php occ app_api:app:register gocassini <daemon-name> \
  --info-xml '<pinned info.xml URL or local path>' \
  --wait-finish \
  --env CASSINI_TALK_RECORDING_SECRET='<secret>' \
  --env CASSINI_TALK_BACKEND_URL='<nextcloud-url>'
```

### Release Tags Are Not Yet a Stable Contract

AppAPI's Docker deploy code tries an extended image tag when a daemon has a
compute device. If the manifest image tag is `latest`, a CUDA daemon tries:

```text
ghcr.io/codemyriad/gocassini:latest-cuda
```

before falling back to:

```text
ghcr.io/codemyriad/gocassini:latest
```

That means CUDA support depends on paired tags. For each release or blessed
commit, publish:

```text
ghcr.io/codemyriad/gocassini:<version>
ghcr.io/codemyriad/gocassini:<version>-cuda
ghcr.io/codemyriad/gocassini:sha-<shortsha>
ghcr.io/codemyriad/gocassini:sha-<shortsha>-cuda
```

Moving tags such as `latest` and `latest-cuda` are useful for quick testing, but
production installs should use version or SHA tags.

### CUDA Must Be Verifiable

A CUDA image tag alone is not enough. The administrator needs confidence that:

- the CUDA image was selected;
- AppAPI attached an NVIDIA device to the container;
- the CUDA runtime libraries can load;
- the selected transcription model can run on the GPU.

Cassini should expose this through a health or doctor surface. At minimum, it
should report:

- image flavor: CPU or CUDA;
- `COMPUTE_DEVICE`;
- `CASSINI_STT_DEVICE`;
- model id;
- whether CUDA device discovery succeeded;
- whether a tiny transcription or runtime probe succeeded.

If a CUDA install cannot use the GPU, the failure should be explicit and
actionable. Silent fallback to CPU is bad for operators because recordings can
appear to work while performance is unexpectedly poor.

### UI Discovery Is Weak

The AppAPI proxy routes exist, but a user may not know where to go after
installation.

Direct URLs are useful for smoke tests:

```text
/index.php/apps/app_api/proxy/gocassini/control-panel
/index.php/apps/app_api/proxy/gocassini/viewer
/index.php/apps/app_api/proxy/gocassini/published/...
/index.php/apps/app_api/proxy/gocassini/api/v1/welcome
```

For product use, Cassini should also add discoverable Nextcloud UI entry points:

- an admin settings link or admin navigation entry for the control panel;
- a user navigation entry for the viewer;
- clear empty states when Talk is not configured, no recordings exist, or the
  backend secret is missing.

### Talk Handoff Needs a Safe Flow

Installing the ExApp and switching Talk to use it are separate operations.

The recommended flow is:

1. Install and verify the Cassini ExApp.
2. Confirm `/api/v1/welcome` through the AppAPI proxy.
3. Configure Talk's `recording_servers` to the AppAPI proxy base URL:

   ```text
   https://<nextcloud-host>/index.php/apps/app_api/proxy/gocassini
   ```

4. Use the same shared secret in Talk and in
   `CASSINI_TALK_RECORDING_SECRET`.
5. Start and stop a test recording in a non-critical Talk room.
6. Keep the previous backend available until that test passes.

The install docs should make this two-phase model explicit. It avoids the
failure mode where the backend exists but users expect the Nextcloud app UI to
appear automatically.

## Acceptance Criteria

Cassini should count as an installable Nextcloud app when these are true:

- A fresh Nextcloud AppAPI install can register `gocassini` from a pinned
  manifest.
- The ExApp container starts without manual shell patching.
- Required environment variables are exposed in AppAPI's deploy options.
- The admin control panel loads for an administrator.
- The viewer loads for a normal logged-in user.
- The Talk welcome endpoint works through the AppAPI proxy URL.
- Talk can start and stop a recording using the AppAPI proxy URL.
- A completed recording appears in the viewer.
- CPU installs use the CPU image.
- CUDA installs use the `-cuda` image and prove GPU access.
- Versioned image tags exist for both CPU and CUDA variants.
- Upgrade instructions identify the old tag, new tag, and rollback path.
- The install docs distinguish AppAPI ExApp mode from local compose or
  standalone operator mode.

## Suggested Issue Bundle

### 1. Update ExApp Install Documentation

Rewrite `docs/exapp-install.md` so it matches the current ExApp implementation.

Include:

- CPU and CUDA image behavior;
- AppAPI UI install;
- OCC install;
- required env vars;
- Talk backend configuration;
- direct proxy URLs;
- verification and rollback.

### 2. Publish Stable CPU/CUDA Tag Pairs

Update release CI so every release publishes:

- `<version>`;
- `<version>-cuda`;
- `sha-<shortsha>`;
- `sha-<shortsha>-cuda`.

Then generate or update `appinfo/info.xml` so release installs are pinned to the
intended image tag.

### 3. Add AppAPI Install Smoke Test

Add a test path that registers the ExApp against a real or harnessed Nextcloud
AppAPI instance and verifies:

- registration completes;
- lifecycle callbacks work;
- `/api/v1/welcome` works through the proxy;
- control panel and viewer routes return HTML.

### 4. Add CUDA Runtime Probe

Add a health or doctor check that proves CUDA is actually usable from inside the
ExApp container.

The check should fail loudly when `COMPUTE_DEVICE=CUDA` or
`CASSINI_STT_DEVICE=cuda` is set but no usable NVIDIA device is available.

### 5. Add Nextcloud UI Entry Points

Add a discoverable path from the Nextcloud UI to:

- the admin control panel;
- the user viewer.

The current AppAPI proxy URLs are sufficient for testing, but not enough for a
polished app experience.

### 6. Clarify Deployment Modes

Document these as distinct modes:

- AppAPI ExApp: production Nextcloud app path.
- Local compose: developer stack for operator/control-panel/viewer work.
- Standalone operator: staging, compatibility, or rollback backend path.

This distinction prevents backend-only deployments from being mistaken for a
complete Nextcloud app install.

## References

- Nextcloud AppAPI and External Apps:
  https://docs.nextcloud.com/server/stable/admin_manual/exapps_management/AppAPIAndExternalApps.html
- Nextcloud Managing ExApps:
  https://docs.nextcloud.com/server/stable/admin_manual/exapps_management/ManagingExApps.html
- Nextcloud Advanced Deploy Options:
  https://docs.nextcloud.com/server/stable/admin_manual/exapps_management/AdvancedDeployOptions.html
- Nextcloud Managing Deploy Daemons:
  https://docs.nextcloud.com/server/stable/admin_manual/exapps_management/ManagingDeployDaemons.html
- AppAPI Docker deployment behavior:
  https://raw.githubusercontent.com/nextcloud/app_api/main/lib/DeployActions/DockerActions.php
