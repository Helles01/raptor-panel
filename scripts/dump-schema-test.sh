#!/usr/bin/env bash
# Tests scripts/dump-schema.sh with stand-ins for mysqldump and mysql, in a folder of its own (nothing in the repository is touched).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
sandbox="$(mktemp -d)"
trap 'rm -rf "$sandbox"' EXIT
mkdir -p "$sandbox/scripts" "$sandbox/bin"
cp "$here/dump-schema.sh" "$sandbox/scripts/"

# The stand-ins write what they were asked for from the files named in MODE_*, and record the configuration file they were given.
cat >"$sandbox/bin/mysqldump" <<'EOF'
#!/usr/bin/env bash
cnf="${1#--defaults-extra-file=}"
cp "$cnf" "$SANDBOX_SEEN" 2>/dev/null || true
printf "%s" "$cnf" >"$SANDBOX_CNFPATH"
printf '%s\n' "$*" >"$SANDBOX_ARGS"
cat "$SANDBOX_DUMP"
EOF
cat >"$sandbox/bin/mysql" <<'EOF'
#!/usr/bin/env bash
case "$*" in
    *"SELECT migration"*) cat "$SANDBOX_MIGRATIONS" ;;
    *"SELECT VERSION"*) echo "10.11.6-MariaDB" ;;
esac
EOF
chmod +x "$sandbox/bin/mysqldump" "$sandbox/bin/mysql"
export PATH="$sandbox/bin:$PATH" SANDBOX_SEEN="$sandbox/seen" SANDBOX_ARGS="$sandbox/args"
export SANDBOX_CNFPATH="$sandbox/cnfpath"
export SANDBOX_DUMP="$sandbox/dump" SANDBOX_MIGRATIONS="$sandbox/migrations"

make_dump() { # tables to leave out
    : >"$SANDBOX_DUMP"
    for t in users servers nodes allocations api_keys migrations $(seq -f 'extra_%g' 1 30); do
        case " $* " in *" $t "*) continue ;; esac
        printf 'CREATE TABLE `%s` (\n  `id` int NOT NULL\n);\n' "$t" >>"$SANDBOX_DUMP"
    done
}
make_dump
printf '2016_01_23_195641_add_allocations_table\n2016_01_23_203639_add_nodes_table\n' >"$SANDBOX_MIGRATIONS"

fail=0
check() { # description, command that must succeed
    if ! eval "$2" >/dev/null 2>&1; then echo "FAIL: $1"; fail=1; else echo "ok:   $1"; fi
}
run() { MYSQL_PWD="$1" bash "$sandbox/scripts/dump-schema.sh" "${@:2}" 2>"$sandbox/stderr"; }

check "writes the three files" 'run "p4ss" --user ada olddb && [ -s "$sandbox/docs/schema/mysqldump-no-data.sql" ] && [ "$(wc -l <"$sandbox/docs/schema/migrations.txt")" -eq 2 ] && grep -q MariaDB "$sandbox/docs/schema/server-version.txt"'
check "the password is not an argument" '! grep -q p4ss "$SANDBOX_ARGS"'
check "the password reached the client in a file" 'grep -q "password=\"p4ss\"" "$SANDBOX_SEEN"'
check "the configuration file is gone afterwards" '[ -s "$SANDBOX_CNFPATH" ] && [ ! -e "$(cat "$SANDBOX_CNFPATH")" ]'
check "refuses to replace without --force" '! run "p4ss" olddb'
check "replaces with --force" 'run "p4ss" --force olddb'

rm -rf "$sandbox/docs"
printf 'INSERT INTO `users` VALUES (1);\n' >>"$SANDBOX_DUMP"
check "refuses a dump with rows and writes nothing" '! run "p4ss" olddb && [ ! -e "$sandbox/docs/schema/mysqldump-no-data.sql" ]'

make_dump users
check "refuses a dump without the users table" '! run "p4ss" olddb && [ ! -e "$sandbox/docs/schema" ]'

printf 'CREATE TABLE `a` (`id` int);\n' >"$SANDBOX_DUMP"
check "refuses a dump of a few tables" '! run "p4ss" olddb && [ ! -e "$sandbox/docs/schema" ]'

make_dump
: >"$SANDBOX_MIGRATIONS"
check "refuses an empty migrations table" '! run "p4ss" olddb && [ ! -e "$sandbox/docs/schema" ]'

printf 'x\n' >"$SANDBOX_MIGRATIONS"
check "refuses a password that cannot be handed over" '! run "pa\"ss" olddb'
check "refuses an odd host" '! run "p4ss" --host "a b" olddb'
check "refuses an option as a database name" '! run "p4ss" -- -rf'
check "refuses no database" '! run "p4ss"'

exit "$fail"
