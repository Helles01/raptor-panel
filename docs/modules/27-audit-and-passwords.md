# 27 — `raptor audit`, and taking the passwords of everybody

Written 2026-11-09, at the owner's word ("csinald az audit parancsot es a jelszocseret egybe"). The first is the command of plan 13.5 that was waiting for a separate approval; the
second is the last open item of `docs/cutover.md` ("what does not exist yet"), in its small form (see the end for the large one).

## 1 — `raptor audit [--json]`
Reads the data in the database and says, row by row, what Raptor cannot use (**error**: something stops working for somebody) and what it still reads but would refuse to write
again (**warning**). It changes nothing and deletes nothing. Exit code 1 when there is an error, 0 with only warnings, so a script can stop on it. The text goes to the standard output, the count to the standard error;
`--json` writes the same report for a program.

- **The checks** are `internal/audit` (written earlier, 12 kinds of row: egg variable rules, egg images, schedules and tasks, API key address lists, subuser permissions, servers, nodes, locations, allocations, users, recovery tokens,
  settings). **New here:** the reading of the rows (`internal/db/auditsource.go`, 12 fixed statements, all in one read-only `REPEATABLE READ` transaction so that the report is about one moment) and the command (`cmd/raptor/cmd_audit.go`).
- **Lists that are not lists of texts** (an API key's addresses, a subuser's permissions) become one entry that cannot be read. For a key that is the right answer: Raptor reads a list that is not empty as a restriction, so such a key works from nowhere,
  and the report says so.
- **Second factor secrets** are looked for in both places, the old column and `raptor_secrets`; a user who has set it up with Raptor, or whose old column was emptied by `migrate-secrets --finalize`, is not reported as having none.
- **The password of the mail server is never read into the audit.** It is the one setting that holds a secret, and a report is pasted into tickets.
- **The text is safe for a terminal.** The rows are the database's own text and the database is untrusted: a user name with an escape sequence in it would otherwise move the cursor or set the title of whoever reads the report. Control characters become `?`
  (`audit.Text`); the JSON form quotes them. Found while writing the command, not in the first version of the report.

**Matched against the reference:** there is nothing to match; the reference has no such check. The plan's list (13.5) also names *safety* questions (administrators nobody knows, an egg script that was changed, a mount outside the shared places, suspicious activity). Those are
not errors of usability and are left to `docs/cutover-review.sql`; the report does not pretend to judge them.

**Tests:** the report on a fake source (exit codes, JSON, a source that fails), the escape sequence in a name; on the real MariaDB a whole database with one good and several bad rows of every kind
(`TestAuditOnARealDatabase`): the bad ones named, the good ones not, the secret of the mail server nowhere, the database unchanged, and only warnings once the errors are removed. 10 mutants killed (the second factor read from one place only, a list that is not a list read
as empty, tasks not attached to their schedule, columns swapped for servers, nodes and allocations, the mail password read, errors not failing the command, both forms written, the text not cleaned).

## 2 — `raptor user reset-passwords --all [--yes]` and `raptor user set-password <user>`
**`reset-passwords --all`** gives every account a password that matches nothing, ends every session and removes every reset link, in one transaction. Without `--yes` it changes nothing and says how many users it would affect. People choose a new password with the
"forgot password" page, which needs the mail to work; `docs/cutover.md` says to set the mail first. The password every account has afterwards is **a bcrypt hash of a random text that is not kept anywhere** (the same hash for all of them), not a marker string: the accounts then look like any other
(the check of the imported data would report a marker as an error, and a login check never needs a special case), and nothing can sign in with it.

**`set-password <user> [--password-stdin]`** gives one user a new password (read from the standard input, or made and shown once on the standard output, like `user create`) and ends their sessions and reset links. It is how an administrator gets back in after the
reset when the mail is not set up; it is also useful on its own. The reasons a password is refused are the ones of a new account (length).

**Matched against the reference:** the reference changes a password through the "forgot password" flow or the account page, and both end the sessions of the user; neither takes everybody's at once. The flow after the reset is the reference's own.

**Intentionally different / not done:**
- **No mail is sent to everybody.** A mass mailing from a panel whose mail was set up an hour ago is a decision for a person; users who try to sign in get "invalid credentials" and use the forgot page.
- **The nodes are not told.** The reference's password reset also revokes the user's access at the nodes (Wings tokens and SFTP). Here a Wings token lives at most 15 minutes, an SFTP login checks the password at every connection (an open one stays until it is closed), and at the cutover the node tokens are
  replaced anyway. A single `set-password` does not tell them either.
- **A forced change at the next sign-in** (a flag in a table of our own, a change of the sign-in, a page in the frontend) was not built. It would be about three modules and would add behaviour the reference does not have; `reset-passwords` does what the cutover needs.

**Tests:** unit (a password that is given or made, validation, an unknown user, the go-ahead, a hash that is valid and matches none of the passwords one would try, a store that fails); on the real MariaDB (`TestAdminPasswords`): one user's new password ends their sessions and
reset links and not the others', the reset leaves no session and no link and every hash unusable, nothing else of the users changed, and the way back in works. 9 mutants killed (a fixed text behind the hash, sessions or links kept, one user's change ending everyone's sessions, no go-ahead, no validation, the wrong text hashed).
Found on the way: the tests first had the arguments of `auth.VerifyPassword(hash, password)` the wrong way round, which made "matches nothing" true of everything; fixed, and the mutant with a known text behind the hash is what proves the check means something.

## Not tested here
A real old database: `raptor audit` has been run on a database made for the test, not on yours. The first real report is the rehearsal (`docs/cutover.md` step 1); expect findings that surprise you, and send them: each one is either a check that is too strict or a row that needs fixing.

## Found on the way: two new allocations at once (not part of either module)
`TestClientAllocationsSamePortChosenTwice` failed once in a full integration run (and not in 15 runs of its own). With `innodb_snapshot_isolation = OFF`, which is how MariaDB 10.x and MySQL 8 behave and so how the
database of an old panel may behave, it failed **every time** (12 of 12): two servers that make a new allocation on the same node and address at the same moment both chose port 30000, and the second got a `Duplicate entry` error from the unique key
(a failed request for the user). The cause: the port list (`PortsOfNodeAddress`) was a plain read, so it came from the snapshot the transaction took at its first plain read (the count of the server's allocations), before the request that went first had made its port. MariaDB 11.8 with snapshot isolation hid
it most of the time by failing the lock before and running the request again, which is why the suite was green in the usual setting. **Fix:** the query is a locking read (`FOR UPDATE`), which reads what is committed now. After it: 12 of 12 with the setting off, the whole `store` and `db` suites green with it off and on.
`docs/modules/20a-sql-first-stores.md` already says lock tests must be run with the setting off as well; this one had been missed. Rule for the future: **after any change near a lock, run the store suite with `SET GLOBAL innodb_snapshot_isolation = OFF` too, and set it back.**
