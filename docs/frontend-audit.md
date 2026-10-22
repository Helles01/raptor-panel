# Frontend dependency audit

The frontend in `web/` is the Pterodactyl panel v1.15.2 frontend (React 16, webpack 5, about 1,500 locked packages).
This file records what the audit found, what was changed and why. It is updated whenever a finding is added, fixed or
accepted.

Audit date: 2026-10-22. Node.js 22.23.4, Yarn 1.22.22.

## How it is checked

```bash
node scripts/frontend-audit.js        # from the repository root, after `yarn install --frozen-lockfile` in web/
```

`yarn audit` reports every package in the tree, including webpack, babel and the dev server, which never reach a user's
browser. The script therefore builds the production bundle, asks webpack which package **versions** are in it, and
matches the `yarn audit` findings against those versions. **High and critical findings in shipped code fail the run**;
findings in build tooling, and moderate or low findings, are reported but do not block. An accepted finding goes into
`web/audit-exceptions.json` (`[{"id": "GHSA-...", "reason": "..."}]`), with a reason.

`yarn audit` is used and not `npm audit`, because the lockfile is `yarn.lock`: it examines exactly the versions that are
shipped.

Dependencies are installed with `--ignore-scripts` (no package install script runs; the build works without them) and
`--frozen-lockfile` (the lockfile's integrity hashes are what is installed).

## Result

Baseline (the reference's lockfile, as copied): 1,488 packages in the tree. `yarn audit` reported 8 critical, 282 high,
160 moderate and 39 low findings, counted per dependency path. The production bundle contains 103 packages (108 package
versions after the changes below). **Only 4 packages with a high finding are in the browser bundle** (`axios`,
`deepmerge-ts`, `lodash-es`, `path-to-regexp`); all 8 critical findings are in build tooling.

After the changes below: **no high or critical finding in shipped code.**

| Place | critical | high | moderate | low |
|---|---|---|---|---|
| shipped to the browser | 0 | 0 | 1 | 1 |
| build tooling only | 5 | 56 | 36 | 5 |

(Counted per package version; the baseline numbers above are counted per path, so the two are not comparable.)

## Changes made to the reference's files

| Package | Before | After | Findings fixed | How |
|---|---|---|---|---|
| `axios` | 1.13.5 | 1.20.0 | 12 high (prototype pollution gadgets, header injection, proxy credential leaks, ReDoS, DoS) | `yarn upgrade` (range `^1.13.2` allows it) |
| `lodash-es` | 4.17.21 | 4.18.1 | high: code injection through `_.template` | `resolutions` (`formik` and `yup` depend on it) |
| `path-to-regexp` (react-router) | 1.7.0 | 1.9.0 | high: backtracking regular expressions | `resolutions` for `**/react-router/path-to-regexp` |
| `deepmerge-ts` | 4.2.1 | 8.0.2 | high: stack exhaustion on recursive objects | `package.json` range `^8.0.2` (used only in `console/chart.ts`; `tsc`, lint, tests and the build pass) |
| `codemirror` | 5.57.0 | 5.65.21 | moderate: ReDoS | `yarn upgrade` |
| `i18next-http-backend` | 3.0.2 | 3.0.6 | moderate: path traversal and URL injection through `lng`/`ns` | `yarn upgrade` |
| `@babel/runtime` | 7.18.3 | 7.28.6 | moderate: inefficient regular expression in generated code | `resolutions` |

`package.json` changed in two places: `deepmerge-ts` and a new `resolutions` block (`@babel/runtime`, `lodash-es`,
`**/react-router/path-to-regexp`). `yarn.lock` was regenerated. Line endings were converted from CRLF to LF. Nothing
under `resources/scripts` was changed. After the changes: `yarn tsc`, `yarn lint`, `yarn test` (46 tests) and
`yarn build:production` all pass.

Release dates of the new versions (checked on the npm registry): axios 1.20.0 2026-08-26, deepmerge-ts 8.0.2 2026-08-21,
i18next-http-backend 3.0.6 2026-04-23, lodash-es 4.18.1 2026-04-01, codemirror 5.65.21 2026-02-07, `@babel/runtime`
7.28.6 2026-01-12, path-to-regexp 1.9.0 2024-09-10. A side effect of `yarn upgrade @babel/runtime` is that the build-time
`devDependencies` entry `@babel/runtime@^7.12.1` moved to 7.29.10 (2026-10-07, about two weeks old); it is used by the
build only.

## Accepted findings in shipped code (not blocking)

| Package | Severity | Finding | Why it is accepted |
|---|---|---|---|
| `i18next-http-backend` 3.0.6 | low | GHSA-xvq9-wjp8-hwqf, incomplete URL validation permits SSRF (fixed only in 4.0.2, a major version) | The load path is fixed (`/locales/locale.json?locale=…&namespace=…`, `resources/scripts/i18n.ts`) and the backend never fetches a URL supplied by a user. The language comes from the account and is validated by the server (plan 10.1 c). A move to v4 is possible later. |
| `uuid` 13.0.0 | moderate | GHSA-w5hq-g745-h8pq, missing buffer bounds check in v3/v5/v6 when `buf` is provided | The frontend only calls `v4()` without a buffer (`components/elements/Switch.tsx`). |

## Findings in build tooling (not blocking)

These packages run only on the machine that builds the frontend (webpack, babel, eslint, jest, the dev server). The
critical ones are `@babel/traverse`, `loader-utils`, `proxy-addr`, `shell-quote` and `websocket-driver`. They matter if
the build machine builds untrusted code or runs the dev server on an open network; neither is the case: the build runs
over this repository's own sources with a frozen lockfile, and `yarn serve` (the webpack dev server) is not used. They
are tracked, and cleared as the tooling is updated.

## Not done

- A move of the tooling to current majors (webpack, babel, eslint, TypeScript 5.1) is a larger change, not needed for the
  shipped bundle, and left for later.
- React 16 is old but is what the frontend is written for; moving it is a rewrite of parts of the UI.
- The reference's `public/` assets (logo, favicons) are not copied: they are Pterodactyl branding.
