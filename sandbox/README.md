# Cassini Sandbox (Nextcloud AIO)

The demo sandbox at **https://demo.nextcloud.codemyriad.io** runs on **Nextcloud
All-in-One (AIO)** — the substrate a real admin runs, and the one
[cloud.codemyriad.io](https://cloud.codemyriad.io) uses. This is deliberate: the
sandbox should mirror a real deployment installing Cassini, not the CI/dev
harness. AIO owns Nextcloud, Postgres, the **Talk HPB** (signaling + Janus +
TURN, as the `talk` optional container), backups, and the reverse-proxy apache.

> The CI/dev **harness** (`harness/`) is a separate thing — a throwaway
> docker-compose stack for tests, with committed dev creds and test-isms. The
> sandbox no longer reuses any of it (this is what [D-515] fixed). Don't wire the
> two back together.

The repo's contribution to the sandbox is one script, **`wire-cassini.sh`**,
which installs/updates Cassini on top of an already-provisioned AIO. Everything
else here is one-time host setup, documented below.

## Architecture

```
browser ──443──▶ Caddy (host) ──/exapps/*──▶ HaRP :8780 ──frp──▶ Cassini ExApp
                      │                                   (nc_app_gocassini)
                      └──everything else──▶ AIO apache :11000 ──▶ Nextcloud + Talk HPB
Talk call media ──3478/tcp+udp──▶ AIO Talk (signaling + Janus + TURN)
```

### Why a manual HaRP daemon

Cassini deploys as a Nextcloud **AppAPI ExApp**, which needs a **deploy daemon**.
The recommended daemon is **HaRP**. AIO does not *yet* expose a HaRP container in
its UI (the AppAPI/HaRP integration is gated behind a newer AIO/Talk bundle), so
`wire-cassini.sh` runs HaRP itself next to AIO and points the host reverse proxy's
`/exapps/*` at it. **When AIO ships the HaRP container**, this collapses to
"tick the HaRP box in AIO" and Cassini installs one-click from the store — the
true [D-449] goal. Until then this script is the reproducible path.

## One-time host setup

### 1. Firewall

Open, at the network firewall (e.g. Hetzner Cloud Console → Firewalls):

| Port | Proto | Purpose |
|---|---|---|
| 22 | TCP | SSH |
| 80, 443 | TCP | Nextcloud / ACME |
| **3478** | **TCP + UDP** | **Talk call media (STUN/TURN/relay).** AIO routes *all* Talk media through this one port. Without it, calls connect at signaling but exchange **no audio/video**, and recordings capture no media. |

(The old compose sandbox used 13479 / 20000 / 49160 — those are obsolete; remove them.)

### 2. Nextcloud AIO (reverse-proxy mode, behind Caddy)

```bash
docker run -d \
  --init --sig-proxy=false \
  --name nextcloud-aio-mastercontainer \
  --restart always \
  --publish 8080:8080 \
  --env APACHE_PORT=11000 \
  --env APACHE_IP_BINDING=127.0.0.1 \
  --env SKIP_DOMAIN_VALIDATION=false \
  --env AIO_DISABLE_BACKUP_SECTION=true \
  --volume nextcloud_aio_mastercontainer:/mnt/docker-aio-config \
  --volume /var/run/docker.sock:/var/run/docker.sock:ro \
  ghcr.io/nextcloud-releases/all-in-one:latest
```

Then open the AIO interface over an SSH tunnel (don't expose :8080 publicly):

```bash
ssh -L 8080:localhost:8080 <sandbox-host>
# browse https://localhost:8080, note the passphrase
```

In the wizard: set the domain `demo.nextcloud.codemyriad.io`, then on the
optional-containers page **enable Talk** (and Talk recording if listed), and
**Download and start containers**. Record the generated admin password (or let
`wire-cassini.sh` reset it — see below).

### 3. Caddy

Route `/exapps/*` to HaRP and everything else to AIO's apache:

```caddyfile
demo.nextcloud.codemyriad.io:443 {
    encode gzip zstd

    handle /exapps/* {
        reverse_proxy 127.0.0.1:8780
    }

    handle {
        reverse_proxy 127.0.0.1:11000
    }
}
```

`sudo caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile && sudo systemctl reload caddy`

## Install / update Cassini

```bash
cp sandbox/env.example sandbox/.env   # first time; edit for this host
sandbox/wire-cassini.sh               # install latest App Store release (default)
```

The script is **idempotent** — re-run it to update Cassini or reconcile config. It:

1. enables AppAPI;
2. runs + registers the HaRP daemon (`harp_aio`);
3. resolves the latest published release from the App Store catalog (or a given
   `--image`), pre-pulls it, and registers the `gocassini` ExApp;
4. installs Cassini the zero-config way (D-447): **no recording secret is set**,
   so Cassini self-generates and persists one; the script then reads it back from
   the ExApp and points `spreed`'s recording backend at Cassini. Only AIO's Talk
   **`INTERNAL_SECRET`** (as `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`, for
   HPB-internal recording — it cannot be generated) is injected, read from the
   AIO Talk container;
5. resets the admin password to `SANDBOX_NC_ADMIN_PASSWORD` if set.

Options:

```bash
sandbox/wire-cassini.sh --image ghcr.io/codemyriad/gocassini:sha-<shortsha>  # test a specific build
sandbox/wire-cassini.sh --register-only                                      # re-register only
```

### Source capture on the sandbox

Participant source-audio capture ([docs/source-audio-capture.md](../docs/source-audio-capture.md))
is **on** on every deploy from this branch — that is the point of the branch —
and both of its switches are passed to AppAPI explicitly each time, so whatever
this deploy says is what the host ends up with.

```bash
sandbox/wire-cassini.sh --image ghcr.io/codemyriad/gocassini:sha-<shortsha>   # collect and ingest
CASSINI_SOURCE_AUDIO_INGEST=0 sandbox/wire-cassini.sh --image ghcr.io/codemyriad/gocassini:sha-<shortsha>   # collect only
CASSINI_SOURCE_CAPTURE=0 sandbox/wire-cassini.sh --image ghcr.io/codemyriad/gocassini:sha-<shortsha>        # collect nothing
```

A deploy with capture on also installs the `cassini_capture` companion app,
built around the payload read from the running ExApp container so the two are
byte-identical. That is why it needs `--image`: the companion must carry the same
version as the ExApp, and only the checkout that produced the image is guaranteed
to. A `--from-store` deploy cannot deliver the payload at all, so it registers
`CASSINI_SOURCE_CAPTURE=0` and says why — a switch answering yes with no
companion behind it is worse than one answering no, and calls still running with
a payload from an earlier deploy stop within about thirty seconds.
`CASSINI_SOURCE_CAPTURE=0` also disables an enabled companion, which is what
backing the feature out completely requires; a deploy that cannot disable it
fails rather than reporting success.

From GitHub, the `Deploy Sandbox` workflow has no inputs for either switch: pass
an `image_tag` and both are on. With capture on, every authenticated participant
of a recorded call is captured — there is nothing per participant to set up, in
the browser or anywhere else. Record a call as described under "Trying it" in the
capture document.

The HaRP shared key persists in a fixed **`/opt/cassini-aio`**
(override with `CASSINI_AIO_STATE`), **not** in the repo. It must be a single
shared path — not a per-user home — so every operator and CI use the *same* HaRP
key; otherwise a second user's run regenerates the key and breaks the daemon.
Create it once, writable by everyone who deploys (all of whom are in the `docker`
group already):

```bash
sudo install -d -g docker -m 2770 /opt/cassini-aio   # setgid: files inherit the docker group
```

## Verify

```bash
# PUBLIC Talk welcome (proves the /exapps route + HaRP tunnel):
curl -fsS https://demo.nextcloud.codemyriad.io/index.php/apps/app_api/proxy/gocassini/api/v1/welcome
# → {"version":1}

# ADMIN status (Talk secrets, STT device, DB/storage):
curl -fsS -u admin:<password> \
  https://demo.nextcloud.codemyriad.io/index.php/apps/app_api/proxy/gocassini/operator/status
# → "signaling_internal_secret_configured": true
```

Then a real recording: a 2-party Talk call (**mics on, actually speak**), start
recording from the call's ⋯ menu, stop after ~1 min — a Cassini job runs
`record → build → publish` and the transcript appears in the **Cassini** viewer.

Full production install guide (any Nextcloud, not just this sandbox):
[`docs/exapp-install.md`](../docs/exapp-install.md).

## GitHub Actions

The **Deploy Sandbox** workflow SSHes into the AIO host and runs
`wire-cassini.sh` (optionally building/pushing an image first for `--image`).
It does **not** provision AIO — that is the one-time setup above. Repository
config lives in the **Sandbox** environment:

- secrets: `SANDBOX_SSH_KEY`, `SANDBOX_NC_ADMIN_PASSWORD`
- variables: `SANDBOX_SSH_HOST`, `SANDBOX_SSH_USER`, `SANDBOX_SSH_PORT`, `SANDBOX_REPO_DIR`

[D-449]: https://linear.app/code-myriad/issue/D-449
[D-515]: https://linear.app/code-myriad/issue/D-515
