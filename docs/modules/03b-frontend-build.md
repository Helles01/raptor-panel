# M3b – Frontend build and audit

Status: **done** (2026-10-22). `scripts/frontend-check.sh` is green: frozen install without scripts, `tsc`, lint, 46 unit
tests, production build, dependency audit. It was built before M3 because it needs neither the database nor the schema.

## What was built
- `web/`: the build inputs of the Pterodactyl v1.15.2 frontend (`resources/scripts` and the configuration files), see
  `web/README.md` for exactly what was copied, what was not, and every change.
- `scripts/frontend-check.sh` and `scripts/frontend-audit.js`: the full frontend check, and an audit that blocks only on
  high or critical findings in code that ships to the browser (it asks webpack which package versions are in the bundle).
- `docs/frontend-audit.md`: findings, fixes and accepted findings.
- A `frontend` job in `.github/workflows/ci.yml` that runs `scripts/frontend-check.sh`.
- `.gitattributes` (`* text=auto eol=lf`): LF in the working tree on every platform. Needed here because the Windows
  working copy of the reference had CRLF, which prettier rejects (15,165 lint errors); it also protects `gofmt` and the
  shell scripts.
- `.gitignore`: the build output in `web/public/assets/` is ignored.

## Tools (installed from official sources, hash verified before installing)
| Tool | Version | Source | Verification |
|---|---|---|---|
| Node.js | 22.23.4 LTS ("Jod", released 2026-10-20) | nodejs.org | `node-v22.23.4-x64.msi`, 31,682,560 bytes, SHA-256 `ca9303d3a12e8bec6de829fcb7812fc776930e0ce5c030328a24984ef928427e` from `SHASUMS256.txt`; the downloaded file matched. The GPG signature of `SHASUMS256.txt` was not verified. |
| npm | 10.9.9 | with Node | – |
| Yarn classic | 1.22.22 | `npm install -g` (npm registry) | registry `dist.integrity` `sha512-prL3kGtyG7o9Z9Sv8IPfBNrWTDmXB4Qbes8A9rEzt6wkJV8mUvoirjU0Mp3GGAU06Y0XQyA3/2/RQFVuK7MTfg==`, `shasum` `ac34549e6aa8e7ead463a7407e1c7390f61a6610`; npm verified it on download. This is the registry's own data, not an independent source. |

**Deviation from the plan:** chapter 11 pinned Node 24.22.1 (and said to fall back to 22 if the old toolchain did not
run). Node **22** was installed directly, because the reference's CI builds with Node 22: that is the configuration the
old webpack/babel chain is known to work with. The plan and `versions.env` now say 22.23.4.

## Results
- Install with `--frozen-lockfile --ignore-scripts` works: no package needs an install script for this build.
- `yarn tsc`, `yarn lint` (after the LF conversion), `yarn test` (4 suites, 46 tests) and `yarn build:production` pass.
  The bundle is 2.6 MB of JavaScript with a `manifest.json` that carries SHA-384 integrity values (M3 uses them for
  `<script integrity>`).
- The audit found only 4 packages with a high finding that ship to the browser, all fixed by upgrades within or just
  beyond their ranges; the 8 critical findings are in build tooling. Details in `docs/frontend-audit.md`.

## What matches the reference
Everything under `resources/scripts`, unchanged.

## Deliberate differences
- Dependency versions differ in the places listed in `web/README.md` and `docs/frontend-audit.md` (security upgrades).
- The reference's `public/` folder is not copied (Pterodactyl logo and favicons, and the Blade admin theme).

## Left for later
- Replace the Pterodactyl branding before a release (logo `assets/svgs/pterodactyl.svg` used by
  `LoginFormContainer.tsx`, favicons, the "Pterodactyl" strings of the page shell). The name and logo are not covered by
  the MIT license of the code.
- Serving the bundle and the `window.PterodactylUser` / `window.SiteConfiguration` page shell, and the translation data
  behind `/locales/locale.json` (the reference keeps it as PHP language files): M3.
- Moving the build tooling to current majors, see `docs/frontend-audit.md`.
