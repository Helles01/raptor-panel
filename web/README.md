# Raptor frontend

This folder holds the React/TypeScript frontend of the Pterodactyl panel **v1.15.2** (commit
`90b2aa87469da4fffd941b3c99f3c1ca48a01b78`): `reference/resources/scripts` with its build configuration. The Pterodactyl
panel is MIT licensed, see `/NOTICE`.

Raptor serves the same API, so the frontend is used as it is. It talks to `/api/client/*`, `/auth/*` and
`/locales/locale.json`, and expects the page shell to define `window.PterodactylUser` and `window.SiteConfiguration`
(module M3).

## Build

Node.js 22 LTS and Yarn classic 1.x (versions are pinned in `/versions.env`; Node 22 is what the reference's CI uses).

```bash
yarn install --frozen-lockfile
yarn tsc
yarn lint
yarn build:production
```

The build writes `public/assets/bundle.*.js` and `public/assets/manifest.json`; these are ignored by git. Dependency
findings and decisions are tracked in `/docs/frontend-audit.md`.

## What was copied, and what was not

Copied unchanged: `package.json`, `yarn.lock`, `tsconfig.json`, `webpack.config.js`, `tailwind.config.js`,
`postcss.config.js`, `babel.config.js`, `jest.config.js`, `.eslintrc.js`, `.eslintignore`, `.prettierrc.json`,
`.editorconfig` and `resources/scripts/**`.

Not copied: the reference's `public/` folder. Its logo and favicons are Pterodactyl branding, and its admin theme and
scripts belong to the Blade admin UI that Raptor does not have.

## Changes to the reference files

Every change made here is listed in this section, so that upstream changes can still be compared.

- Line endings are LF (the reference working copy on Windows had CRLF; prettier and `.gitattributes` require LF).
- `package.json`: `deepmerge-ts` `^4.2.1` → `^8.0.2`, and a new `resolutions` block (`@babel/runtime`, `lodash-es`,
  `**/react-router/path-to-regexp`). `yarn.lock` was regenerated. These fix findings of `yarn audit` in code that ships to
  the browser; details in `/docs/frontend-audit.md`.
- Nothing under `resources/scripts` was changed.

## Still to do

- Replace the Pterodactyl branding before a release: the logo `/assets/svgs/pterodactyl.svg` referenced by
  `resources/scripts/components/auth/LoginFormContainer.tsx`, and the favicons. The name and logo are not covered by the
  MIT license of the code.
