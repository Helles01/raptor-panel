# 22 — The look of the panel (frontend restyle)

Written 2026-11-03, at the owner's request: the panel is not to look like a copy of the reference. The scope that was chosen is **a new look and branding on the existing React code** (not a new frontend, not a new
API); the administrator's pages are a later decision. The design skills `redesign-existing-projects` and `design-taste-frontend` were used; the second one says dashboards are outside its scope, so only its rules for typography,
colour, states and consistency were applied.

## Design read
Redesign (visual language changed; structure, routes, strings and the API kept) of a game-server control panel for hosting administrators and server owners, calm and technical. Dials: variance 4, motion 3, density 6.

**Direction A, "graphite and ember", dark only for now** (chosen from two mock-ups):
- **Surfaces:** cool, almost colourless graphite (`canvas` #141516, `surface` #1b1c1e, `line` #2a2c2f), flat, with hairlines instead of shadows; one corner scale (8 px controls and panels, 12 px cards, 16 px for the largest).
- **Accent:** one burnt orange ("ember", 500 = #e0703a, below 70% saturation) for the primary button, the active tab, the focus ring, links and progress. Blue, cyan and primary are the same accent (the components use all three names).
  State colours (green, red, yellow) are calmer ramps and mean state only. An offline server is neutral grey, not red.
- **Type:** Geist for text and Geist Mono for numbers, addresses and the console (both variable, latin and latin-ext, so Hungarian letters work, served from the panel: `@fontsource-variable/geist` and
  `geist-mono` 5.3.0, OFL-1.1, no dependencies and no install scripts; integrity checked against the registry before they were added). Sentence case everywhere: the all-caps captions were removed.
- **Mark:** three claw marks (`components/elements/Logo.tsx`), a simple geometric glyph that takes the colour of its text. It is a stand-in until there is a designed logo. The Pterodactyl mascot, its footer links and
  the `container@pterodactyl~` prompt are gone.

## The frame (a layout of its own, not the reference's top bar)
The first pass changed colours only and the panel still read as the reference with a new coat. The second pass changed the layout, so it reads as a theme that is simply the default:
- **A menu down the left side** (`components/layout/AppShell.tsx`): the panel's name, then the links. On a screen narrower than 1024 px it is a drawer opened from the menu button, with a dimmed overlay; it closes when the
  page changes. The drawer's translate is written by hand in a styled component, because twin.macro turns `-translate-x-full` into `transform: var(--tw-transform)`, which is empty here (the drawer then stayed open).
- **Two menus inside it:** `layout/DashboardNav.tsx` (Servers, then Account, API credentials, SSH keys, Activity, taken from `routes.account`) and `layout/ServerNav.tsx` (All servers, the server's name with its status, then
  the server's pages, each wrapped in `Can` for its permission). The active link has an ember bar (`layout/SidebarLink.tsx`). The routes carry an `icon`; `SubNavigation` is gone.
- **A slim top bar** (`NavigationBar.tsx`): the menu button and logo on a small screen, search, the account picture, sign out.
- **Servers as cards** (`dashboard/ServerRow.tsx`): name, description, a state chip (`elements/StatusChip.tsx`, tones good, warn, bad and neutral), the address and three meters (CPU, memory, disk). A server the panel
  cannot reach says "Unreachable" instead of showing an empty spinner.
- **The console page:** a header with the name and the power buttons, six stat tiles in two rows of three (the two network values share one tile; six in one row did not fit in the 1200 px column and cut the values), the console at full width,
  the graphs below. On a phone there are two columns and the address and network tiles take a full row.
- **A heading on every page** (`PageContentBlock` takes `heading`; `ServerContentBlock` passes its title, except for the console, which has its own header).
- **Full-page messages** (`elements/ScreenBlock.tsx`) are a dark card with an icon, a sentence and a button. The four illustrations of the reference (mascot, robot, server) are deleted; nothing of its artwork is left.

## Where it lives
The tokens are `web/tailwind.config.js` (names unchanged: `neutral`/`gray` 50 light to 900 dark, `blue`/`cyan`/`primary` for the accent, plus `canvas`, `surface`, `line`, `ember`) and
`assets/css/GlobalStylesheet.ts` (fonts, body, focus ring, scroll bars, reduced motion). Because the components already used these names, most screens changed by the palette alone; then by hand: the two buttons
(`elements/button/style.module.css`, `elements/Button.tsx`), inputs and labels, the boxes (`ContentBox`, `GreyRowBox`, `TitledGreyBox`), the login box, the message boxes and the connection banner, the console page
(`server/console/*`), and everything under "The frame" above.

## Found on the way
- **The stat values of the console page were unreadable** (a few pixels high): `use-fit-text` fell to its minimum and stayed there. The values now have a fixed size and the package is removed.
- **The `/admin` link** in the navigation bar led to a 404 (there is no administrator UI: C-004) and is not shown.
- Two packages are gone from the bundle (`use-fit-text`, `@fontsource-variable/ibm-plex-sans`); `scripts/frontend-check.sh` is green and the audit has no blocking finding.

## Not done yet
- A **light mode** and a switch (the colours are tokens, so it is cheap to add).
- **Title Case strings** inside the components ("Debug Information", "Change Server Details", "Startup Command") are still as the reference has them, except the page headings; they are a copy pass.
- The **favicons**, the page shell's own `Pterodactyl` identifiers (`window.PterodactylUser`, event names), and a designed logo.
- **Empty states** ("It looks like you have no databases.") are the reference's plain line with the button under it; they can become a composed block with an icon.
- Checked in the browser (desktop and a 375 px phone): sign-in, dashboard, the menu drawer, console, files (error page), databases, schedules and its dialog, startup, settings, account. Not yet looked at with data:
  backups, users, network, activity, the search and the 2FA dialog; the file manager and the console need a node.

## Working with it
`raptor serve` reads the frontend's `manifest.json` (file names and integrity values) **once, at start**. After `yarn build:production` or `scripts/frontend-check.sh` (which builds again) the file names change, and a panel
that is already running still names the old ones: the page loads and then shows nothing, because every script is a 404 (seen on 2026-11-03). Restart the panel after a build.
