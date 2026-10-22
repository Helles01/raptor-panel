# M0 – Scaffold

Status: **done** (2026-10-22). `scripts/check.sh` is green: Go version, gofmt, `go vet`, `go test`, `govulncheck` (no
findings in called code).

## What was built
- `go.mod`: module `github.com/Helles01/raptor-panel`, Go 1.27.3.
- `NOTICE`: the AGPL-3.0 notice and the Pterodactyl MIT copyright text (from `reference/LICENSE.md`).
- `versions.env`: pinned versions (reference tag and commit, Wings 1.13.4, Go, sqlc, govulncheck, Node, MariaDB).
- `scripts/check.sh` and `.github/workflows/ci.yml`: they run the same steps in the same order (CI installs govulncheck
  from `versions.env`).
- `cmd/raptor`: the skeleton of the executable (a `run` function with injected input and output so it is testable, a
  subcommand dispatcher), the `version` command.

## Installed tools (from official sources; the hash was verified before installing)
| Tool | Version | Source | Verification |
|---|---|---|---|
| Go | 1.27.3 | go.dev/dl | `go1.27.3.windows-amd64.msi`, 66,146,304 bytes, SHA-256 `89ec8325f667c674fe1b94a5a88e26932aa12323589881927ff54da99d4b7faa`: the go.dev JSON and HTML page both give this, and the downloaded file matched |
| sqlc | v1.31.1 | `go install` (proxy.golang.org) | module hash `h1:+V+BjBJfFNPX/RFfL8eiZD9jk9lVJUEGGllWvnYNqbc=`, matches sum.golang.org |
| govulncheck | v1.9.0 | `go install` (proxy.golang.org) | module hash `h1:12VaqnI9kJfqwRoPptxoqsUhQtdfrSbdksT7X/KY/sc=`, matches sum.golang.org |

Not installed yet: Node.js and Yarn (M3b), MariaDB (integration tests; the root password is set by the project owner),
WSL2/Docker/Wings (before M7).

## Dependencies
| Module | Version | Why |
|---|---|---|
| `go.yaml.in/yaml/v3` | v3.0.5 | the config file and the Wings `config.yml` (instead of `gopkg.in/yaml.v3`: as far as is known that repository is archived; this is the fork maintained by the YAML organisation, with the same API) |
| `github.com/go-sql-driver/mysql` | v1.10.1 | MariaDB/MySQL driver |
| `golang.org/x/crypto` | v0.58.0 | bcrypt |
| `filippo.io/edwards25519` | v1.2.0 (indirect) | dependency of the mysql driver |

`x/term` (password prompts in the CLI) will be added together with the M1b CLI commands.

## Differences and notes
- The CI pins the GitHub Actions steps by tag (`@v4`, `@v5`), not by commit hash. It is worth replacing them with hashes
  before the repository is published.
- `scripts/check.sh` is bash (Git Bash or Linux).
