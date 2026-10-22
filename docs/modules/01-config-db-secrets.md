# M1 – Config, database, secrets

Status: **partly done** (2026-10-22). The schema-independent part is done and tested; the baseline schema, the sqlc
queries that depend on it and the integration tests that need MariaDB wait for the `docs/schema/` files and a MariaDB
instance.

## Done
| Package | Content |
|---|---|
| `internal/config` | A YAML file (optional, `--config` or `RAPTOR_CONFIG`) and `RAPTOR_*` environment variables; the environment overrides. An unknown field is an error. Allow-list validation (host, port, name, user, TLS mode). Neither the password nor the key appears in `%v`, `%+v`, `%#v` or `slog` output (guarded by a test). Later additions: `app.url` (M6, M3), the `wings` section (M6), and `app.timezone` / `RAPTOR_APP_TIMEZONE` (M12: an IANA name, default UTC, the zone database is built into the binary, `App.Location()`; `Local` is refused because it depends on the machine). |
| `internal/secrets` | Raptor's secret format: AES-256-GCM, `rp1:` prefix, a secret is bound to its location (`raptor_secrets\|table\|row\|column` as AAD), so it cannot be swapped between rows. A failure of `Open` does not reveal a reason. |
| `internal/legacycrypt` | **Decryption only** of the reference's format (Laravel `Crypt`): AES-CBC + HMAC-SHA256 (over `iv+value`), unwrapping a PHP-serialized string, `APP_KEY` (also with the `base64:` prefix). The MAC is checked before anything else is parsed; every failure is the same `ErrDecrypt`. |
| `internal/legacycrypt/legacycrypttest` | For tests only: producing the Laravel format. Production code must not import it. |
| `internal/secretmigration` | The logic of `migrate-secrets`: 6 columns (`nodes.daemon_token`, `api_keys.token`, `users.totp_secret`, `database_hosts.password`, `databases.password`, the SMTP password in `settings`). All or nothing: if any value cannot be decrypted, **nothing is written**. Re-runnable (by source hash it converts only new or changed rows, `--all` converts everything), `--dry-run`. It has read-only access to the old columns. |
| `internal/db` | `Open` (safe driver defaults, UTC, `utf8mb4`), `Migrate` (Raptor tables only: a `raptor_migrations` tracking table, a database lock against parallel runs, one statement per file), `ExpectedMigrations` (the 195 migrations of v1.15.2) and `CompareMigrations`, `AppliedLaravelMigrations` (read-only), `SecretStore` (fixed `SELECT`s of the old columns; the only table written is `raptor_secrets`). |
| sqlc | `sqlc.yaml`, `internal/db/migrations/0001_raptor_secrets.sql`, `internal/db/queries/secrets.sql`, generated `internal/db/dbq`. |
| `cmd/raptor` | `raptor keygen`, `raptor db check`, `raptor db migrate`, `raptor migrate-secrets [--dry-run] [--all]`. The old `APP_KEY` comes only from the `RAPTOR_LEGACY_APP_KEY` environment variable, never from a file. |

## What matches the reference
- Every element of the encryption format (CBC, the HMAC over `iv+value`, the `\/` escape in the JSON, PHP `s:N:"…";`
  serialization with a byte length). The reference writes all six columns with the default, serializing `encrypt()`; the
  code also handles a non-serialized (`encryptString`) value.
- The list of migrations is the file names in `reference/database/migrations` (195 of them, the last one is
  `2024_07_13_091852_clear_unused_allocation_notes`).

## Deliberate differences
See `docs/compat.md`: C-002 (secrets in the `raptor_secrets` table), C-006 (additive tables with the `raptor_` prefix).

## Verification
`scripts/check.sh` is green. The integration tests (`internal/db/integration_test.go`, `-tags integration`) compile
(`go vet -tags integration`), **but have not run yet**, because there is no MariaDB.

## What is left, and what it waits for
1. **Baseline schema** (`docs/schema/mysqldump-no-data.sql`, `migrations.txt`, `server-version.txt`) →
   `internal/db/schema/`, the second schema source of sqlc, the further queries. `migrations.txt` is to be compared with
   `expected_migrations.txt`.
2. **A MariaDB instance** for the integration tests (`RAPTOR_TEST_DSN`); the root password is set by the project owner.
3. **A real Laravel sample.** The test vectors were produced with our own code from the description of the format, so a
   shared misreading could slip through. To rule that out, either `raptor migrate-secrets --dry-run` on the real database
   (decrypting every row, MAC check included, proves the format), or one value encrypted by real PHP and its key.
4. Re-check the `key`/`value` column names of the `settings` table and the `settings::mail:mailers:smtp:password` key
   against the dump.
