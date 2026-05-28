# VM / Mac-hosted Talk harness

This directory contains the VM-specific harness variant. It is intentionally isolated from the native Linux harness under `harness/`.

Use it when Docker/Nextcloud/Talk run inside a Linux VM and the browser runs on the Mac/host machine.

## Start / stop through `cassini dev`

```bash
export CASSINI_HARNESS_VM=true
# Optional. If omitted, the VM harness tries to use the VM's primary IPv4 address.
# export CASSINI_HARNESS_HOST=192.168.252.20

./bin/cassini dev stack up
./bin/cassini dev stack down
```

Without `CASSINI_HARNESS_VM=true`, `cassini dev stack up|down` uses the native harness exactly as before.

## Direct VM harness commands

```bash
harness/vm/bin/up.sh
harness/vm/bin/create-room.sh --name "VM room"
harness/vm/bin/down.sh --volumes
```

## Host browser setup

Open the VM-facing Nextcloud root and log in with `admin` / `admin`:

```bash
VM_IP="$(multipass list | awk '$1 == "dev-vm" { print $3; exit }')"
open "http://${VM_IP}:28080/"
```

After login, use the normal Nextcloud app navigation to open Talk.

For later media-capture testing, Chrome must treat the exact HTTP origin as secure. Include the scheme and port:

```bash
open -na "Google Chrome" --args \
  --user-data-dir=/tmp/cassini-vm-chrome \
  --unsafely-treat-insecure-origin-as-secure="http://${VM_IP}:28080" \
  "http://${VM_IP}:28080/"
```

## Layout

- `compose.yml` — VM-specific Compose stack.
- `bin/` — VM-specific lifecycle/bootstrap/render scripts.
- `config/` — static Janus transport config used by the VM stack.
- `runtime/generated/` — generated signaling, Janus, and Coturn config; ignored by git.

The VM harness defaults to project name `spreedtest-vm` so its Docker volumes are separate from the native harness.
