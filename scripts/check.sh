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
bash scripts/dump-schema-test.sh >/dev/null

# govulncheck exits with 3 when it finds a vulnerability in code that is called. Two advisories of Echo v4 have no fixed version and were reviewed (the reasons are in
# docs/accepted-advisories.md, and internal/api/advisories_test.go fails if they stop being true). Only these identifiers are accepted: any other finding, a failure of
# the tool itself, or an output that cannot be read, fails the check.
accepted_vulns="GO-2026-6872 GO-2026-6875"
vuln_out="$(mktemp)"
trap 'rm -f "$vuln_out"' EXIT
vuln_status=0
govulncheck ./... >"$vuln_out" 2>&1 || vuln_status=$?
cat "$vuln_out"
if [ "$vuln_status" -ne 0 ]; then
    if [ "$vuln_status" -ne 3 ]; then
        echo "check: govulncheck failed (exit ${vuln_status})" >&2
        exit "$vuln_status"
    fi
    found="$(grep -oE '^Vulnerability #[0-9]+: GO-[0-9]+-[0-9]+' "$vuln_out" | grep -oE 'GO-[0-9]+-[0-9]+' | sort -u || true)"
    if [ -z "$found" ]; then
        echo "check: govulncheck found something, but no identifier could be read from its output" >&2
        exit 3
    fi
    rejected=""
    for id in $found; do
        case " ${accepted_vulns} " in
            *" ${id} "*) ;;
            *) rejected="${rejected} ${id}" ;;
        esac
    done
    if [ -n "$rejected" ]; then
        echo "check: govulncheck found vulnerabilities that are not accepted:${rejected}" >&2
        exit 3
    fi
    echo "check: only reviewed advisories remain (${found//$'\n'/ }): docs/accepted-advisories.md"
fi
