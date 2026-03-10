#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
DOCKER_BIN="${DOCKER_BIN:-docker}"
BUILD_DEVICE="${CASSINI_TRANSCRIBER_DEVICE:-cuda}"
DOCKERFILE="${PROJECT_DIR}/Dockerfile.nvidia"
DEFAULT_TAG="cassini-transcriber:nvidia"

case "${BUILD_DEVICE}" in
  cpu)
    DOCKERFILE="${PROJECT_DIR}/Dockerfile.cpu"
    DEFAULT_TAG="cassini-transcriber:cpu"
    ;;
  cuda|nvidia|auto)
    ;;
  *)
    echo "unsupported transcriber docker build device: ${BUILD_DEVICE}" >&2
    exit 2
    ;;
esac

IMAGE_TAG="${CASSINI_TRANSCRIBER_IMAGE:-${DEFAULT_TAG}}"

exec "${DOCKER_BIN}" build -f "${DOCKERFILE}" -t "${IMAGE_TAG}" "${PROJECT_DIR}"
