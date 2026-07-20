# shellcheck shell=bash
# Compatibility shim for callers that historically sourced lib-exapp-image.sh.
# Canonical manifest identity and route readers live in lib-exapp-manifest.sh.

_EXAPP_IMAGE_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib-exapp-manifest.sh
source "$_EXAPP_IMAGE_LIB_DIR/lib-exapp-manifest.sh"
unset _EXAPP_IMAGE_LIB_DIR
