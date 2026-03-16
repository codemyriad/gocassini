# Proxmox Jellyfin NVIDIA Passthrough

This host uses a small repair script to keep Jellyfin CT `103` aligned with the
current NVIDIA device majors exposed by Proxmox after boot.

## Why This Exists

The Jellyfin LXC was previously pinned to stale NVIDIA cgroup major numbers:

- `195:*`
- `508:*`
- `511:*`

After a reboot, the host exposed:

- `195:*` for `/dev/nvidia0` and `/dev/nvidiactl`
- `234:*` for `/dev/nvidia-uvm` and `/dev/nvidia-uvm-tools`
- `237:*` for `/dev/nvidia-caps/*`

That left the container with incomplete NVIDIA access and broke NVENC with:

```text
CUDA_ERROR_NO_DEVICE: no CUDA-capable device is detected
```

## What Is Installed

- Host script: `/usr/local/sbin/proxmox-sync-jellyfin-nvidia.sh`
- Source copy in repo: [bin/proxmox-sync-jellyfin-nvidia.sh](/home/silvio/gocassini/bin/proxmox-sync-jellyfin-nvidia.sh)
- Systemd unit: `/etc/systemd/system/proxmox-sync-jellyfin-nvidia.service`

The service runs before the Proxmox guest startup path and rewrites
`/var/lib/lxc/103/config` so the NVIDIA passthrough block matches the current
host device majors.

## Manual Repair

Re-run the repair script on the Proxmox host:

```bash
sudo /usr/local/sbin/proxmox-sync-jellyfin-nvidia.sh
```

Then restart the container if it is already running:

```bash
sudo pct reboot 103
```

## Verification

Inside the container:

```bash
sudo pct exec 103 -- nvidia-smi
sudo pct exec 103 -- /usr/lib/jellyfin-ffmpeg/ffmpeg -hide_banner -v error -f lavfi -i testsrc2=size=1280x720:rate=30 -t 1 -c:v h264_nvenc -f null -
```

Jellyfin is configured for NVIDIA transcoding in:

- `/etc/jellyfin/encoding.xml`

Expected key values:

- `HardwareAccelerationType=nvenc`
- `VaapiDevice` empty
