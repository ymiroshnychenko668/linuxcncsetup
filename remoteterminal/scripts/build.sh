#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
project_dir=$(cd -- "$script_dir/.." && pwd)
build_dir=${1:-"$project_dir/build"}

for command_name in go npm; do
    if ! command -v "$command_name" >/dev/null 2>&1; then
        echo "Required build command is missing: $command_name" >&2
        exit 1
    fi
done

mkdir -p "$build_dir"

(
    cd "$project_dir/web"
    npm ci
    npm run build
)

(
    cd "$project_dir"
    CGO_ENABLED=1 go test -tags pam ./...
    CGO_ENABLED=1 go build -tags pam -trimpath -o "$build_dir/remoteterminal" ./cmd/remoteterminal
)

echo "Built $build_dir/remoteterminal and $project_dir/web/dist"
