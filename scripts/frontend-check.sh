#!/usr/bin/env bash
# Frontend checks: install from the frozen lockfile without install scripts, typecheck, lint, unit tests, production
# build, dependency audit. Needs the Node.js and Yarn versions pinned in versions.env.
set -euo pipefail
cd "$(dirname "$0")/.."

# shellcheck source=../versions.env
. ./versions.env

have="$(node --version)"
if [ "$have" != "v${NODE_VERSION}" ]; then
    echo "frontend-check: Node ${NODE_VERSION} is pinned in versions.env, found ${have}" >&2
    exit 1
fi

(
    cd web
    yarn install --frozen-lockfile --ignore-scripts
    yarn tsc
    yarn lint
    yarn test
    yarn build:production
)
node scripts/frontend-audit.js
