#!/usr/bin/env bash

# DEPRECATED: retained as a compatibility launcher.
# Install or run the supported Go TUI with ./tui/install.sh --run instead.

set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

printf '%s\n' 'WARNING: setup.sh is deprecated; use ./tui/install.sh --run instead.' >&2
exec "${SCRIPT_DIR}/tui/install.sh" --run "$@"
