#!/usr/bin/env bash
# Build the normal reference-frontend pipeline with CUDA ONNX Runtime.
# Requires the same build tools as build-cassini-bin.sh; CUDA/cuDNN are needed
# only when running the binary. Output: dist/cassini-bin plus sibling .so files.
set -euo pipefail
exec "$(cd "$(dirname "$0")" && pwd)/build-cassini-bin.sh" --backend cuda "$@"
