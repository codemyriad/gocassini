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
sharing mode and a one-sentence cause, and nothing an unprivileged reader should not
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
[Route `/exapps/*` to HaRP](/docs/getting-started/install#route-exapps-to-harp-at-your-reverse-proxy).
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
reach Cassini, or the recordings account or file sharing is not ready, so the
call would be captured and then not publishable. Open Cassini as an administrator; the
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
| `/operator/status` answers 503 | Recordings cannot be published or served yet | Read `recordings_access.state` and `.step`. `unavailable` names a thing to create or enable; `degraded` means a call failed |
| `state=unavailable`, `step=owner_account` | The `cassini` service account does not exist | Press **Create recordings account** in **Operator › Settings › Who can see recordings**, or run `occ group:add cassini` and `occ user:add --group=cassini cassini`, then re-enable Cassini |
| `state=unavailable`, `step=sharing_api` | Nextcloud file sharing is unavailable to the `cassini` account | Check that the Files sharing app is enabled and that sharing is allowed under **Administration settings › Sharing** |
| `state=degraded`, `step=private_archive` | Cassini could not confirm its `CassiniRecordings` folder is private and writable | Check that nothing is mounted or shared at `CassiniRecordings` in the `cassini` account's Files, then press **Check again** |
| `state=degraded`, `step=nextcloud_probe` | Nextcloud did not answer the setup check | Check container-to-Nextcloud connectivity, then press **Check again** |
| `state=unknown` | No check has completed in this process, usually just after a restart | Look again after a few seconds. If it sticks, disable and re-enable Cassini |
| Publishes fail with "Nextcloud recording shares are not ready" | Deliberate: a recording that cannot be shared would be one nobody can open | Fix the state above |
| `recordings_access.warning` names a person | One recipient's share was refused. Everyone else still has access | Share the file with that person from Files, signed in as `cassini` |
| Someone who was in the room sees an empty list | They were a guest, email or federated participant, or the room was captured before they were added | Share the recording with them from Files, signed in as `cassini` |
| The viewer returns 502 | Nextcloud Files is unreachable from the container | Check container-to-Nextcloud connectivity and the service account |

Listings fail closed: a misconfigured instance degrades to "no meetings",
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
