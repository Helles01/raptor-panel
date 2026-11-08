# 24 — The password of the mail server

Written 2026-11-08. The panel signs in to the mail server with the settings of the `settings` table (host, port, encryption, user name, sender: `internal/mail`) and a password
that is read from `raptor_secrets`. On a panel that came from the old one, `raptor migrate-secrets` seals the old password there. A fresh installation had no way to set it; this
was the open point of `docs/first-test.md` and `21-build-and-serve.md`.

## The command
```
printf '%s\n' "$PASSWORD" | raptor settings set-mail-password --password-stdin
raptor settings set-mail-password --clear
```
- The password is read from the **first line of the standard input**, never from an argument (every process on the machine can read the arguments). It is not shown by
  `settings get` or `settings list`, and the command never prints it.
- `--clear` removes it, for a mail server that needs none. Exactly one of the two flags is given.
- The mail is sent with the new password from the next message on: the sender reads the settings when it sends (`21-build-and-serve.md`), so no restart is needed.

## How it is kept
- Sealed in `raptor_secrets` (`settings`, the row id of the setting `mail:mailers:smtp:password`, column `value`), the same place `migrate-secrets` uses, so the sender finds it either way.
- A fresh installation has no row for that setting: it is made with an empty value, in one statement (`INSERT … ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`), so two commands
  that arrive together take turns on the row instead of deadlocking on a shared lock (the first version, an `INSERT IGNORE` and a locking read, did deadlock under eight at once).
- A row that came from the old panel keeps its old, encrypted value in the column: the old columns are not touched (plan 2.3). The new password is sealed beside it.
- `--clear` removes the sealed value and leaves the row.

## What is checked
Not empty, at most 1000 characters (counted as characters, not bytes), and **no control character**: a line end, a tab or an escape in the password would break the SMTP command that
carries it. A refused password is not repeated in the error message.

## Matched against the reference
The reference keeps the password in `.env` (`MAIL_PASSWORD`) and in the `settings` table, encrypted with `APP_KEY`; there is no command for it there. Raptor keeps it sealed with its own
key, and does not read `.env`. Nothing in the reference's behaviour is changed.

## Tests
Unit tests for the service (limits, control characters, the error that does not repeat the password) and the command (one line of input, both flags, the password as an argument is a
usage error, nothing on stdout). On the real MariaDB: a fresh installation (no row, then a row, replaced, cleared), a row from the old panel (its value kept), and eight first passwords at once (one row,
one sealed value), also with `innodb_snapshot_isolation` off. Seven mutants were made to fail on purpose (not sealed, old value overwritten, clear forgets the secret, control characters allowed,
the limit in bytes, both flags allowed, the password on stdout): all were caught.
