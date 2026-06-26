#!/usr/bin/env bash

REPO_ROOT="$(git rev-parse --show-toplevel)"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# NOTE: this works for:
# - Claude Code - native
# - OpenCode - fallback, as long as AGENTS.md not provided
# - Pi 
ln -s "$SCRIPT_DIR/CLAUDE.md" "$REPO_ROOT/CLAUDE.md"

