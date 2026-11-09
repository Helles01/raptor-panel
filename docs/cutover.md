# The cutover: from the old panel to Raptor

Written 2026-11-09. This is the runbook for the day the panel is replaced. It uses only commands that exist; where something does not exist yet it says so (the last chapter). It follows
`docs/plan.md` chapter 13, and starts from the same assumption: **the `APP_KEY` of the old panel is known to someone else**, so the old machine and everything on it counts as untrustworthy.

Nothing here was run against a real old panel yet: the commands were written and tested on test databases, and the first run on real data is the rehearsal (step 1). Do not skip it.

## What is carried over, and what is not
- **Carried over:** the content of the database, as one dump. Nothing else: no `.env`, no `storage/`, no PHP files, no certificates, no keys, no scripts from the old machine.
- **New:** the machine (or a clean installation), the MariaDB instance with its own user and password, the `RAPTOR_SECRET_KEY`, the TLS certificate, the service account.
- **The nodes stay where they are.** Their `config.yml` is replaced (new token, new panel address) and Wings is restarted; their servers keep running while it is done.
- **Everybody signs in again.** API keys are revoked, second factors are reset, node tokens are replaced; the passwords of users stay (they are bcrypt hashes, which Raptor reads as they are).

## What you need
- A new machine with the versions pinned in `versions.env` (`docs/plan.md` chapter 11), set up as in `deploy/README.md`: the user, the directories, the binary, the frontend build, `/etc/raptor/raptor.env`, the service unit.
- The `APP_KEY` of the old panel (from its `.env`), for **one command only** (step 6). It is typed in, never put in a file.
- A copy of every node's current `config.yml`, kept apart (it is for the way back, and is not put on the new machine).
- A window of time, and a notice to the users: who has to do what afterwards (the list in "What your users will notice" below).
- A way to make a dump of the old database and move the file to the new machine.

## Step 0 — the new machine, before the day
Do all of `deploy/README.md` up to and including `raptor doctor`. The doctor will say that the database has no schema yet; that is right. What it must not say is anything about the configuration, the key, the address, the frontend build or the connection to the database.

## Step 1 — a rehearsal
Do steps 2 to 8 on a **test machine** with a copy of the database and, if you can, one test node. It costs an afternoon and finds what is different in your data. Write down what `raptor db check`, the review (step 5), `migrate-secrets --dry-run` and `raptor doctor` say; on the day they must say the same.

## Step 2 — stop the old panel and take the final dump
On the old machine, in the window:
```bash
# stop what writes to the database: the web server and the queue worker, and the cron of the old panel
mysqldump --single-transaction --routines --triggers --hex-blob --default-character-set=utf8mb4 -u <old user> -p <old database> > panel.sql
sha256sum panel.sql
```
Copy **only `panel.sql`** to the new machine, check the hash there, and leave the old machine switched on but not serving (it stays untouched until the end of the validation).

## Step 3 — import into the new database
```bash
mysql -u raptor -p raptor < panel.sql
```
The database and the user are the new ones (`deploy/README.md`), not the old. Then, so that nothing of the old panel's sessions or reset links can matter:
```sql
TRUNCATE TABLE sessions;
TRUNCATE TABLE password_resets;
```

## Step 4 — the schema
```bash
alias raptor='systemd-run --pipe --wait --collect -q -p User=raptor -p Group=raptor -p EnvironmentFile=/etc/raptor/raptor.env /opt/raptor/bin/raptor'
raptor db check      # the 195 migrations of the reference (v1.15.2): every one must be there, and none unknown
raptor db migrate    # Raptor's own tables (additive; the old tables are not changed)
```
If `db check` lists migrations that are missing, the old panel was another version than the one Raptor is written against: stop here and find out which, with the rehearsal's notes.

## Step 5 — look at what came over
The imported database is **untrusted data**. Read the answers of `docs/cutover-review.sql` (read-only):
```bash
mysql -u raptor -p raptor < docs/cutover-review.sql | less
```
What to look for, and what to do about it:
- **Administrators** (`root_admin = 1`) and accounts that changed lately: anybody you do not know is removed; the others are kept.
- **API keys, SSH keys, subusers:** they are revoked or reviewed in the rotation (step 7); the list tells you who will notice.
- **Eggs:** a startup command or an install script that you did not write runs on the nodes. The query shows the hash and length of every install script, to be compared with the egg files of the reference version the old panel was made from (`reference/database/Seeders/eggs`).
- **Schedules and tasks:** a `command` task or a `power` task that nobody remembers is suspect; so is a task that downloads or runs something.
- **Mounts:** a `source` outside the places nodes mean to share. **Nodes:** a `fqdn` that is not yours. **Database hosts:** a host you do not know.
- **Settings:** the mail server, the two-factor requirement. **Activity:** sign-ins and changes of the accounts in the weeks before.

Anything that looks wrong is dealt with in the database now (with the mysql client, after a dump of the database as it is), before Raptor reads it.

## Step 6 — convert the secrets
The old panel encrypted some columns with its `APP_KEY`. Raptor keeps its own copies, sealed with its own key, next to them; the old columns are not changed here.
```bash
read -rs RAPTOR_LEGACY_APP_KEY && export RAPTOR_LEGACY_APP_KEY       # type or paste the APP_KEY (with its base64: prefix); nothing is shown or saved
alias raptor-legacy='systemd-run --pipe --wait --collect -q -p User=raptor -p Group=raptor -p EnvironmentFile=/etc/raptor/raptor.env -E RAPTOR_LEGACY_APP_KEY /opt/raptor/bin/raptor'
raptor-legacy migrate-secrets --dry-run      # decrypts everything and writes nothing: this is where a wrong key shows
raptor-legacy migrate-secrets
unset RAPTOR_LEGACY_APP_KEY
```
`raptor doctor` now says whether anything is left unconverted. Do not start the panel with the old key still in the environment.

## Step 7 — replace what is known
This is the part that makes the leak of the old key not matter. In this order:
```bash
# 1. every API key of the old panel: the raw values were readable, so they count as known. New ones are made by the people who need them.
raptor apikey revoke --all --yes

# 2. every second factor: the secrets were readable. Users set it up again; --require says whether they must (none, admins or everyone).
raptor user reset-2fa --all --yes --require admins

# 3. the token of every node, one at a time (see "The nodes" below for what to do with each file).
raptor node list
raptor node rotate-token <id> > config-node-<id>.yml

# 4. the account the panel uses on every database host.
raptor dbhost list
raptor dbhost rotate-password <id>

# 5. the password of the mail server in the panel, and the one at the mail provider.
printf '%s\n' "$NEW_MAIL_PASSWORD" | raptor settings set-mail-password --password-stdin
```
**The passwords of the databases of servers** (`raptor dbhost rotate-all`) are a decision of their own: a new password on the host breaks every game server that has the old one written in its configuration. It is the right
thing to do if the passwords are assumed known, and it needs the owners to be told first. Look at what it would do (`raptor dbhost rotate-all`, no `--yes`), and do it host by host (`--host <id> --yes`) if you want to.

Rotated at the provider, by hand: the mail password, reCAPTCHA keys, S3 credentials, anything else that was in the old `.env`.

## Step 8 — start, and see it work
```bash
raptor doctor                 # no failure; read every warning
systemctl start raptor
journalctl -u raptor -f
```
Then, in a browser, what the first usable milestone proves (`docs/plan.md` chapter 5, the criteria 2 to 6) and a little more: sign in (with the second factor, if you required it) and out, the server list (your own servers and not foreign ones), a server page, the node connected, a power action (the container really changes state, and an activity entry is written), the console (output arrives and a command can be sent), and then the file list, a backup (start, finish, download, delete) and a schedule. The node, the console and the files work only after the node has its new `config.yml` (next).

### The nodes
For each node, one after another (never all at once):
1. Save `config-node-<id>.yml` as `/etc/pterodactyl/config.yml` on the node (it holds the new token and the **new panel address**).
2. `systemctl restart wings` on the node. Its servers keep running; only the panel could not reach it between the rotation and now.
3. In the panel, the node's servers show their state again. If not: the token in the file is the one `raptor node rotate-token` printed last (a second run makes a newer one and the first file stops working).

Put the DNS name of the panel on the new machine (or the proxy in front of it) when the nodes have their files.

## Step 9 — the validation period
A period you fix in advance (a week is usual). During it **the old machine and its database stay switched off and untouched**. Watch `raptor doctor`, the journal, and what your users report. What was made after the cutover (new keys, node tokens, second factors) is invisible to the old panel, so there is no
routine rollback (below).

## Step 10 — closing
When the period is over and nothing is missing:
```bash
raptor migrate-secrets --finalize                          # only checks: every old value converted, every converted copy opens with this key
# make a fresh backup of the database now, and keep it with the key
raptor migrate-secrets --finalize --yes --backup-taken     # makes the old encrypted values empty; this cannot be taken back
```
Then switch the old machine off for good, and **destroy the `APP_KEY` of the old panel, its `.env`, its database credentials and every copy of the old dump**. Keep the backup of `RAPTOR_SECRET_KEY` apart from the database.

## The way back
There is no routine rollback, because the old machine counts as compromised. In an emergency the old panel may be started **on an isolated network** only, with its own database as it was at the dump. It does not know the new node tokens: the saved
`config.yml` files of the old nodes (the copies you kept apart) have to be put back, and Wings restarted. Everything that was done after the cutover is lost to it. That is why step 1 is not optional.

## What your users will notice
| What | Who | What they do |
|---|---|---|
| They are signed out | everybody | sign in again |
| Their API keys do not work | whoever has one | make a new one (Account, API credentials) |
| Their second factor is gone | whoever used one | set it up again at the first sign-in, if it is required |
| The passwords of their databases changed | server owners, if `rotate-all` was done | read the new password in the panel and put it in the game server's configuration |
| Servers do not respond for a moment | nobody, if the nodes are done one at a time | nothing: the servers keep running |

## What does not exist yet
These are known gaps, not forgotten ones:
- **`raptor audit`** (the check of the imported database as a command, `internal/audit`) is written, but the command waits for your separate approval (`docs/plan.md` 13.5). Until then `docs/cutover-review.sql` is the same list as questions.
- **A forced change of passwords:** the password hashes of the users come over as they are. If the old machine was compromised they should be changed; there is no command that makes everybody choose a new one (a reset link can be asked for by each user on the sign-in page).
- **A real node and a real bucket** have not been tried (`docs/first-test.md`); the first time is the VPS test, and the rehearsal.
