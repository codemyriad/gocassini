# Talk recording — manual validation walkthrough

_Last validated in `dev-vm` on 2026-06-26._

## Goal

Validate in `dev-vm` that Cassini can be installed as a Nextcloud ExApp, its control panel and viewer are visible, and two private admin + Erlich Bachman 1:1 Talk recordings produce transcripts in the viewer without losing existing published meetings.

## Host/VM setup

From the host repo:

```bash
cd /Users/ivan/dev/cassini
multipass list
multipass info dev-vm
```

Planning-time VM facts:

- VM name: `dev-vm`
- VM IP: `192.168.252.29`
- actual repo mounts:
  - `/home/ubuntu/cassini`
  - `/home/ubuntu/dev/workspace`

Use the mounted workspace inside VM:

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && git branch --show-current && docker --version && docker compose version'
```

If old stacks are running and you want a clean validation:

```bash
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && docker compose -p spreedtest-vm -f harness/vm/compose.yml --profile full down --volumes || true'
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace && docker compose -p cassini-exapp-test -f harness/compose.yml --profile full down --volumes || true'
multipass exec dev-vm -- bash -lc 'cd /home/ubuntu/dev/workspace/deployment && docker compose down --volumes || true'
```

## Start installed ExApp harness

Run:

```bash
multipass exec dev-vm -- bash -lc '
  cd /home/ubuntu/dev/workspace
  ./bin/cassini dev stack up \
    --services full \
    --cassini installed-exapp \
    --recording-backend installed-exapp \
    --build
'
```

Expected evidence:

- Nextcloud reachable at `http://<vm-ip>:28080/`.
- AppAPI is installed/enabled.
- Cassini ExApp is registered/enabled.
- Talk `recording_servers` points at the AppAPI proxy base for `gocassini`.
- `/api/v1/welcome` through proxy returns `{"version":1}`.
- `/operator/status` reports Talk recording secret and internal signaling secret configured.

## Open the UI

On the host:

```bash
VM_IP="$(multipass list | awk '$1 == "dev-vm" { print $3; exit }')"
open "http://${VM_IP}:28080/"
```

Log in:

```text
admin / admin
```

Check:

1. Nextcloud app menu has `Cassini`.
2. Admin menu or app menu has `Cassini Admin`.
3. `Cassini Admin` opens the control panel.
4. `Cassini` opens the viewer.

If Chrome needs the VM HTTP origin treated as secure for Talk media:

```bash
open -na "Google Chrome" --args \
  --user-data-dir=/tmp/cassini-vm-chrome \
  --unsafely-treat-insecure-origin-as-secure="http://${VM_IP}:28080" \
  "http://${VM_IP}:28080/"
```

## Run private 1:1 validation

Run the installed-ExApp validation helper:

```bash
multipass exec dev-vm -- bash -lc '
  cd /home/ubuntu/dev/workspace
  ./harness/bin/validate-installed-exapp-private-talk.sh \
    --nextcloud-host 192.168.252.29 \
    --duration 60
'
```

Expected helper behavior:

- creates/reuses users:
  - `cassini-erlich` / `Erlich Bachman`
  - `cassini-monica` / `Monica Hall`
  - `admin`
- creates/reuses admin-facing 1:1 conversation with Erlich;
- starts recording through Talk/OCS so Talk calls the installed ExApp backend;
- plays Erlich fixture audio into the private call;
- polls the installed ExApp admin API through AppAPI proxy until job 1 is done;
- runs a second separate private recording job;
- polls until job 2 is done;
- fetches published catalog/transcript metadata through the ExApp proxy;
- asserts both new jobs remain visible and previous catalog IDs remain.

## Manual fallback for the private flow

If the helper needs to be driven manually:

```bash
multipass exec dev-vm -- bash -lc '
  cd /home/ubuntu/dev/workspace
  ./bin/cassini dev play-private --scaffold-only --nextcloud-host 192.168.252.29
'
```

Then run playback/recording twice:

```bash
multipass exec dev-vm -- bash -lc '
  cd /home/ubuntu/dev/workspace
  ./bin/cassini dev play-private --conversation admin --nextcloud-host 192.168.252.29 --duration 60
'

multipass exec dev-vm -- bash -lc '
  cd /home/ubuntu/dev/workspace
  ./bin/cassini dev play-private --conversation admin --nextcloud-host 192.168.252.29 --duration 60
'
```

Open the admin 1:1 call URL from `harness/runtime/play-private-scaffold.json` if you want to watch it in the browser.

## Verify transcript in viewer

Open the Cassini viewer from the Nextcloud app menu, or direct proxy route if needed:

```text
http://<vm-ip>:28080/index.php/apps/app_api/proxy/gocassini/viewer/
```

Expected:

- both newest private recording jobs appear;
- each transcript has non-empty segments/words, or portable catalog metadata reports a positive segment count;
- Erlich/admin private runs are represented in the catalog;
- older meetings remain present if the catalog was non-empty before.

## Debug commands

Status through AppAPI proxy:

```bash
curl -fsS -u admin:admin \
  "http://${VM_IP}:28080/index.php/apps/app_api/proxy/gocassini/operator/status" | python3 -m json.tool
```

Talk config inside Nextcloud:

```bash
multipass exec dev-vm -- bash -lc '
  cd /home/ubuntu/dev/workspace
  docker compose -p cassini-exapp-test -f harness/compose.yml exec -T -u www-data nextcloud php occ config:app:get spreed recording_servers
  docker compose -p cassini-exapp-test -f harness/compose.yml exec -T -u www-data nextcloud php occ config:app:get spreed call_recording
'
```

ExApp logs:

```bash
multipass exec dev-vm -- bash -lc 'docker logs nc_app_gocassini --tail=200'
```

Archive preservation:

```bash
multipass exec dev-vm -- bash -lc '
  docker exec nc_app_gocassini sh -lc '\''find "$APP_PERSISTENT_STORAGE/operator/jobs/current" -maxdepth 1 -name "*.meeting" | sort'\''
  docker exec nc_app_gocassini sh -lc '\''python3 - <<PY
import json, os
p=os.path.join(os.environ["APP_PERSISTENT_STORAGE"], "site/published/catalog.json")
d=json.load(open(p))
print(len(d.get("meetings", [])))
print([m.get("id") for m in d.get("meetings", [])])
PY'\''
'
```

## Last validated run

The final D-395 validation run used:

```bash
multipass exec dev-vm -- bash -lc '
  cd /home/ubuntu/dev/workspace
  ./harness/bin/validate-installed-exapp-private-talk.sh \
    --nextcloud-host 192.168.252.29 \
    --duration 60
'
```

It preserved the pre-existing catalog entry `01KW1WGDK5F6T7CND3767ZN69Q` and added:

- job 1: `01KW1XSN0T64BBYC8XKBFB2WVG`
- job 2: `01KW1XVZWZCMW25D42HX7TX3HE`

The helper reported `catalog_entries=3`.
