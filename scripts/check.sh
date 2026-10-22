#!/usr/bin/env bash
# Local and CI checks: pinned Go version, gofmt, go vet, go test, govulncheck.
set -euo pipefail
cd "$(dirname "$0")/.."

# shellcheck source=../versions.env
. ./versions.env

have="$(go env GOVERSION)"
if [ "$have" != "go${GO_VERSION}" ]; then
    echo "check: Go ${GO_VERSION} is pinned in versions.env, found ${have}" >&2
    exit 1
fi

unformatted="$(find . \( -path ./reference -o -path ./web \) -prune -o -name '*.go' -print0 | xargs -0 gofmt -l)"
if [ -n "$unformatted" ]; then
    echo "check: gofmt needed for:" >&2
    echo "$unformatted" >&2
    exit 1
fi

go vet ./...
go test ./...
govulncheck ./...
