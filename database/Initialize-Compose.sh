#!/bin/sh
# Container entry point for Compose database initialization.
set -eu

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
psql=${PSQL:-psql}

value_or_default() {
    value=$(printenv "$1" 2>/dev/null || true)
    if [ -n "$value" ]; then printf '%s' "$value"; else printf '%s' "$2"; fi
}

control_database=$(value_or_default CONTROL_DATABASE card_issuer_control)
shard_database=$(value_or_default SHARD_DATABASE card_issuer_shard_01)
shard_id=$(value_or_default SHARD_ID shard_01)
for name in CONTROL_DB_PASSWORD AUTH_DB_PASSWORD SHARD_DB_PASSWORD; do
    case "$name" in
        CONTROL_DB_PASSWORD) password=${CONTROL_DB_PASSWORD-} ;;
        AUTH_DB_PASSWORD) password=${AUTH_DB_PASSWORD-} ;;
        SHARD_DB_PASSWORD) password=${SHARD_DB_PASSWORD-} ;;
    esac
    [ -n "$password" ] || { printf '%s must be nonempty.\n' "$name" >&2; exit 1; }
done

sh "$script_dir/Invoke-Database.sh" \
    --action SetupTest \
    --psql "$psql" \
    --control-database "$control_database" \
    --shard-database "$shard_database" \
    --shard-id "$shard_id"

"$psql" -X -w -q --set=ON_ERROR_STOP=1 --set=VERBOSITY=terse --dbname postgres --file "$script_dir/compose-runtime.sql"

had_pguser=${PGUSER+x}
had_pgpassword=${PGPASSWORD+x}
had_pgsslmode=${PGSSLMODE+x}
saved_pguser=${PGUSER-}
saved_pgpassword=${PGPASSWORD-}
saved_pgsslmode=${PGSSLMODE-}
restore_environment() {
    if [ "$had_pguser" = x ]; then PGUSER=$saved_pguser; export PGUSER; else unset PGUSER; fi
    if [ "$had_pgpassword" = x ]; then PGPASSWORD=$saved_pgpassword; export PGPASSWORD; else unset PGPASSWORD; fi
    if [ "$had_pgsslmode" = x ]; then PGSSLMODE=$saved_pgsslmode; export PGSSLMODE; else unset PGSSLMODE; fi
}
trap restore_environment 0 1 2 15

PGSSLMODE=disable
export PGSSLMODE
for connection in "${control_database}|ci_app_control|${CONTROL_DB_PASSWORD}" "${control_database}|ci_app_auth|${AUTH_DB_PASSWORD}" "${shard_database}|ci_app_shard|${SHARD_DB_PASSWORD}"; do
    IFS='|' read -r database user password <<EOF
$connection
EOF
    PGUSER=$user PGPASSWORD=$password "$psql" -X -w -q --set=ON_ERROR_STOP=1 --set=VERBOSITY=terse --dbname "$database" --command 'SELECT 1;' >/dev/null
done

printf '%s\n' 'Compose database initialization complete.'
