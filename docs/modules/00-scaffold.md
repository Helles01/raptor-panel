# M0 – Váz

Állapot: **kész** (2026-10-22). `scripts/check.sh` zöld: Go-verzió, gofmt, `go vet`, `go test`, `govulncheck` (nincs találat).

## Mi készült
- `go.mod`: modul `github.com/Helles01/raptor-panel`, Go 1.27.3.
- `NOTICE`: AGPL-3.0 megjegyzés és a Pterodactyl MIT szerzői jogi szövege (a `reference/LICENSE.md` alapján).
- `versions.env`: rögzített verziók (referencia tag és commit, Wings 1.13.4, Go, sqlc, govulncheck, Node, MariaDB).
- `scripts/check.sh` és `.github/workflows/ci.yml`: ugyanazt a sorrendet futtatják (a CI a `versions.env`-ből telepíti a govulncheck-et).
- `cmd/raptor`: a futtatható váza (`run` függvény injektált be- és kimenettel, így tesztelhető; almenü-diszpécser), `version` parancs.

## Telepített eszközök (hivatalos forrásból, a hash-t telepítés előtt ellenőriztem)
| Eszköz | Verzió | Forrás | Ellenőrzés |
|---|---|---|---|
| Go | 1.27.3 | go.dev/dl | `go1.27.3.windows-amd64.msi`, 66 146 304 bájt, SHA-256 `89ec8325f667c674fe1b94a5a88e26932aa12323589881927ff54da99d4b7faa`: a go.dev JSON-ja és HTML oldala is ezt adja, a letöltött fájlé egyezett |
| sqlc | v1.31.1 | `go install` (proxy.golang.org) | modul hash `h1:+V+BjBJfFNPX/RFfL8eiZD9jk9lVJUEGGllWvnYNqbc=`, egyezik a sum.golang.org-gal |
| govulncheck | v1.9.0 | `go install` (proxy.golang.org) | modul hash `h1:12VaqnI9kJfqwRoPptxoqsUhQtdfrSbdksT7X/KY/sc=`, egyezik a sum.golang.org-gal |

Még nincs telepítve: Node.js, Yarn (M3b), MariaDB (integrációs tesztek, a root jelszót te adod meg), WSL2/Docker/Wings (M7 előtt).

## Függőségek
| Modul | Verzió | Miért |
|---|---|---|
| `go.yaml.in/yaml/v3` | v3.0.5 | konfigfájl és a Wings `config.yml` (a `gopkg.in/yaml.v3` helyett: úgy tudom, annak a repója archiválva van, ez a YAML szervezet által gondozott, azonos API-jú fork) |
| `github.com/go-sql-driver/mysql` | v1.10.1 | MariaDB/MySQL driver |
| `golang.org/x/crypto` | v0.58.0 | bcrypt |
| `filippo.io/edwards25519` | v1.2.0 (közvetett) | a mysql driver függősége |

Az `x/term` (jelszóbekérés a CLI-ben) az M1b CLI-parancsokkal kerül be.

## Eltérések és megjegyzések
- A CI a GitHub Actions lépéseit címkével (`@v4`, `@v5`) rögzíti, nem commit hash-sel. Hash-re cserélni a repó feltöltése előtt érdemes.
- A `scripts/check.sh` bash (Git Bash vagy Linux).
