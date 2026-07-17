#!/usr/bin/env bash
set -euo pipefail

# Compatibility shim for older harness scripts. The implementation is split
# across lib/*.sh so safe helpers can be sourced without triggering stack
# topology validation. This shim resolves defaults only; stack entrypoints call
# harness_stack_init explicitly when they need fail-loud validation.

_HARNESS_COMMON_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib/base.sh
source "$_HARNESS_COMMON_DIR/lib/base.sh"
# shellcheck source=./lib/stack-env.sh
source "$_HARNESS_COMMON_DIR/lib/stack-env.sh"
# shellcheck source=./lib/artifacts.sh
source "$_HARNESS_COMMON_DIR/lib/artifacts.sh"
# shellcheck source=./lib/stack.sh
source "$_HARNESS_COMMON_DIR/lib/stack.sh"

harness_stack_env_resolve

unset _HARNESS_COMMON_DIR
