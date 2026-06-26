---
shaping: true
---

# Spike X3 — Nextcloud Talk Config, AppAPI Env Setup, and Secret Flow

## Context

User answer for Q1: include D-403 in D-395, but first understand the concrete Nextcloud shape:

- how Talk recording secret configuration flows through Nextcloud;
- how AppAPI turns ExApp deploy options into container env;
- whether changing a secret in Nextcloud updates an already deployed ExApp;
- whether the secret is configurable at all after deployment.

This spike is exploration only. It does not implement changes.

## Sources inspected

Local repo:

- `appinfo/info.xml`
- `harness/bin/bootstrap.sh`
- `harness/bin/manual-test-setup.sh`
- `harness/bin/common.sh`
- `harness/compose.yml`
- `cassini-operator/internal/operator/talk_backend.go`
- `cassini-operator/internal/operator/run.go`
- `cassini-operator/internal/operator/record_runtime.go`
- `cassini-operator/internal/operator/status.go`
- `cassini-go-recorder/internal/talk/recorder.go`
- `cassini-go-recorder/internal/nextcloud/ocs_client.go`
- `cassini-go-recorder/internal/nextcloud/recording_auth.go`

`dev-vm` read-only source inspection from running Nextcloud/AppAPI/Talk containers:

- Nextcloud `33.0.5`
- AppAPI `33.0.0`
- `/var/www/html/apps/app_api/lib/Command/ExApp/Register.php`
- `/var/www/html/apps/app_api/lib/Command/ExApp/Update.php`
- `/var/www/html/apps/app_api/lib/DeployActions/DockerActions.php`
- `/var/www/html/apps/app_api/lib/Service/ExAppService.php`
- `/var/www/html/apps/app_api/lib/Service/ExAppDeployOptionsService.php`
- `/var/www/html/apps/app_api/lib/Command/ExAppConfig/SetConfig.php`
- `/var/www/html/apps/app_api/lib/Controller/ExAppsPageController.php`
- `/var/www/html/custom_apps/spreed/lib/Recording/BackendNotifier.php`
- `/var/www/html/custom_apps/spreed/lib/Config.php`
- `/var/www/html/custom_apps/spreed/lib/Controller/RecordingController.php`

## Data flow: Talk record button to Cassini job

```text
Admin configures Talk recording backend
  spreed.recording_servers = { servers: [{ server, verify }], secret }
  spreed.call_recording = yes

User/admin starts recording in Talk
  Talk reads recording_servers from Nextcloud app config
  Talk POSTs { type: start, ... } to <server>/api/v1/room/<token>
  Talk signs body with recording_servers.secret
  Talk includes Talk-Recording-Backend = Nextcloud absolute URL

Nextcloud AppAPI proxy
  info.xml marks Cassini /api/v1/welcome and /api/v1/room/<token> as PUBLIC
  AppAPI forwards request to HaRP / installed ExApp container

Cassini operator
  validates Talk HMAC using CASSINI_TALK_RECORDING_SECRET
  creates a job with TalkAuthMode = hpb-internal
  uses Talk-Recording-Backend as public BaseURL
  uses CASSINI_TALK_BACKEND_URL override only if configured

cassini record child process
  inherits the ExApp container env from the operator process
  reads CASSINI_TALK_RECORDING_SECRET
  reads CASSINI_TALK_SIGNALING_INTERNAL_SECRET
  uses recording secret to fetch Talk signaling settings via recording-auth headers
  uses signaling internal secret to authenticate to standalone signaling/HPB as internal client

Cassini operator delivery/publish
  sends started/stopped/failed callbacks to Nextcloud with Talk HMAC
  uploads recording to Talk store endpoint with Talk HMAC
  builds/transcribes/publishes into APP_PERSISTENT_STORAGE-backed paths
  viewer serves /published/* through installed ExApp route
```

## Talk recording secret in Nextcloud

`spreed` stores recording backend config as JSON app config:

```bash
occ config:app:set spreed recording_servers \
  --value='{"servers":[{"server":"<backend-base>","verify":false}],"secret":"<recording-secret>"}'
occ config:app:set spreed call_recording --value=yes
```

Talk source inspection:

- `Config::getRecordingServers()` reads `spreed.recording_servers` and returns `servers`.
- `Config::getRecordingSecret()` reads the same JSON and returns `secret`.
- `Recording\BackendNotifier::backendRequest()`:
  - picks the first configured recording server;
  - appends `/api/v1/room/<room-token>`;
  - signs `random + body` with `getRecordingSecret()`;
  - sets `Talk-Recording-Random`;
  - sets `Talk-Recording-Checksum`;
  - sets `Talk-Recording-Backend` to `$urlGenerator->getAbsoluteURL('')`;
  - sends the request server-side with local-address access allowed.
- `RecordingController::validateBackendRequest()` uses the same `getRecordingSecret()` for backend callbacks/uploads.

Implication:

- The Talk recording secret is Nextcloud/Talk app config.
- It authenticates both directions of the recording-backend protocol:
  - Talk → Cassini start/stop requests;
  - Cassini → Talk started/stopped/failed callbacks and upload.

## HPB internal signaling secret

`CASSINI_TALK_SIGNALING_INTERNAL_SECRET` is **not** the Talk recording backend secret.

It is the standalone signaling/HPB internal-client secret (`internalsecret` in signaling config). Cassini's HPB-internal recorder uses it for the signaling `hello` as an internal client after it has fetched recording-authorized signaling settings from Nextcloud.

Cassini source inspection:

- `cassini-go-recorder/internal/talk/recorder.go`
  - loads `CASSINI_TALK_RECORDING_SECRET` and `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` from process env;
  - `validateInternalBootstrapConfig()` rejects `hpb-internal` if either is missing;
  - `bootstrapInternalHPB()` uses recording secret to fetch signaling settings, then uses internal signaling secret for HPB auth.

Implication:

- Updating Talk's `recording_servers.secret` does not and cannot update the HPB internal signaling secret.
- Production admins need two matching configurations:
  - Talk `recording_servers.secret` ↔ Cassini `CASSINI_TALK_RECORDING_SECRET`;
  - signaling server `internalsecret` ↔ Cassini `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`.

## AppAPI env setup

`occ app_api:app:register` accepts repeated `--env KEY=VALUE` options:

```bash
php occ app_api:app:register gocassini <daemon> \
  --info-xml /tmp/gocassini-info.xml \
  --env CASSINI_TALK_RECORDING_SECRET=... \
  --env CASSINI_TALK_SIGNALING_INTERNAL_SECRET=... \
  --wait-finish
```

AppAPI source inspection:

- `Register.php` parses repeated `--env` into `deployOptions['environment_variables']`.
- `ExAppService::getAppInfo()` parses `info.xml` `<environment-variables>` and builds an allow-list keyed by declared variable name.
- Only deploy options whose keys are in that allow-list override a value.
- Env variables whose resulting value is empty are filtered out.
- `DockerActions::buildDeployEnvs()` creates the final container env list from:
  - AppAPI automatic envs (`AA_VERSION`, `APP_SECRET`, `APP_ID`, `APP_VERSION`, `APP_HOST`, `APP_PORT`, `APP_PERSISTENT_STORAGE`, `NEXTCLOUD_URL`, `COMPUTE_DEVICE`, HaRP envs, etc.);
  - declared/non-empty `info.xml` environment variables with admin-supplied values.

Implication:

- Declaring `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` in `info.xml` is necessary before AppAPI can pass it into the container.
- Passing `--env CASSINI_TALK_SIGNALING_INTERNAL_SECRET=...` against the current manifest is insufficient because AppAPI drops undeclared keys.
- This confirms D-403 is part of the D-395 fix.

## Does changing Nextcloud Talk secret update the deployed ExApp?

No.

Changing:

```bash
occ config:app:set spreed recording_servers --value=...
```

updates only Nextcloud/Talk app config. It does not mutate the environment of a running ExApp container, does not update AppAPI deploy options, and does not restart/recreate Cassini.

Consequences:

- If Talk's `recording_servers.secret` changes but `CASSINI_TALK_RECORDING_SECRET` in the ExApp does not, Talk → Cassini HMAC validation fails and/or Cassini → Talk callbacks/uploads fail.
- If Cassini's ExApp env changes but Talk config does not, the same protocol mismatch occurs in the opposite direction.
- Rotation must update both sides and redeploy/recreate the ExApp container.

## Is the ExApp env configurable after deployment?

Partially, but not live.

Observed AppAPI behavior:

- On initial install/register:
  - `--env` options or web UI deploy options are applied to declared manifest variables.
  - AppAPI stores deploy options after successful deployment.
- On app update:
  - `app_api:app:update` has no `--env` option.
  - it reuses previously stored deploy options;
  - it redeploys only as part of an actual version update path;
  - newly declared env vars receive no value unless a stored deploy option already exists for them.
- On enabling an already registered app:
  - AppAPI enables it but does not redeploy the container with new env values.
- `app_api:app:config:set` is a separate ExApp app-config store accessible through AppAPI APIs; it does not become process env and Cassini does not currently read it.

Practical implications:

- For an existing pre-D-395 production install, simply upgrading to a manifest that declares `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` may not be enough, because there is no previously stored deploy option value for that newly declared variable.
- The safe production procedure is reinstall/re-register/redeploy with matching env values while preserving data.
- For the local harness, default setup can use `app_api:app:register --test-deploy-mode` with all required `--env` values on every run. AppAPI's test-deploy path unregisters/re-registers for iteration and Docker/HaRP deployment recreates the container. Data removal is not requested, so persistent storage should remain unless explicitly wiped.

## Communication and storage model implications

Q2 preference: keep the current storage model, but configure services so they can communicate and the produced deployment appears in the viewer. If that cannot be done without breaking the model, stop and open a separate task.

This spike supports keeping the current model:

- `APP_PERSISTENT_STORAGE` is AppAPI-managed and injected as container env.
- Cassini already redirects default DB/work/site roots under `APP_PERSISTENT_STORAGE` for ExApp deploys.
- The viewer/control-panel/published routes are served by the same ExApp operator container and AppAPI proxy.
- The missing pieces are env/routing/validation, not necessarily storage redesign.

Critical communication knobs:

- `spreed.recording_servers.server` must be the URL Talk can reach for Cassini's recording backend.
- `Talk-Recording-Backend` must resolve from Cassini for callbacks/uploads, or `CASSINI_TALK_BACKEND_URL` must override it.
- `NEXTCLOUD_URL` injected by AppAPI must resolve from Cassini for AppAPI-internal callbacks/initialization.
- Browser-facing viewer URL and container-facing callback URL may be different in the harness; that is acceptable as long as both route correctly.

Stop condition:

- If installed-ExApp validation can record/build/publish but cannot show the result in the viewer without changing the AppAPI persistent-storage/publication model, stop D-395 execution and open a new storage/viewer task.

## Decisions from user answers

| Question | Decision |
|---|---|
| Include D-403? | Yes, include it after this context exploration. |
| Storage model? | Keep current model; configure communication; stop if that requires model breakage. |
| Harness install behavior? | Installing the app should be the default when starting the harness. Reinstallation for code updates is desirable and can be included if straightforward. |
| VM path? | Use `/home/ubuntu/dev/workspace`. |
| Archive preservation? | Run two separate recording jobs. |

## Resulting plan changes

- D-395 Slice 1 remains manifest/status parity, but docs must explain that declaration is necessary for AppAPI deploy options and not a live config bridge from Nextcloud.
- Harness setup should install by default, probably with `--test-deploy-mode` to make repeated local runs work.
- Production docs must include a secret-rotation/redeploy sequence:
  1. pause recordings;
  2. update Talk `recording_servers.secret`;
  3. update/redeploy Cassini ExApp env with the same `CASSINI_TALK_RECORDING_SECRET`;
  4. ensure `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` matches signaling `internalsecret`;
  5. verify status and run a controlled recording.
- Validation should run two independent private 1:1 recording jobs and assert both remain visible in the viewer/catalog.
