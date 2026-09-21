#!/usr/bin/env bash
# Reclaim hosted-runner space only when a CUDA build needs it. Keep 40 GiB
# available for the base, extracted layers, build cache and exported app image.
set -euo pipefail
minimum_kib=$((40 * 1024 * 1024))
available_kib() { df -Pk / | awk 'NR == 2 {print $4}'; }
df -h / | head -2
if [[ "$(available_kib)" -ge "$minimum_kib" ]]; then
  echo 'At least 40 GiB free; skipping CUDA build disk cleanup.'
  exit 0
fi

echo 'Less than 40 GiB free; reclaiming unused hosted-runner toolchains.'
sudo rm -rf \
  /usr/share/dotnet /usr/local/lib/android /opt/ghc \
  /opt/hostedtoolcache/CodeQL /usr/local/share/boost \
  /usr/local/share/powershell /usr/share/swift
sudo docker image prune -af 2>&1 | tail -1 || true
df -h / | head -2
if [[ "$(available_kib)" -lt "$minimum_kib" ]]; then
  echo 'Insufficient disk space for the CUDA build after cleanup (need 40 GiB).' >&2
  exit 1
fi
