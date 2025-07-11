#!/bin/bash

set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT_NAME="$(basename "$0")"

echo "📁 Running scripts in: $SCRIPT_DIR"
echo "⚙️  Ignoring: $SCRIPT_NAME"
echo

for file in "$SCRIPT_DIR"/*.sh; do
  fname="$(basename "$file")"
  [[ "$fname" == "$SCRIPT_NAME" ]] && continue

  echo "🚀 Executing $fname..."
  chmod +x "$file"
  "$file"
  echo "✅ Finished $fname"
  echo
done
