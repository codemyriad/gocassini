# Get from installation to a verified recording

Cassini → Setup checks recording storage, speech processing, Talk connectivity,
HPB authentication, and the recording handoff. Existing recordings remain
available when the recording connection needs attention.

## Before installation

The Nextcloud ExApp requires Nextcloud 32–35, administrator access, and a working
AppAPI deploy daemon. Published images currently target Linux amd64. CPU
transcription is supported; NVIDIA/CUDA is optional. Live Talk capture requires
standalone signaling with HPB media support and its internal client secret.
Installing AppAPI alone does not configure its deploy daemon.

On AIO, enable HaRP and Talk in the AIO management interface, start the
containers, and use Administration → AppAPI → Test deploy. Reuse the AIO daemon;
do not install a second HaRP alongside it just to follow a generic guide.
For custom installations, follow [Nextcloud's AppAPI guide](https://docs.nextcloud.com/server/latest/admin_manual/exapps_management/AppAPIAndExternalApps.html).
A hosted account may require provider assistance with these server components.

Install the release following [the production guide](exapp-install.md). If only
an unstable release is offered, deliberately choose that channel or a pinned
manifest. Check the App Store for current release availability.

When upgrading an existing registration to this feature, refresh its manifest
routes using the documented [update procedure](exapp-update-constraints.md).
The new ADMIN routes are `/operator/readiness`, `/operator/readiness/check`, and
`/operator/talk/setup`. Deploying only a new container image may leave these
routes inaccessible until registration metadata is refreshed.

## Complete Setup

1. Choose a storage mode and let the existing storage workflow create the
   service account and, if selected, the access-controlled Team folder.
   Cassini does not choose who may read recordings for you.
2. Open **Talk authentication**. Supply the signaling server's `[clients]
   internalsecret. An AIO host administrator retrieves it with
   `docker exec nextcloud-aio-talk printenv INTERNAL_SECRET`. This is different
   from the recording-backend secret, which Cassini generates itself.
3. Choose a dedicated **Test room** on this Nextcloud. The URL is stored so
   Cassini can recheck after restarting. Public links and `/index.php/call/` links
   are supported even when AppAPI uses an internal hostname. Cassini extracts
   the room token and always probes its deployment-configured Talk backend
   (`CASSINI_TALK_BACKEND_URL`, falling back to `NEXTCLOUD_URL`); the pasted
   hostname is retained as the public identity in the HPB handshake, but is never
   dialed by Cassini. Use a room from this instance.
   The connection check authenticates
   but never joins the call or records media.
4. Open **Connect Talk**. Identify the installation method and who can change
   its server configuration. Use the administrator request if you lack host
   access. For a confirmed command path, review the advanced commands and
   acknowledge that they replace the previous recorder. They save the previous
   `recording_servers` value to a private, uniquely named backup and prompt for
   an administrator app password. Keep that backup for rollback.
5. Press **Check again**. A rejected recording credential means the handoff
   needs checking; it is not evidence that HPB is absent. Missing permissions,
   network errors, unavailable rooms, and absent HPB have distinct findings.

The internal secret is saved atomically with mode 0600 in
`recording-setup.json`, next to the operator database on its persistent volume.
An environment-supplied `CASSINI_TALK_SIGNALING_INTERNAL_SECRET` takes precedence
and is edited in deployment configuration. API responses never return this
secret. A saved change affects new recorder processes, not active recordings.
A damaged configuration file is reported and must be restored, not silently
replaced. Include this file in your normal volume backup.

## Test the full path

Press **Prepare test**, open the test room, start a call and use **Talk's**
Start recording action. Speak for about 20 seconds and stop recording. Setup
follows the first matching recording started through Talk after the test was
prepared. A job started directly through the operator does not count.

After publishing finishes, open the recording and verify the audio and
transcript. Confirm playback in Setup. This confirmation is a human observation;
Cassini does not pretend that producing a file proves audible playback.

A failed/blocked job is shown with its stage. Inspect it in Operator and repair
that stage, then rerun it or prepare a new test. If Setup keeps waiting for Talk,
check the selected room, moderator permission, and recording-backend handoff.
Do not prepare another test while your intended test is already recording.

Live check results expire after five minutes and are discarded on restart.
The last test's playback confirmation is retained as historical evidence.
**Check again** refreshes outbound connectivity and storage. It cannot verify
that Talk can still call Cassini: the expired incoming-connection check offers
**Test a recording**, while the previous playback confirmation remains visible. An expired result is
**Not verified**, never a green pass or a permanent veto on recording.

## AIO restart persistence

AIO manages Talk's recording settings independently of Cassini. Its startup
script sets `spreed recording_servers` to the AIO recorder when
`TALK_RECORDING_ENABLED=yes`. With the recorder disabled, it deletes the setting
when `REMOVE_DISABLED_APPS=yes`. See the upstream
[startup script](https://github.com/nextcloud/all-in-one/blob/main/Containers/nextcloud/entrypoint.sh)
and [container configuration](https://github.com/nextcloud/all-in-one/blob/main/php/containers.json).

To preserve a custom recording backend:

1. Keep AIO's **Talk** component enabled but disable **Talk Recording**.
2. Set `NEXTCLOUD_KEEP_DISABLED_APPS=true` on the AIO **mastercontainer**, using
   its persistent Compose/environment configuration, and recreate the
   mastercontainer using AIO's normal procedure. Start/recreate the managed
   containers through AIO so the change reaches Nextcloud. This is supported by
   AIO's [configuration manager](https://github.com/nextcloud/all-in-one/blob/main/php/src/Data/ConfigurationManager.php).
   For manual-install deployments, set `REMOVE_DISABLED_APPS=no` directly in
   their managed Nextcloud container configuration instead.
3. Verify the effective values without displaying secrets:

   ```bash
   docker exec nextcloud-aio-nextcloud sh -c '
     test "$TALK_RECORDING_ENABLED" != yes && test "$REMOVE_DISABLED_APPS" != yes
   '
   ```

   Exit zero means AIO will neither select its recorder nor delete a custom
   recording backend under the inspected startup logic. The generated AIO
   handoff commands check these conditions before making changes.
4. Apply **Connect Talk** and run the test recording.

Keeping disabled apps changes AIO's cleanup behavior for other optional apps
as well: their Nextcloud apps are not automatically removed when their
components are disabled. The AIO mastercontainer maps the keep-disabled flag to
an empty `REMOVE_DISABLED_APPS`; manual installations typically use `no`. Both
are accepted by the check above.

After the first successful test, restart using AIO's normal controls, re-open
Setup, run **Check again**, and prepare a new test through Talk. This checks both
saved Cassini configuration and AIO's ownership of the Talk setting. Cassini
never automatically replaces a recorder that an administrator may have selected.

For rollback on AIO, restore the backup made by Connect Talk:

```bash
# Substitute the exact backup filename printed during handoff.
previous=$(cat ./cassini-recording-backend.XXXXXX)
if [ -n "$previous" ]; then
  docker exec -u www-data nextcloud-aio-nextcloud php occ config:app:set spreed recording_servers --value="$previous"
else
  docker exec -u www-data nextcloud-aio-nextcloud php occ config:app:delete spreed recording_servers
fi
unset previous
```

The generated handoff also enables `call_recording`. If it was previously
disabled, restore that choice through Talk's administration settings.

## API and testing

- `GET /operator/readiness`: current local checks and cached network evidence;
  returns 200 even when setup needs action, with `Cache-Control: no-store`.
- `POST /operator/readiness/check`: bounded, coalesced checks, using the recorder's
  `cassini talk-check` command. That command shares discovery and hello/auth
  protocol implementations with live recording and produces redacted JSON.
- `PUT /operator/talk/setup`: save an internal secret or test room, prepare a
  test, or confirm playback of the matching published job. ADMIN only.
- `GET /operator/setup`: adds only a coarse `recording_state` for ordinary
  users. No private room URL, secret source, configuration detail or job ID.

Basic health and archive access are independent of recording readiness. Missing
recording credentials produce an actionable recording refusal, not a container
restart loop. CPU readiness follows the same device/model policy as processing.

Tests cover HTTP/WebSocket authentication with no room joins, missing HPB,
incorrect credentials, secret persistence/redaction, trusted diagnostic targets,
coalescing and expiry, configuration edits, Talk-only test selection, publication
and playback confirmation, and restart invalidation of live evidence.

The installed-stack check is `IMAGE_REF=<built-image>
./harness/bin/ci-e2e-recording-readiness.sh`. It runs two real Talk/CPU/publish
cycles separated by a Nextcloud and ExApp restart, verifies the new ADMIN route
boundary and current HPB authentication, and asserts that no automated check
claims human playback confirmation. It requires a dedicated Docker environment
because the existing harness uses fixed HaRP/ExApp names. The normal installed
CPU CI job runs this check. Its Nextcloud restart is not an AIO mastercontainer
restart; validate the AIO-specific persistence settings on your deployment too.

## Environment-aware repair guidance

Each repair form opens beneath its check. Cassini uses its live diagnostics for
capability findings, but does not infer Nextcloud's installation method from its
own container or assume that an administrator has host access. The form starts
with both installation method and access unknown.

Administrators can identify AIO, Docker, Compose, a host/archive installation,
Snap, or another/unknown method. Access is a separate choice: host terminal,
container/appliance console, provider/another administrator, or unknown. These
are user-supplied choices, not automatic detection results. They stay in page
memory and are not saved as deployment facts.

For a confirmed host terminal and a supported command path, Connect Talk offers
advanced commands. Docker, Compose and host installations require explicit
service/container, web-server user and absolute occ-path details as applicable.
The instructions identify the Nextcloud host as the target even when Cassini runs
elsewhere. The script checks its tools and Nextcloud before writing configuration;
copying requires acknowledgment that the current recorder will be replaced.
Changing installation details clears that acknowledgment.

Provider-managed, console-only, unknown and other installations receive a safe
administrator request and guidance on what information to obtain. Each request
uses known check labels and states; it excludes raw messages, room URLs, job IDs
and credentials. Secret-related requests ask the administrator to configure the
value securely rather than reply with it in a support ticket. These requests are
copyable text; Cassini does not send them automatically.

The command paths are covered by generation, quoting, gating and browser tests.
They have not all been validated on live platform deployments. A Snap occ wrapper
is not proof of ExApp compatibility; a provider's actual policies and a remote
execution host may determine what is possible. Podman, Kubernetes and vendor NAS
packages need a verified platform-specific path before generated commands can
be offered for them.

Automatic environment classification from authoritative deployment metadata and
an in-app Talk configuration operation remain future work. Neither is claimed
by this UI. The [pre-install guide](before-installing.md) explains eligibility
before Cassini can run, including actual AppAPI test deployment, recorder-target
architecture, HPB and provider involvement.
