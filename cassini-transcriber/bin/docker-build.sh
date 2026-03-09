#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
IMAGE_TAG="${CASSINI_TRANSCRIBER_IMAGE:-cassini-transcriber:nvidia}"
DOCKER_BIN="${DOCKER_BIN:-docker}"

exec "${DOCKER_BIN}" build -f "${PROJECT_DIR}/Dockerfile.nvidia" -t "${IMAGE_TAG}" "${PROJECT_DIR}"
