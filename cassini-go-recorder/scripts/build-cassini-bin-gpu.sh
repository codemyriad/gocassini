#!/usr/bin/env bash
# Build cassini-bin linked against a CUDA-enabled sherpa-onnx for linux-x86_64.
#
# The default sherpa-onnx-go-linux module ships a CPU-only libonnxruntime.so.
# This script swaps in the CUDA build that k2-fsa publishes alongside each
# sherpa-onnx release, then drives `go build` through a temporary go.work
# override so the existing go.mod stays untouched.
#
# Prerequisites:
#   - linux-x86_64 host with Go >= 1.22, git, curl, tar, bzip2
#   - NVIDIA GPU with CUDA 12.x + cuDNN 9.x runtime libs (only needed to run
#     the resulting binary; not required to build it)
#
# Output (under cassini-go-recorder/dist/):
#   cassini-bin                      — GPU-linked binary
#   libonnxruntime.so                — GPU-enabled runtime, ship with binary
#   libsherpa-onnx-c-api.so          — sherpa-onnx C API, ship with binary
#   libonnxruntime_providers_*.so    — CUDA EP and shared providers
#
# Usage:
#   scripts/build-cassini-bin-gpu.sh [--sherpa-version 1.13.1]

set -euo pipefail

SHERPA_VERSION="1.13.1"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --sherpa-version) SHERPA_VERSION="$2"; shift 2 ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

REC="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$REC/dist"
CACHE="$REC/.build-cache"
mkdir -p "$DIST" "$CACHE"

SHERPA_TARBALL="sherpa-onnx-v${SHERPA_VERSION}-cuda-12.x-cudnn-9.x-linux-x64-gpu.tar.bz2"
SHERPA_URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/v${SHERPA_VERSION}/${SHERPA_TARBALL}"
SHERPA_EXTRACT="$CACHE/sherpa-onnx-v${SHERPA_VERSION}-gpu"

if [[ ! -d "$SHERPA_EXTRACT" ]]; then
  echo "==> downloading $SHERPA_TARBALL"
  curl -fL --retry 3 -o "$CACHE/$SHERPA_TARBALL" "$SHERPA_URL"
  echo "==> extracting"
  mkdir -p "$SHERPA_EXTRACT"
  tar -xjf "$CACHE/$SHERPA_TARBALL" -C "$SHERPA_EXTRACT" --strip-components=1
fi

SHERPA_LIB="$SHERPA_EXTRACT/lib"
if [[ ! -f "$SHERPA_LIB/libonnxruntime.so" ]] || [[ ! -f "$SHERPA_LIB/libsherpa-onnx-c-api.so" ]]; then
  echo "expected libs not found under $SHERPA_LIB" >&2
  exit 1
fi
CUDA_PROVIDER="$SHERPA_LIB/libonnxruntime_providers_cuda.so"
if [[ ! -f "$CUDA_PROVIDER" ]] || ! strings "$CUDA_PROVIDER" | grep -F "CUDAExecutionProvider" >/dev/null; then
  echo "downloaded onnxruntime has no CUDA execution-provider library" >&2
  exit 1
fi

# Set up a local sherpa-onnx-go-linux clone with the GPU .so swapped in.
# go.work makes this transparent to the in-tree go.mod.
SHIM="$CACHE/sherpa-onnx-go-linux-gpu"
if [[ ! -d "$SHIM/.git" ]]; then
  echo "==> cloning sherpa-onnx-go-linux@v${SHERPA_VERSION}"
  rm -rf "$SHIM"
  git clone --depth=1 --branch "v${SHERPA_VERSION}" \
    https://github.com/k2-fsa/sherpa-onnx-go-linux "$SHIM"
fi
SHIM_LIB="$SHIM/lib/x86_64-unknown-linux-gnu"
echo "==> swapping in GPU libraries"
cp "$SHERPA_LIB/libonnxruntime.so"        "$SHIM_LIB/libonnxruntime.so"
cp "$SHERPA_LIB/libsherpa-onnx-c-api.so"  "$SHIM_LIB/libsherpa-onnx-c-api.so"
for so in "$SHERPA_LIB/"libonnxruntime_providers_*.so; do
  [[ -f "$so" ]] && cp "$so" "$SHIM_LIB/"
done

# Build with a transient go.work that points sherpa-onnx-go-linux at the shim.
GOWORK_FILE="$CACHE/go.work"
cat > "$GOWORK_FILE" <<EOF
go 1.22

use $REC

replace github.com/k2-fsa/sherpa-onnx-go-linux v${SHERPA_VERSION} => $SHIM
EOF

echo "==> building cassini-bin"
(
  cd "$REC"
  GOWORK="$GOWORK_FILE" go build -p 2 -o "$DIST/cassini-bin" ./cmd/cassini
)

echo "==> copying runtime libs"
cp "$SHIM_LIB/libonnxruntime.so"        "$DIST/"
cp "$SHIM_LIB/libsherpa-onnx-c-api.so"  "$DIST/"
for so in "$SHIM_LIB/"libonnxruntime_providers_*.so; do
  [[ -f "$so" ]] && cp "$so" "$DIST/"
done

echo "==> verifying CUDA symbols"
if ldd "$DIST/cassini-bin" >/dev/null 2>&1; then
  ldd "$DIST/cassini-bin" | grep -E "onnxruntime|sherpa" || true
fi
if [[ ! -f "$DIST/libonnxruntime_providers_cuda.so" ]] || \
   ! strings "$DIST/libonnxruntime_providers_cuda.so" | grep -F "CUDAExecutionProvider" >/dev/null; then
  echo "FAIL: shipped runtime has no CUDA execution-provider library" >&2
  exit 1
fi

echo
echo "==> done. Built to $DIST/"
ls -lh "$DIST/"
echo
echo "Ship the entire $DIST/ directory together; the binary uses rpath to find"
echo "the .so files in the same directory. The host must provide CUDA 12.x and"
echo "cuDNN 9.x runtime libraries (install via the matching nvidia-*-cu12 pip"
echo "wheels, the system package manager, or any other channel)."
