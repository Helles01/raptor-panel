# The schema of the old panel

Raptor builds its database layer from the real structure of the database it is going to run on. Three files go in this folder, **structure only,
no rows**:

| File | What it is |
|---|---|
| `mysqldump-no-data.sql` | the output of `mysqldump --no-data` |
| `migrations.txt` | the `migration` column of the `migrations` table, in order of `id` |
| `server-version.txt` | `SELECT VERSION();` |

## Making them
On a machine that has the MariaDB or MySQL client programs and can reach the old panel's database (a copy of it is fine, and so is a restored
backup):

```bash
scripts/dump-schema.sh --host 127.0.0.1 --user panel pterodactyl
```

The script asks for the password on the terminal (or takes `MYSQL_PWD`) and gives it to the client programs in a file that only you can read, never
as an argument. It only reads. It writes nothing if the dump holds a single row, if it does not look like the panel's database (fewer than 30
tables, or no `users`, `servers`, `nodes`, `allocations`, `api_keys` or `migrations`), or if the migrations table is empty. `--force` replaces
files that are already here. `scripts/dump-schema-test.sh` tests the script with stand-ins for the client programs.

Look through the three files before committing them: they are only the structure, but they are yours to check.

## What happens next
`migrations.txt` is compared with `internal/db/expected_migrations.txt` (the 195 migrations of the reference v1.15.2); a difference is a
decision to make before anything else. Then the sqlc baseline is built from the dump and the stores of `docs/store-contract.md` follow.
