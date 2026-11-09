# Putting the panel on a machine

The files here are for a Linux machine with systemd and a MariaDB, which is where the panel is meant to run. They were written on a machine that has neither, so what they say is checked
against the code (`deploy_test.go` reads them), not by running them: the first run is the VPS test (`docs/first-test.md`). Read each file before it goes in; they are short.

| File | What it is |
|---|---|
| `raptor.env.example` | every setting the panel reads, with what each means; copy it to `/etc/raptor/raptor.env` |
| `raptor.service` | the systemd unit: its own user, a start that is checked first (`raptor doctor`), and what the panel does not need taken away |
| `nginx.conf.example`, `Caddyfile.example` | a reverse proxy with TLS in front of the panel |

## A clean machine
The panel goes on a machine of its own, or at least in directories of its own; it is not put in the place of the old panel (`docs/plan.md` chapter 13).

```bash
# a user that cannot log in, and the places
useradd --system --no-create-home --shell /usr/sbin/nologin raptor
install -d -o root -g root -m 0755 /opt/raptor/bin /opt/raptor/web/public
install -d -o root -g raptor -m 0750 /etc/raptor

# the binary and the build of the frontend (made with `go build ./cmd/raptor` and scripts/frontend-check.sh)
install -m 0755 raptor /opt/raptor/bin/raptor
cp -r web/public/assets /opt/raptor/web/public/assets
```

The database is a new one with a new user and a new password; the old credentials are not reused.

```sql
CREATE DATABASE raptor CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'raptor'@'127.0.0.1' IDENTIFIED BY '<a new password>';
GRANT ALL PRIVILEGES ON raptor.* TO 'raptor'@'127.0.0.1';
```

The environment: `install -m 0600 -o root -g root deploy/raptor.env.example /etc/raptor/raptor.env`, then fill in `RAPTOR_DB_PASSWORD`, `RAPTOR_APP_URL`, and `RAPTOR_SECRET_KEY` (made with `raptor keygen`,
**and kept somewhere else as well**: without it the secrets stored with it cannot be read again).

## Running commands
The commands of the panel (`raptor user create`, `raptor doctor` and the rest) need the same environment as the panel. `systemd-run` gives it the way the service gets it, as the panel's user, and passes the
standard input on, which a password is given through:

```bash
alias raptor='systemd-run --pipe --wait --collect -q -p User=raptor -p Group=raptor -p EnvironmentFile=/etc/raptor/raptor.env /opt/raptor/bin/raptor'

raptor db init                       # an empty database: the tables of the reference and Raptor's own
raptor user create --admin --email you@example.com --username you --first-name You --last-name Admin --password-stdin < password-file
raptor doctor                        # what is wrong, before anything is started
```

A database that came from the old panel is not initialised: it is checked and migrated, and its secrets are converted. That is the cutover, in `docs/cutover.md`.

## Starting it
```bash
install -m 0644 deploy/raptor.service /etc/systemd/system/raptor.service
systemctl daemon-reload && systemctl enable --now raptor
journalctl -u raptor -f
```
`raptor doctor` runs before every start: a failure (a missing key, a database that cannot be reached, a schema that is not up to date, no frontend build) stops the start with the reason in the journal; what
is only a warning (no mail server, no proxy trusted, a node without a token) is printed and does not.

Then the reverse proxy (`nginx.conf.example` or `Caddyfile.example`), with `RAPTOR_TRUSTED_PROXIES` set to its address, so that the panel sees the address of the client and not of the proxy.

## Updating
Build the new binary and the new frontend, put them in place, and `systemctl restart raptor`. The check runs first; a schema change of Raptor's own tables is `raptor db migrate` (the doctor says when it is due).

## Backing up
The database, and `RAPTOR_SECRET_KEY` apart from it. The backups of the servers are the nodes' (or the bucket's) own business.
