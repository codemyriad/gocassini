#!/usr/bin/env bash
# Build the three evaluation corpora from the rebuilt fixture tracks.
set -euo pipefail
FIX="$1"; OUT="$2"; mkdir -p "$OUT"
SPK=(mira leo ben ana noah jules)

# 1. isolated multitrack — the status quo's best case
args=(); maps=(); meta=(); i=0
for s in "${SPK[@]}"; do args+=(-i "$FIX/$s.wav"); maps+=(-map "$i:a"); meta+=(-metadata:s:a:$i "title=$s"); i=$((i+1)); done
ffmpeg -v error -y "${args[@]}" "${maps[@]}" -c:a libopus -b:a 64k -ar 48000 -ac 1 "${meta[@]}" "$OUT/clean.mkv"

# 2. + crosstalk bleed at -35 dB (D-683 measured 34.4-37.5 dB on a real meeting)
filter=""
for j in "${!SPK[@]}"; do
  others=""; for k in "${!SPK[@]}"; do [ "$j" = "$k" ] && continue; others="$others[$k:a]"; done
  n=$(( ${#SPK[@]} - 1 ))
  filter="${filter}${others}amix=inputs=$n:duration=longest:normalize=0[bl$j];"
  filter="${filter}[bl$j]volume=-35dB,adelay=25:all=1[bd$j];"
  filter="${filter}[$j:a][bd$j]amix=inputs=2:duration=first:normalize=0[o$j];"
done
maps=(); meta=(); i=0
for s in "${SPK[@]}"; do maps+=(-map "[o$i]"); meta+=(-metadata:s:a:$i "title=$s"); i=$((i+1)); done
ffmpeg -v error -y "${args[@]}" -filter_complex "${filter%;}" "${maps[@]}" \
  -c:a libopus -b:a 64k -ar 48000 -ac 1 "${meta[@]}" "$OUT/bleed.mkv"

# 3. shared microphone — leo AND ana on one device (5 tracks, 6 people)
ffmpeg -v error -y -i "$FIX/mira.wav" -i "$FIX/leo.wav" -i "$FIX/ben.wav" \
  -i "$FIX/ana.wav" -i "$FIX/noah.wav" -i "$FIX/jules.wav" \
  -filter_complex "[1:a][3:a]amix=inputs=2:duration=longest:normalize=0[room]" \
  -map 0:a -map "[room]" -map 2:a -map 4:a -map 5:a \
  -c:a libopus -b:a 64k -ar 48000 -ac 1 \
  -metadata:s:a:0 "title=mira" -metadata:s:a:1 "title=room-laptop" \
  -metadata:s:a:2 "title=ben" -metadata:s:a:3 "title=noah" -metadata:s:a:4 "title=jules" \
  "$OUT/sharedmic.mkv"

for f in "$OUT"/*.mkv; do printf "%-14s " "$(basename $f)"; ffprobe -v error -show_entries stream_tags=title -of csv=p=0 "$f" | tr '\n' ' '; echo; done
