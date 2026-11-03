#!/usr/bin/env bash
# Writes the three files Raptor needs from the database of the old panel, into docs/schema/:
#   mysqldump-no-data.sql   the structure of the database, no rows
#   migrations.txt          the names of the migrations that were run, one a line
#   server-version.txt      the version of the database server
# It only reads. Nothing is written to the database, and no row of any other table leaves it. The password is asked for on the terminal
# (or read from MYSQL_PWD) and handed to the client programs in a file that only you can read, never as an argument, which every process on
# the machine could see.
#
# Usage: scripts/dump-schema.sh [--host H] [--port P] [--user U] [--force] DATABASE
set -euo pipefail
cd "$(dirname "$0")/.."

host=127.0.0.1
port=3306
user=
force=0
while [ $# -gt 0 ]; do
    case "$1" in
        --host) host="${2:?--host needs a value}"; shift 2 ;;
        --port) port="${2:?--port needs a value}"; shift 2 ;;
        --user) user="${2:?--user needs a value}"; shift 2 ;;
        --force) force=1; shift ;;
        -h|--help) sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        --) shift; break ;;
        -*) echo "dump-schema: unknown option $1" >&2; exit 2 ;;
        *) break ;;
    esac
done
if [ $# -ne 1 ]; then
    echo "usage: scripts/dump-schema.sh [--host H] [--port P] [--user U] [--force] DATABASE" >&2
    exit 2
fi
database="$1"

# What goes into a configuration file or a command line is checked first: no quotes, spaces, control characters or option-like names.
case "$host" in *[!A-Za-z0-9._:-]*|'') echo "dump-schema: the host has characters I will not pass on" >&2; exit 2 ;; esac
case "$port" in *[!0-9]*|'') echo "dump-schema: the port must be a number" >&2; exit 2 ;; esac
case "$database" in -*|*[!A-Za-z0-9_\$.-]*|'') echo "dump-schema: the database name has characters I will not pass on" >&2; exit 2 ;; esac
case "$user" in *[!A-Za-z0-9_\$.@-]*) echo "dump-schema: the user name has characters I will not pass on" >&2; exit 2 ;; esac
[ -n "$user" ] || user="$(id -un)"

for tool in mysqldump mysql; do
    command -v "$tool" >/dev/null 2>&1 || { echo "dump-schema: $tool is not installed (it comes with the MariaDB or MySQL client)" >&2; exit 1; }
done

out=docs/schema
if [ -e "$out/mysqldump-no-data.sql" ] && [ "$force" -ne 1 ]; then
    echo "dump-schema: $out already has a dump; use --force to replace it" >&2
    exit 1
fi

password="${MYSQL_PWD:-}"
if [ -z "$password" ]; then
    if [ ! -r /dev/tty ]; then
        echo "dump-schema: no terminal to ask for the password on; set MYSQL_PWD instead" >&2
        exit 1
    fi
    read -rsp "Password of $user@$host for $database: " password </dev/tty
    echo >&2
fi
unset MYSQL_PWD

umask 077
cnf="$(mktemp)"
tmp="$(mktemp -d)"
cleanup() { rm -f "$cnf"; rm -rf "$tmp"; }
trap cleanup EXIT
case "$password" in *[\"\\$'\n']*) echo "dump-schema: a password with a quote, a backslash or a line end cannot be handed over safely; use another account for this" >&2; exit 1 ;; esac
printf '[client]\nuser=%s\npassword="%s"\nhost=%s\nport=%s\n' "$user" "$password" "$host" "$port" >"$cnf"
unset password

echo "Reading the structure of $database ..." >&2
mysqldump --defaults-extra-file="$cnf" --no-data --skip-comments --skip-lock-tables --single-transaction --no-tablespaces "$database" >"$tmp/dump.sql"
mysql --defaults-extra-file="$cnf" --batch --skip-column-names "$database" -e 'SELECT migration FROM migrations ORDER BY id' >"$tmp/migrations.txt"
mysql --defaults-extra-file="$cnf" --batch --skip-column-names "$database" -e 'SELECT VERSION()' >"$tmp/server-version.txt"

# What was written must be a schema of the reference panel, and nothing but a schema.
if grep -qE '^(INSERT|REPLACE)[[:space:]]' "$tmp/dump.sql"; then
    echo "dump-schema: the dump has rows in it; nothing was written" >&2
    exit 1
fi
tables="$(grep -c '^CREATE TABLE' "$tmp/dump.sql" || true)"
if [ "$tables" -lt 30 ]; then
    echo "dump-schema: only $tables tables found; this does not look like the panel's database. Nothing was written" >&2
    exit 1
fi
for table in users servers nodes allocations api_keys migrations; do
    grep -q "^CREATE TABLE \`$table\`" "$tmp/dump.sql" || { echo "dump-schema: no table $table; nothing was written" >&2; exit 1; }
done
[ -s "$tmp/migrations.txt" ] || { echo "dump-schema: the migrations table is empty; nothing was written" >&2; exit 1; }
[ -s "$tmp/server-version.txt" ] || { echo "dump-schema: the server gave no version; nothing was written" >&2; exit 1; }

mkdir -p "$out"
mv "$tmp/dump.sql" "$out/mysqldump-no-data.sql"
mv "$tmp/migrations.txt" "$out/migrations.txt"
mv "$tmp/server-version.txt" "$out/server-version.txt"

echo "Wrote $out/: $tables tables, $(wc -l <"$out/migrations.txt" | tr -d ' ') migrations, server $(tr -d '\n' <"$out/server-version.txt")." >&2
echo "Look through the three files before you commit them: they hold the structure only, but they are yours to check." >&2
