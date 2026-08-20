#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
project_dir=$(cd -- "$script_dir/.." && pwd)
build_dir=${1:-"$project_dir/build"}
go_binary=${GO_BINARY:-}

if [[ -z "$go_binary" ]]; then
    go_binary=$(command -v go || true)
fi
if [[ -z "$go_binary" && -x /opt/remoteterminal/tools/go-1.26.5-5c2c3b16caef/bin/go ]]; then
    go_binary=/opt/remoteterminal/tools/go-1.26.5-5c2c3b16caef/bin/go
fi
if [[ -z "$go_binary" ]]; then
    echo "Required Go toolchain is missing" >&2
    exit 1
fi
if ! command -v npm >/dev/null 2>&1; then
    echo "Required build command is missing: npm" >&2
    exit 1
fi

mkdir -p "$build_dir"

(
    cd "$project_dir/web"
    npm ci --no-audit --no-fund
    npm run lint
    npm run typecheck
    npm test -- --run
    npm run build
)

(
    cd "$project_dir"
    "$go_binary" test ./...
    "$go_binary" vet ./...
    CGO_ENABLED=0 "$go_binary" build -tags production -trimpath -ldflags '-s -w' -o "$build_dir/websetupmanager" ./cmd/websetupmanager
)

echo "Built $build_dir/websetupmanager with embedded production frontend"
