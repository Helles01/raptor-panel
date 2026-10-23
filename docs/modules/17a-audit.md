# M17 groundwork – the audit of stored data

Status: **checks done** (2026-10-23); the command that loads the rows and prints the report is **not** written: the plan (13.5) says `raptor audit` is built in after separate approval at the start of M17.

## What it is for
The reference accepted data that Raptor refuses or cannot use: a cron field its library read leniently, an egg rule outside Raptor's interpreter, an allowed-address list that nothing can read, a password hash of another kind.
The audit finds every such row before the cutover, so that nobody meets it afterwards. It is the technical half of 13.5; the other half (root admins, recent changes, install scripts compared with the seeded eggs, mounts, suspicious activity)
is a review for a person and is not in this package.

## Done: `internal/audit`
`audit.Run(Input, now)` returns a `Report` of twelve sections, always in this order, with `Count`, `HasErrors`, `Text()` and `JSON()`. A finding names the row (`schedules#4 (Backup)`), the problem and what follows from it.
**Error** means something stops working for somebody; **Warning** means the row works but Raptor would refuse to save it again.

| Section | Checks | Severity |
|---|---|---|
| `egg_variables` | the rules must be applicable (`eggrules.Parse`); the environment name (`eggs.CheckEnvName`) | error; warning |
| `egg_images` | a JSON object of names and images, not empty; each image of an allowed form | error; warning |
| `schedules` | the name; the cron fields parse and match a date; each task's action is known (error) and its payload, offset and position are valid, positions are not repeated | error / warning |
| `api_keys` | allowed addresses: all unreadable means the key works from nowhere; some unreadable match nothing | error; warning |
| `subusers` | permission names that do not exist: only the owner and administrators can manage the subuser (C-025) | warning |
| `servers` | the server's own settings against the rules of creation (limits, image, startup, names) | warning |
| `nodes`, `locations` | the settings against the rules of creation | warning |
| `allocations` | the address reads, the port is 1 to 65535 (error); the alias (warning) | error; warning |
| `users` | the password is a bcrypt hash and two-factor has a secret (errors); the user name, capitals, the e-mail address (warnings) | error; warning |
| `recovery_tokens` | the token is a bcrypt hash, or it can never be used | error |
| `settings` | a known setting's value against its rule | warning |

New small exports serve it: `eggs.CheckEnvName`, `servers.ValidateImage`, `settings.Known`, `auth.ValidHash`, `users.ValidateUsername`.

## What the command must do (to be approved)
Read the rows in one transaction (read-only), fill `audit.Input` (the settings without their storage prefix, the stored allowed addresses decoded from JSON, a schedule with its tasks), call `Run(in, now)`, print `Text()` or, with a flag, `JSON()`, and exit
with a failure status when `HasErrors()`. It writes nothing. It belongs in the cutover checklist before `--finalize`: no errors, and every warning read and accepted.

## Still to do
The command and its loader (waits for the schema), and the sections of the review half that need judgement rather than a rule. More rows can be added the same way; each new rule of Raptor that is stricter than the reference should get a check here.
