#!/usr/bin/env bash
# Build Cassini's native frontend against pinned fork source and ORT assets.
# Usage: build.sh cpu|cuda OUTPUT_LIB_DIR WORK_DIR [additional CMake options...]
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
if ! command -v sha256sum >/dev/null; then
  sha256sum() { shasum -a 256 "$@"; }
fi
if [[ ${1:-} == --fingerprint ]]; then
  (cd "$here" && sha256sum build.sh) | sha256sum | cut -d' ' -f1
  exit 0
fi
[[ $# -ge 3 ]] || { echo 'usage: build.sh cpu|cuda OUTPUT_LIB_DIR WORK_DIR [CMake options...]' >&2; exit 2; }
backend=$1; output=$2; work=$3; shift 3
case "$backend" in cpu) gpu=OFF ;; cuda) gpu=ON ;; *) echo 'backend must be cpu or cuda' >&2; exit 2 ;; esac
# Keep the loader token literal.
# shellcheck disable=SC2016
case $(uname -s) in
  Linux) extension=so; origin='$ORIGIN' ;;
  Darwin) extension=dylib; origin='@loader_path' ;;
  *) echo 'Native packages support Linux and macOS.' >&2; exit 1 ;;
esac
if [[ $backend == cuda && ( $(uname -s) != Linux || $(uname -m) != x86_64 ) ]]; then
  echo 'Cassini CUDA packages support x86_64 only.' >&2; exit 1
fi
for tool in cmake curl tar sha256sum; do
  command -v "$tool" >/dev/null || { echo "Missing build prerequisite: $tool" >&2; exit 1; }
done
# codemyriad/sherpa-onnx tag v2.0.1-cassini-1: upstream v2.0.1 + #4032 Sortformer
# + NeMo TDT joiner-size detection + Cassini markers. The source's cmake pins
# ONNX Runtime 1.28.2 (CUDA: onnxruntime-linux-x64-gpu_cuda12-1.28.2-patched.zip).
version=2.0.1
source_commit=c810dfcf409b2a72a13e9f0ac961ff55473e350d
source_sha=2812247867348e6f22094fa5dd96097b41bdd33ca0be95d94bafcb0f68c4c12c
# What GetVersion() reports; the recorder and operator read capabilities from it.
runtime_version=${version}+cassini.parakeet-tdt-joiner-v1.nemotron-diarization-v2
fingerprint=$("$here/build.sh" --fingerprint)
preinstalled=OFF
ort_identity=pinned-upstream-archive
if [[ -n ${SHERPA_ONNXRUNTIME_LIB_DIR:-} || -n ${SHERPA_ONNXRUNTIME_INCLUDE_DIR:-} ]]; then
  : "${SHERPA_ONNXRUNTIME_LIB_DIR:?set both ONNX Runtime directories}"
  : "${SHERPA_ONNXRUNTIME_INCLUDE_DIR:?set both ONNX Runtime directories}"
  test -f "$SHERPA_ONNXRUNTIME_INCLUDE_DIR/onnxruntime_c_api.h"
  test -f "$SHERPA_ONNXRUNTIME_LIB_DIR/libonnxruntime.$extension"
  preinstalled=ON
  ort_identity=$(sha256sum "$SHERPA_ONNXRUNTIME_LIB_DIR/libonnxruntime.$extension" \
    "$SHERPA_ONNXRUNTIME_INCLUDE_DIR/onnxruntime_c_api.h")
fi
mkdir -p "$output" "$work"
output=$(cd "$output" && pwd); work=$(cd "$work" && pwd)
archive=$work/sherpa-${source_commit}.tar.gz
if [[ ! -f $archive ]]; then
  curl --fail --location --retry 3 "https://codeload.github.com/codemyriad/sherpa-onnx/tar.gz/${source_commit}" -o "$archive.tmp"
  mv "$archive.tmp" "$archive"
fi
[[ $(sha256sum "$archive" | cut -d' ' -f1) == "$source_sha" ]] || { echo 'Source archive checksum mismatch' >&2; exit 1; }
# Inputs key isolates source/build trees; an obsolete native library
# cannot survive a source change merely because it was cached.
key=$(printf '%s\n' "$fingerprint" "$backend" "$(uname -s)" "$(uname -m)" "$ort_identity" "$@" | sha256sum | cut -c1-20)
source_dir=$work/source-$key
build_dir=$work/build-$key
if [[ ! -f $source_dir/.cassini-extracted ]]; then
  mkdir -p "$source_dir"
  tar -xzf "$archive" -C "$source_dir" --strip-components=1
  touch "$source_dir/.cassini-extracted"
fi
# Keep the dynamic-loader token literal for relocatable sibling libraries.
# shellcheck disable=SC2016
cmake -S "$source_dir" -B "$build_dir" \
  -DCMAKE_BUILD_TYPE=Release -DCMAKE_CXX_STANDARD=17 -DBUILD_SHARED_LIBS=ON \
  -DCMAKE_BUILD_RPATH_USE_ORIGIN=ON -DCMAKE_INSTALL_RPATH="$origin" \
  -DSHERPA_ONNX_USE_PRE_INSTALLED_ONNXRUNTIME_IF_AVAILABLE="$preinstalled" \
  -DSHERPA_ONNX_ENABLE_GPU="$gpu" -DSHERPA_ONNX_ENABLE_C_API=ON \
  -DSHERPA_ONNX_ENABLE_BINARY=OFF -DSHERPA_ONNX_ENABLE_TESTS=OFF \
  -DSHERPA_ONNX_ENABLE_PYTHON=OFF -DSHERPA_ONNX_ENABLE_PORTAUDIO=OFF \
  -DSHERPA_ONNX_ENABLE_WEBSOCKET=OFF -DSHERPA_ONNX_ENABLE_TTS=OFF \
  -DSHERPA_ONNX_ENABLE_SPEAKER_DIARIZATION=ON \
  -DSHERPA_ONNX_BUILD_C_API_EXAMPLES=OFF "$@"
cmake --build "$build_dir" --target sherpa-onnx-c-api --parallel "${CASSINI_NATIVE_BUILD_JOBS:-2}"
grep -aqF "$runtime_version" "$build_dir/lib/libsherpa-onnx-c-api.$extension" \
  || { echo "Built library does not report $runtime_version" >&2; exit 1; }
cp "$build_dir/lib/libsherpa-onnx-c-api.$extension" "$output/"
ort=$(find "$build_dir/_deps" -path "*/lib/libonnxruntime.$extension" -print -quit)
if [[ $preinstalled == ON ]]; then ort=$SHERPA_ONNXRUNTIME_LIB_DIR/libonnxruntime.$extension; fi
if [[ -z $ort ]]; then echo 'Pinned ONNX Runtime library missing from build dependencies' >&2; exit 1; fi
cp -L "$(dirname "$ort")"/libonnxruntime*."$extension"* "$output/"
if [[ $backend == cuda ]]; then test -f "$output/libonnxruntime_providers_cuda.so"; fi
printf '%s\n' "$fingerprint" > "$output/cassini-native-inputs.sha256"
printf 'sherpa=%s\nfrontend=nemo-log-guard\ntdt_joiner=parakeet-tdt-joiner-v1\ndiarization=nemotron-diarization-v2\nonnxruntime=1.28.2\nbackend=%s\nsource_commit=%s\nsource_sha256=%s\n' \
  "$runtime_version" "$backend" "$source_commit" "$source_sha" > "$output/cassini-native-buildinfo.txt"

printf 'onnxruntime_identity=%s\n' "$ort_identity" >> "$output/cassini-native-buildinfo.txt"
