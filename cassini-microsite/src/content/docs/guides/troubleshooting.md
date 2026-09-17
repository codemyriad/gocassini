---
title: Troubleshooting
description: The install and recording problems seen in practice, what each one means, and the command that clears it.
source: docs/exapp-install.md, docs/exapp-nextcloud-recordings-permissions.md
copied: "2026-09-17"
---

Most Cassini problems are one of three things: the reverse proxy is not sending
`/exapps/*` to HaRP, the recordings storage is not ready, or the compute device
the daemon asked for is not there. Each says so, in a different place. This page
maps the symptom to the answer.

## Where to look first

Two endpoints answer almost everything.

```bash
# ADMIN — the diagnosis. Answers 503 when something is broken, so monitor this one.
curl -fsS -u admin:<app-password> \
  https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/status

# USER — "is Cassini set up", for anyone with an account. Always answers 200.
curl -sS -u alice:<app-password> \
  https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/operator/setup
```

`/operator/status` reports the app version, the transcription device and whether
it is usable, whether the Talk secrets are configured (never the values), the
storage state, and DB health. `/operator/setup` carries `ok`, a `state`, the
audience and a one-sentence cause, and nothing an unprivileged reader should not
see.

In the app itself, an administrator gets the cause in words with **Try again**
and **Show details**; the disclosure behind **Show details** carries the step, the
paths and the `occ` lines that fix it. Everybody else gets the same first
sentence and no buttons.

## Install

**The app reports `[enabled]` but no navigation icon ever appears.** The reverse
proxy is not routing `/exapps/*` to HaRP, so AppAPI's `PUT /enabled?enabled=1`
callback never arrives and Cassini never registers its navigation entries. Fix
the route — see
[Step 1b](/docs/getting-started/install#step-1b--route-exapps-to-harp-at-your-reverse-proxy).
Do not test it with an unauthenticated `curl https://…/exapps/…`: that returns
502 whether the route is right or wrong. Check `nextcloud.log` and look for
`PUT /enabled` in the container log instead.

**`occ app_api:app:register` hangs until `--wait-finish` times out.** The
container could not call back into Nextcloud. Check that `NEXTCLOUD_URL` resolves
from inside the container:

```bash
docker exec nc_app_gocassini sh -lc \
  'curl -k -s -o /dev/null -w "%{http_code}\n" "$NEXTCLOUD_URL/status.php"'
```

**A variable you passed with `--env` has no effect.** AppAPI only passes
variables the manifest declares under `<environment-variables>`, and silently
drops the rest. Check the spelling against the manifest. Deploy environment is
also creation-time only: changing it on a running install needs a redeploy, not
an update.

## Recording

**The Record button in Talk answers "The recording failed".** Either Talk cannot
reach Cassini, or a prerequisite of the audience in force is missing, so the call
would be captured and then not publishable. Open Cassini as an administrator; the
notice names the cause, and **Show details** carries the missing thing and its
command.

**Recording stays disabled and the log says
`talk_signaling_internal_secret_set -> false`.** The signalling internal secret is
not set. It is the one value Cassini cannot generate for itself — see
[Finding the signalling internal secret](/docs/getting-started/install#finding-the-signalling-internal-secret).

**Recordings publish, but Talk's own recording backend is still the old one.**
The handoff is one `spreed` setting. Re-check `occ config:app:get spreed
recording_servers` against the `recording_backend_url` on `/operator/status`.

## Storage and permissions

These come from `recordings_access` on `/operator/status`.

| Symptom | Likely cause | Fix |
|---|---|---|
| `/operator/status` answers 503 | The storage the selected audience needs is not ready | Read `recordings_access.state` and `.step`. `unavailable` names a thing to install or set and carries the command in `.detail`; `degraded` means a call failed |
| `state=unavailable`, `step=owner_account` | The `cassini` service account does not exist | `occ group:add cassini` and `occ user:add --group=cassini cassini`, then re-enable Cassini |
| `state=unavailable`, `step=app_missing:<id>` | That native app is not enabled | `occ app:install <id> && occ app:enable <id>`, then re-enable Cassini |
| `state=unavailable`, `step=administrator` | No probed account is an administrator Cassini may act as | Set `CASSINI_NC_ADMIN_USER` to one, then re-enable Cassini |
| `state=unavailable`, `step=group_folder` | There is no `Cassini` Team folder | Pick **Meeting participants** in **Operator › Settings › Who can see recordings**, which creates and maps it as you; or run the `occ` recipe in [Who can see a recording](/docs/guides/who-can-see-a-recording) |
| `state=unavailable`, `step=mode_mismatch:default_root_shadowed` | A Team folder is mounted at `CassiniNoACL`, which is where the "anyone with an account" audience keeps recordings | `occ groupfolders:list`, then unmap it — or switch the audience to meeting participants if that is what this instance was meant to be |
| `state=unknown` | No preflight has completed in this process, usually just after a restart | Look again after a few seconds. If it sticks, disable and re-enable Cassini |
| Publishes fail with "the recordings storage is not ready" | Deliberate: writing somewhere the read path is not looking would leave recordings nobody can open | Fix the state above |
| The settings section says "A switch didn't finish" | A switch stopped between copying and tidying up. The archive is complete at the audience shown; the other root holds a copy nothing reads | Press **Resume** |
| Cassini says N recordings aren't shown yet | The instance is settled, but the root the audience in force does not name still has an archive | Press **Move them in**, or leave them — they are not at risk |
| A granted user sees an empty list | Their account cannot traverse the Team folder | Confirm with `occ groupfolders:list` that the folder has advanced ACL and the `everyone: read` mount, and that `occ user:info <user>` reports `everyone` |
| Everyone sees a private recording | The file lacks an explicit `everyone` deny, or advanced ACLs were disabled | Re-enable Cassini to re-run protection, then inspect the file's Advanced permissions |
| The viewer returns 502 | Nextcloud Files is unreachable from the container | Check container-to-Nextcloud connectivity and the service account |

Catalog scans fail closed: a mis-configured instance degrades to "no meetings",
never to "everyone's meetings".

## Transcription

**Jobs go straight to `build/blocked` on a CUDA install.** The daemon asked for
CUDA and got the plain image, or the container cannot see the GPU. There is no
CPU fallback: recording finishes and the build refuses rather than decoding on
the CPU silently. Check `docker exec nc_app_gocassini nvidia-smi` and that
`/operator/status` shows `"device": "cuda"` with `"device_usable": true`. Install
the matching `-cuda` image, then use **Rerun**.

**Jobs stay queued with a retry time.** On a CUDA image, temporary RAM or VRAM
pressure is retried with exponential backoff. After enough unsuccessful
deferrals the job moves to `build/blocked`; restore capacity and use **Rerun**.

**A build is blocked naming a model it cannot download.**
`CASSINI_DISALLOW_MODEL_DOWNLOAD` is set and the selected quality tier is not the
one this image bundles. Select the bundled tier, or allow the download. See
[CPU or GPU](/docs/guides/cpu-or-gpu).

**A meeting published with no summary.** That is normal on a deployment with no
language-model endpoint. Check `features.summaries` on `/operator/setup`: `false`
means no transcript is being sent for a summary at all. See
[AI providers](/docs/guides/ai-providers).

## From the CLI

**`Nextcloud rejected the credentials`** — the app password is wrong, revoked, or
belongs to a different account.

**`meetings=0`** — either the account has no readable recordings, or the
recordings storage is not set up. Check the same account in the app in a browser:
if it sees nothing there either, this is a provisioning question.

**`no recording you can read at that id`** — the id is absent from that account's
catalogue. It may not exist, or it may belong to somebody else; these are
answered identically on purpose.

More, including the redirect and host guards, is in
[Agent access via the CLI](/docs/guides/agent-access).
