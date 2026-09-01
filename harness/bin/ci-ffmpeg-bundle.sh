#!/usr/bin/env bash
set -euo pipefail

: "${IMAGE_REF:?IMAGE_REF must name the Cassini image to test}"

printf '[ffmpeg-bundle] checking %s\n' "$IMAGE_REF"
docker run --rm --entrypoint /bin/sh "$IMAGE_REF" -ceu '
  fail() {
    echo "[ffmpeg-bundle] FAIL $*" >&2
    exit 1
  }

  ffmpeg_path=$(command -v ffmpeg)
  ffprobe_path=$(command -v ffprobe)
  test "$ffmpeg_path" = /opt/cassini/ffmpeg/bin/ffmpeg || fail "ffmpeg resolved to $ffmpeg_path"
  test "$ffprobe_path" = /opt/cassini/ffmpeg/bin/ffprobe || fail "ffprobe resolved to $ffprobe_path"
  test ! -e /usr/bin/ffmpeg || fail "distro /usr/bin/ffmpeg is still installed"
  test -r /opt/cassini/ffmpeg/BUILDINFO || fail "BUILDINFO is missing"

  resolved=$(sed -n "s/^resolved_version=//p" /opt/cassini/ffmpeg/BUILDINFO | head -n 1)
  requested=$(sed -n "s/^requested_version=//p" /opt/cassini/ffmpeg/BUILDINFO | head -n 1)
  case "$resolved" in
    ""|*[!0-9.]*|.*|*..*|*.) fail "invalid resolved version in BUILDINFO: $resolved" ;;
  esac
  if test "$requested" != latest && test "$requested" != "$resolved"; then
    fail "requested version $requested differs from resolved version $resolved"
  fi
  actual=$(ffmpeg -version | sed -n "1s/^ffmpeg version \([^ ]*\).*/\1/p")
  test "$actual" = "$resolved" || fail "runtime version $actual differs from BUILDINFO $resolved"

  buildconf=$(ffmpeg -hide_banner -buildconf 2>&1)
  for flag in --disable-autodetect --disable-network --enable-libopus; do
    printf "%s\n" "$buildconf" | grep -q -- "$flag" || fail "configure flag missing: $flag"
  done
  if printf "%s\n" "$buildconf" | grep -Eq -- "--enable-(gpl|nonfree)"; then
    fail "GPL or nonfree FFmpeg components are enabled"
  fi
  if ldd "$ffmpeg_path" 2>&1 | grep -q "not found"; then
    ldd "$ffmpeg_path" >&2 || true
    fail "ffmpeg has an unresolved shared library"
  fi

  ffmpeg -hide_banner -encoders 2>/dev/null | grep -Eq "[[:space:]]libopus[[:space:]]" \
    || fail "libopus encoder is unavailable"
  ffmpeg -hide_banner -decoders 2>/dev/null | grep -Eq "[[:space:]]opus[[:space:]]" \
    || fail "native Opus decoder is unavailable"
  filters=$(ffmpeg -hide_banner -filters 2>/dev/null)
  for filter in aresample anullsrc asetpts aformat concat amix alimiter sine; do
    printf "%s\n" "$filters" | grep -Eq "[[:space:]]${filter}[[:space:]]" \
      || fail "required filter is unavailable: $filter"
  done

  tmp=$(mktemp -d)
  trap "rm -rf \"$tmp\"" EXIT HUP INT TERM
  ffmpeg -hide_banner -loglevel error \
    -f lavfi -i sine=frequency=880:sample_rate=48000:duration=0.15 \
    -c:a libopus "$tmp/source.opus"
  ffmpeg -hide_banner -loglevel error -i "$tmp/source.opus" \
    -map 0:a:0 -c:a copy -metadata CASSINI_SMOKE=ok "$tmp/retag.opus"
  ffprobe -v error -select_streams a:0 \
    -show_entries stream=codec_name,sample_rate,channels -of default=nw=1 "$tmp/retag.opus" \
    | grep -q "codec_name=opus" || fail "Ogg Opus probe failed"
  ffmpeg -hide_banner -loglevel error -i "$tmp/retag.opus" \
    -af "aresample=async=1:first_pts=0,aformat=sample_fmts=s16:sample_rates=16000:channel_layouts=mono" \
    -f s16le -acodec pcm_s16le -y /dev/null

  # Cassini capture and transcription consume Matroska/WebM as well as Ogg.
  # Exercise an actual mux/probe/decode rather than trusting component lists,
  # whose flag formatting has changed between FFmpeg major releases.
  ffmpeg -hide_banner -loglevel error \
    -f lavfi -i sine=frequency=440:sample_rate=48000:duration=0.15 \
    -c:a libopus "$tmp/source.mkv"
  ffprobe -v error -select_streams a:0 \
    -show_entries stream=codec_name,sample_rate,channels -of default=nw=1 "$tmp/source.mkv" \
    | grep -q "codec_name=opus" || fail "Matroska Opus probe failed"
  pcm_bytes=$(ffmpeg -hide_banner -loglevel error -i "$tmp/source.mkv" \
    -map 0:a:0 -f s16le -acodec pcm_s16le -ar 16000 -ac 1 - | wc -c)
  test "$pcm_bytes" -gt 0 || fail "Matroska-to-PCM pipe decode produced no audio"

  # Exercise the exact portable-pack lifecycle, not only an FFmpeg retag. The
  # multi-track mix filters produce an end-trim-sensitive WebM: FFmpeg 9.0.1
  # normalizes its final Ogg granule by 24 samples on the first metadata remux.
  # Cassini must converge on that normalized compressed identity, rebuild the
  # v3 manifest, and verify the tagged output.
  bundle="$tmp/mixed.meeting"
  mkdir -p "$bundle"
  ffmpeg -hide_banner -loglevel error \
    -f lavfi -i sine=frequency=410:sample_rate=48000:duration=1.00 \
    -f lavfi -i sine=frequency=520:sample_rate=48000:duration=1.00 \
    -filter_complex "[0:a][1:a]amix=inputs=2:duration=longest:normalize=0,alimiter=limit=0.95[out]" \
    -map "[out]" -ac 1 -ar 48000 -c:a libopus -b:a 64k -vbr on \
    -compression_level 10 -application voip "$bundle/meeting.webm"
  cat >"$bundle/cassini.json" <<EOF
{"kind":"meeting","version":"cassini.meeting.v1","created_at_utc":"2026-08-29T00:00:00Z","state":"ready","stage":"ready","source_kind":"mkv","source_path":"smoke.mkv"}
EOF
  cat >"$bundle/manifest.json" <<EOF
{"kind":"cassini.meeting-artifact.v1","version":"1","generatedAt":"2026-08-29T00:00:00Z","source":{"basename":"smoke.mkv","durationMs":1000},"files":{"audio":"meeting.webm","transcript":"transcript.words.v1.json"},"speakerCount":1,"wordCount":1}
EOF
  cat >"$bundle/transcript.words.v1.json" <<EOF
{"version":"transcript.words.v1","media":{"src":"meeting.webm","durationMs":1000},"speakers":[{"id":"spk_1","label":"Speaker 1"}],"segments":[{"speaker":"spk_1","startMs":0,"endMs":100,"text":"hello","words":[{"text":"hello","startMs":0,"endMs":100}]}]}
EOF
  if ! pack_output=$(cassini pack "$bundle" --out "$tmp/mixed.opus" 2>&1); then
    printf "%s\n" "$pack_output" >&2
    fail "portable mixed-WebM pack failed"
  fi
  if ! inspect_output=$(cassini inspect "$tmp/mixed.opus" 2>&1); then
    printf "%s\n" "$inspect_output" >&2
    fail "portable mixed-WebM inspect failed"
  fi
  printf "%s\n" "$inspect_output" | grep -q "cassini=ok" \
    || fail "portable mixed-WebM integrity was not verified"

  echo "[ffmpeg-bundle] OK FFmpeg $resolved"
'
