# The local test database

Installed 2026-11-03, with the approval of the project owner, for the integration tests of the SQL layer (`go test -tags integration`).

| | |
|---|---|
| Server | MariaDB 11.8.9 from the official ZIP `mariadb-11.8.9-winx64.zip` (97,016,251 bytes). SHA-256 `830c4672…1963f5` agreed with `downloads.mariadb.org` (REST API) and `archive.mariadb.org` (`sha256sums.txt`) and with the file as downloaded; the full value is in `versions.env` |
| Where | `D:\Pluginok\tools\mariadb-11.8.9-winx64` (program) and `D:\Pluginok\tools\mariadb-data` (data), outside the repository; no Windows service, no administrator rights |
| Network | `127.0.0.1:3306` only |
| Accounts | `root` (password in `.raptor-test.env`) and `raptor_test`, which has all rights on the database `raptor_test` and nothing else. The passwords were generated at install time and are only in `.raptor-test.env`, which is in `.gitignore` |
| Run | `powershell -File scripts\testdb.ps1 start` (also `stop`, `status`); after a reboot it has to be started again |
| Tests | `export RAPTOR_TEST_DSN="$(grep '^RAPTOR_TEST_DSN=' .raptor-test.env \| cut -d= -f2-)"` then `go test -tags integration -p 1 ./internal/db ./internal/store` (`-p 1`: the packages share one database and must not run at the same time) |

Removing it: stop it, delete the two folders and `.raptor-test.env`.

## What was checked on it
`go test -tags integration ./internal/db`: the migrations, the secret store, and `InitBaseline` (35 tables of the baseline, 195 migrations recorded, a second
run refused). `raptor db check` says "OK: the database has exactly the expected schema version" on a database made by `raptor db init`.
