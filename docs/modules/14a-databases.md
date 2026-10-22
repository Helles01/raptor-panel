# M14 groundwork – databases on external hosts

Status: **groundwork done** (2026-10-22): the rules and the SQL for the databases the panel creates for servers, which need
no database to test. The endpoints (`/servers/{server}/databases*`, the application API for database hosts), the connection to
the hosts and the encrypted host passwords wait for the schema and for a MariaDB to run the integration tests against.

## Done: `internal/dbhosts`
| Function | Content |
|---|---|
| `UniqueName`, `Username`, `Password` | The names and credentials of a new database: `s<server id>_<name>` cut to 48 characters, `u<server id>_` and ten random characters, and a 24-character password of letters and digits with 2 to 6 of `! @ = . + ^`. Randomness comes from a reader, positions are chosen without bias. |
| `ValidateRemote`, `ValidateMaxConnections`, `Host.Validate` | Allow-lists: the address a user may connect from (letters, digits and `_ - / . % :`), the connection limit, and a database host's name, address, port and user. |
| `CheckCanCreate`, `ChooseHost` | The feature switch and the server's limit (null means no limit, 0 means none, as in the reference); the choice of a host at random among those linked to the server's node, falling back to any host only when allowed. |
| `Create`, `ChangePassword`, `Drop` | The statements to run on a host, as `Statement{SQL, Args}`: identifiers checked and quoted, the password bound. See C-028 and C-029. |

Mutation checks confirmed that each of these fails a test: not escaping the GRANT, dropping the server-prefix checks for the
database and for the user, widening the name alphabet to a backtick, skipping the remote and password checks, passing the
password inside the SQL, no truncation, the limit boundary, hosts of no node matching a node, modulo bias, and one special
character too few. The structure test runs 20 000 mostly valid but partly hostile databases through `Create` and checks that
every statement has exactly the quotes it should, and no `;`, quote or newline.

## What matches the reference
The name and user formats, the password alphabet, the privileges granted (the same list), the host choice and its fallback,
the limit semantics, the error texts, and the order of the statements on creation (database, user, rights).

## Deliberate differences (`docs/compat.md`: C-028, C-029)
- Names are ASCII, GRANT targets are escaped, and the builders refuse anything that is not this server's (C-028).
- A password change is one `ALTER USER`, statements are not built with the password inside, and there is no `FLUSH PRIVILEGES` (C-029).

## Still to do (waits for the schema and a MariaDB)
- **The integration test that proves C-028** on a real MariaDB (and one on MySQL if the hosts may be MySQL): the wildcard
  behaviour is taken from the documentation and the reading of the reference, and has not been reproduced yet.
- Check that the hosts accept a bound password in `CREATE USER` / `ALTER USER`; if not, the password goes in as a quoted
  literal through a function that accepts only the generator's alphabet (the builders already insist on it).
- The create flow: count and create in one transaction with the server's databases locked (plan 10.1 d), clean-up with `Drop`
  when a step fails (and do not hide its own failure), the host password handling in `raptor_secrets` (N1), the hashid in
  the URLs, activity events (`server:database.create|delete|update-password`), and the view-password permission.
- `raptor dbhost` (plan M14) and the check that a host with databases cannot be deleted (the reference's text: "Cannot delete a
  database host server that has active databases linked to it.").
