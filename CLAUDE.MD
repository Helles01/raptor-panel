Raptor Panel

A fast, secure game server management panel written in Go. It is a ground-up rewrite of the Pterodactyl Panel backend (PHP/Laravel), designed to stay compatible with the existing Pterodactyl Wings daemon and database.

Goals
Replace the PHP/Laravel backend with a Go backend.
Stay compatible with the existing Pterodactyl Wings API, so Wings runs unchanged.
Be able to read the existing Pterodactyl MariaDB/MySQL database, so servers, users, nodes and allocations do not need to be recreated.
Reuse the existing React/TypeScript frontend where possible.
Prioritise security and correctness over feature count.
Repository layout
raptor-panel/
  CLAUDE.md
  NOTICE                 # third-party attributions (Pterodactyl, MIT)
  reference/             # READ-ONLY clone of github.com/pterodactyl/panel
  cmd/raptor/            # main entrypoint
  internal/
    api/                 # HTTP handlers (client API, application API, Wings API)
    auth/                # login, sessions, JWT, 2FA, permissions
    db/                  # database access (sqlc), migrations
    wings/               # client for talking to Wings nodes
    config/              # config loading
  web/                   # frontend (React/TypeScript, adapted from reference/resources/scripts)
  docs/

reference/ is the source of truth for behaviour. Never edit it, never commit changes to it, and never copy PHP code verbatim. Read it, understand the behaviour, then write idiomatic Go.

Tech stack
Go (latest stable), standard library first
HTTP: Gin or Echo (pick one early and stay consistent)
Database: MariaDB/MySQL, queries via sqlc, no ORM magic
Auth: JWT for Wings communication, secure server-side sessions for the panel
Passwords: bcrypt (must verify existing Pterodactyl hashes)
Frontend: React + TypeScript + Tailwind
Pterodactyl compatibility notes
Existing password hashes are bcrypt with the $2y$ prefix. Go expects $2a$; normalise the prefix before verification. Do not rehash until a successful login.
Some fields are encrypted with Laravel's APP_KEY format (for example node daemon tokens and 2FA secrets). Implement a Laravel-compatible decryptor behind an interface, or provide a migration command that re-encrypts them.
The Wings daemon fetches server configuration from the panel API and authenticates with node tokens. Match the request/response shapes found in reference/routes/api-remote.php and the matching controllers under reference/app/Http/Controllers/Api/Remote/.
Do not change the database schema in ways that break the existing Laravel migrations until a migration path is documented.
Security rules (non-negotiable)
Never log secrets, tokens, passwords or full request bodies containing them.
Validate and bound every input. Use allow-lists, not deny-lists.
All SQL through parameterised queries (sqlc). No string-built SQL.
Never build file paths from user input without cleaning and confining them to a base directory (path traversal was the root cause of a past Pterodactyl RCE).
Check authorisation on every endpoint, including subuser permission checks. Never trust that a UI hides an action.
Enforce 2FA support for admins.
Constant-time comparison for tokens and secrets.
Rate-limit login and sensitive endpoints.
Keep dependencies minimal, pinned and reviewed. Run govulncheck regularly.
Secrets come from environment or a config file that is git-ignored. Never commit them.
Working method

Work in small, reviewable steps. Do not attempt a wholesale port in one go.

First produce a written plan in docs/plan.md: module breakdown, order of work, and which Pterodactyl endpoints map to which Go packages. Wait for review.
Then implement one module at a time, in this order:
config + database connection + schema/sqlc setup
authentication (login, password verification, sessions, 2FA)
read-only server list and server details
power actions (start/stop/restart/kill) via Wings
Wings remote API (so existing nodes can connect)
users, nodes, allocations, eggs
subusers and permissions
schedules, backups, databases, file manager proxying
For each module: write the code, write tests, and summarise what behaviour was matched against reference/ and what was intentionally different.
Ask before making architectural decisions that are hard to reverse.
Code style
gofmt and go vet clean. Add golangci-lint config early.
Small packages, explicit errors, wrap errors with context (fmt.Errorf("...: %w", err)).
Context-aware functions (context.Context first parameter).
Table-driven tests. Aim for tests on auth, permissions and anything touching paths or Wings requests.
No global mutable state. Dependency injection through constructors.
Licensing
Raptor Panel's own license: TO BE DECIDED (MIT or AGPL-3.0).
Parts adapted from Pterodactyl (notably the React frontend) are MIT licensed: keep the original copyright notice in NOTICE and in any copied files.
Commands
go build ./cmd/raptor
go test ./...
go vet ./...
govulncheck ./...