#!/usr/bin/env bash
set -euo pipefail

CT_ID="${1:-103}"
CONFIG_PATH="/var/lib/lxc/${CT_ID}/config"
DOC_PATH="/home/silvio/gocassini/docs/proxmox-jellyfin-nvidia.md"
SCRIPT_PATH="/usr/local/sbin/proxmox-sync-jellyfin-nvidia.sh"

if [[ ! -f "${CONFIG_PATH}" ]]; then
  echo "missing config: ${CONFIG_PATH}" >&2
  exit 1
fi

required_devices=(
  /dev/nvidia0
  /dev/nvidiactl
  /dev/nvidia-uvm
  /dev/nvidia-uvm-tools
)

optional_devices=(
  /dev/nvidia-caps/nvidia-cap1
  /dev/nvidia-caps/nvidia-cap2
)

for device in "${required_devices[@]}"; do
  if [[ ! -c "${device}" ]]; then
    echo "missing NVIDIA device on host: ${device}" >&2
    exit 1
  fi
done

major_for_device() {
  local device="$1"
  local major_hex

  major_hex="$(stat -c '%t' "${device}")"
  echo "$((16#${major_hex}))"
}

declare -a devices_for_majors=("${required_devices[@]}")
for device in "${optional_devices[@]}"; do
  if [[ -c "${device}" ]]; then
    devices_for_majors+=("${device}")
  fi
done

mapfile -t majors < <(
  for device in "${devices_for_majors[@]}"; do
    major_for_device "${device}"
  done | sort -n | uniq
)

managed_block() {
  cat <<EOF
# NVIDIA passthrough for Jellyfin CT ${CT_ID}
# Managed by ${SCRIPT_PATH}
# See ${DOC_PATH}
EOF

  local major
  for major in "${majors[@]}"; do
    printf 'lxc.cgroup2.devices.allow = c %s:* rwm\n' "${major}"
  done

  cat <<'EOF'
lxc.mount.entry = /dev/nvidia0 dev/nvidia0 none bind,optional,create=file
lxc.mount.entry = /dev/nvidiactl dev/nvidiactl none bind,optional,create=file
lxc.mount.entry = /dev/nvidia-uvm dev/nvidia-uvm none bind,optional,create=file
lxc.mount.entry = /dev/nvidia-uvm-tools dev/nvidia-uvm-tools none bind,optional,create=file
lxc.mount.entry = /dev/nvidia-caps dev/nvidia-caps none bind,optional,create=dir
EOF
}

tmp_file="$(mktemp)"
trap 'rm -f "${tmp_file}"' EXIT

block="$(managed_block)"

awk -v block="${block}" '
function emit_block() {
  if (!inserted) {
    print block
    inserted = 1
  }
}

/^# NVIDIA passthrough for Jellyfin CT / { next }
/^# Managed by \/usr\/local\/sbin\/proxmox-sync-jellyfin-nvidia\.sh$/ { next }
/^# See \/home\/silvio\/gocassini\/docs\/proxmox-jellyfin-nvidia\.md$/ { next }
/^lxc\.cgroup2\.devices\.allow = c (195|234|237|508|511):\* rwm$/ { next }
/^lxc\.mount\.entry = \/dev\/nvidia0 / { next }
/^lxc\.mount\.entry = \/dev\/nvidiactl / { next }
/^lxc\.mount\.entry = \/dev\/nvidia-uvm / { next }
/^lxc\.mount\.entry = \/dev\/nvidia-uvm-tools / { next }
/^lxc\.mount\.entry = \/dev\/nvidia-caps / { next }

/^lxc\.cgroup2\.cpuset\.cpus =/ {
  emit_block()
  print
  next
}

{ print }

END {
  emit_block()
}
' "${CONFIG_PATH}" >"${tmp_file}"

install -m 0644 "${tmp_file}" "${CONFIG_PATH}"

echo "updated ${CONFIG_PATH} for CT ${CT_ID}"
printf 'allowed NVIDIA majors: %s\n' "${majors[*]}"
