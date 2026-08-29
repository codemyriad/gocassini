#!/usr/bin/env bash
set -euo pipefail
FIX=/work/fixture; OUT=/work/corpus2; SPK=(mira leo ben ana noah jules)
for DB in 25 30 35 45 55; do
  filter=""
  for j in "${!SPK[@]}"; do
    others=""; for k in "${!SPK[@]}"; do [ "$j" = "$k" ] && continue; others="$others[$k:a]"; done
    n=$(( ${#SPK[@]} - 1 ))
    filter="${filter}${others}amix=inputs=$n:duration=longest:normalize=0[bl$j];"
    filter="${filter}[bl$j]volume=-${DB}dB,adelay=25:all=1[bd$j];"
    filter="${filter}[$j:a][bd$j]amix=inputs=2:duration=first:normalize=0[o$j];"
  done
  args=(); maps=(); meta=(); i=0
  for s in "${SPK[@]}"; do args+=(-i "$FIX/$s.wav"); maps+=(-map "[o$i]"); meta+=(-metadata:s:a:$i "title=$s"); i=$((i+1)); done
  ffmpeg -v error -y "${args[@]}" -filter_complex "${filter%;}" "${maps[@]}" \
    -c:a libopus -b:a 64k -ar 48000 -ac 1 "${meta[@]}" "$OUT/bleed${DB}.mkv"
  echo "built bleed${DB}.mkv"
done
