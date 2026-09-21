#!/usr/bin/env bash
# Reclaim hosted-runner space below a conservative 40 GiB cleanup threshold.
# This threshold is not a measured minimum required by the build.
set -euo pipefail
cleanup_threshold_kib=$((40 * 1024 * 1024))
available_kib() { df -Pk / | awk 'NR == 2 {print $4}'; }
df -h / | head -2
if [[ "$(available_kib)" -ge "$cleanup_threshold_kib" ]]; then
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
