#!/bin/sh
# Portable database bootstrap, migration, and test-fixture runner.
set -eu

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd)
action=Migrate
psql=psql
control_database=card_issuer_control
shard_database=card_issuer_shard_01
admin_database=postgres
shard_id=shard_01
migration_root=$script_dir/migrations

usage() {
    cat <<'EOF'
Usage: sh database/Invoke-Database.sh [options]

  --action Bootstrap|Migrate|SetupTest
  --psql PATH
  --control-database NAME
  --shard-database NAME
  --admin-database NAME
  --shard-id ID
  --migration-root PATH
EOF
}

fail() { printf '%s\n' "$*" >&2; exit 1; }

while [ "$#" -gt 0 ]; do
    case "$1" in
        --action) action=${2-}; shift 2 ;;
        --psql) psql=${2-}; shift 2 ;;
        --control-database) control_database=${2-}; shift 2 ;;
        --shard-database) shard_database=${2-}; shift 2 ;;
        --admin-database) admin_database=${2-}; shift 2 ;;
        --shard-id) shard_id=${2-}; shift 2 ;;
        --migration-root) migration_root=${2-}; shift 2 ;;
        --help|-h) usage; exit 0 ;;
        *) fail "Unknown or incomplete option: $1" ;;
    esac
done

case "$action" in Bootstrap|Migrate|SetupTest) ;; *) fail 'Action must be Bootstrap, Migrate, or SetupTest.' ;; esac
printf '%s' "$control_database" | grep -Eq '^[a-zA-Z_][a-zA-Z0-9_]{0,62}$' || fail 'Invalid control database name.'
printf '%s' "$shard_database" | grep -Eq '^[a-zA-Z_][a-zA-Z0-9_]{0,62}$' || fail 'Invalid shard database name.'
printf '%s' "$admin_database" | grep -Eq '^[a-zA-Z_][a-zA-Z0-9_]{0,62}$' || fail 'Invalid administrative database name.'
printf '%s' "$shard_id" | grep -Eq '^[a-zA-Z0-9_-]{1,63}$' || fail 'Invalid shard ID.'
[ "$control_database" != "$shard_database" ] || fail 'Control and shard database names must differ.'
[ "$admin_database" != "$control_database" ] && [ "$admin_database" != "$shard_database" ] || fail 'Application databases must differ from the administrative database.'
command -v "$psql" >/dev/null 2>&1 || fail "psql executable not found: $psql"

if command -v sha256sum >/dev/null 2>&1; then
    hash_file() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
    hash_file() { shasum -a 256 "$1" | awk '{print $1}'; }
else
    fail 'A SHA-256 utility is required: sha256sum (Linux) or shasum (macOS).'
fi

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/card-issuer-db.XXXXXX") || fail 'Unable to create a temporary directory.'
cleanup() { rm -rf "$work_dir"; }
trap cleanup EXIT HUP INT TERM

invoke_sql_file() {
    database=$1
    file=$2
    "$psql" -X --no-password --set=ON_ERROR_STOP=1 --set=VERBOSITY=terse "--dbname=$database" "--file=$file"
}

invoke_migrations() {
    database=$1
    kind=$2
    migration_dir=$migration_root/$kind
    set -- "$migration_dir"/*.sql
    [ -f "$1" ] || fail "No $kind migrations found."

    metadata_unsorted=$work_dir/$kind-metadata-unsorted
    metadata=$work_dir/$kind-metadata
    : > "$metadata_unsorted"
    for file in "$migration_dir"/*.sql; do
        name=$(basename "$file")
        version=$(printf '%s\n' "$name" | sed -nE 's/^([0-9][0-9][0-9][0-9]*)_[a-z0-9_]+\.sql$/\1/p')
        [ -n "$version" ] || fail "Invalid migration filename: $name"
        if grep -q "^$version|" "$metadata_unsorted"; then
            fail "Duplicate migration version $version"
        fi
        normalized=$work_dir/$name
        sed 's/$//' "$file" > "$normalized"
        hash=$(hash_file "$normalized")
        printf '%s|%s|%s|%s\n' "$version" "$name" "$normalized" "$hash" >> "$metadata_unsorted"
    done
    LC_ALL=C sort -t '|' -k1,1n "$metadata_unsorted" > "$metadata"
    known_versions=
    while IFS='|' read -r version name normalized hash; do
        if [ -z "$known_versions" ]; then known_versions=$version; else known_versions=$known_versions,$version; fi
    done < "$metadata"

    sql=$work_dir/$kind-migrations.sql
    cat > "$sql" <<EOF
SET ROLE ci_owner;
SELECT pg_advisory_lock(170017,2);
DO \$\$ BEGIN
  IF current_setting('server_version_num')::int < 170000 THEN RAISE EXCEPTION 'PostgreSQL 17 or newer is required'; END IF;
END \$\$;
BEGIN;
CREATE SCHEMA IF NOT EXISTS ci_meta AUTHORIZATION ci_owner;
REVOKE ALL ON SCHEMA ci_meta FROM PUBLIC;
CREATE TABLE IF NOT EXISTS ci_meta.database_identity (singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), kind text NOT NULL);
INSERT INTO ci_meta.database_identity(singleton,kind) VALUES (true,'$kind') ON CONFLICT DO NOTHING;
DO \$\$ BEGIN
  IF (SELECT kind FROM ci_meta.database_identity) <> '$kind' THEN RAISE EXCEPTION 'Database migration kind mismatch'; END IF;
END \$\$;
CREATE TABLE IF NOT EXISTS ci_meta.schema_migrations (version bigint PRIMARY KEY, filename text NOT NULL, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());
COMMIT;
DO \$\$ BEGIN
  IF EXISTS (SELECT FROM ci_meta.schema_migrations WHERE version NOT IN ($known_versions)) THEN
    RAISE EXCEPTION 'Applied migration is missing from this checkout';
  END IF;
END \$\$;
EOF

    while IFS='|' read -r version name normalized hash; do
        cat >> "$sql" <<EOF
DO \$\$ BEGIN
  IF EXISTS (SELECT FROM ci_meta.schema_migrations WHERE version=$version AND (checksum<>'$hash' OR filename<>'$name')) THEN
    RAISE EXCEPTION 'Applied migration changed: $name';
  END IF;
  IF NOT EXISTS (SELECT FROM ci_meta.schema_migrations WHERE version=$version) AND EXISTS (SELECT FROM ci_meta.schema_migrations WHERE version>$version) THEN
    RAISE EXCEPTION 'Out-of-order migration: $name';
  END IF;
END \$\$;
EOF
    done < "$metadata"

    while IFS='|' read -r version name normalized hash; do
        cat >> "$sql" <<EOF
SELECT EXISTS(SELECT FROM ci_meta.schema_migrations WHERE version=$version) AS applied \gset
\if :applied
  \echo 'Already applied: $kind/$name'
\else
  BEGIN;
EOF
        cat "$normalized" >> "$sql"
        cat >> "$sql" <<EOF
  INSERT INTO ci_meta.schema_migrations(version,filename,checksum) VALUES ($version,'$name','$hash');
  COMMIT;
\endif
EOF
    done < "$metadata"
    printf '%s\n' 'SELECT pg_advisory_unlock(170017,2);' >> "$sql"
    invoke_sql_file "$database" "$sql"
}

if [ "$action" = Bootstrap ] || [ "$action" = SetupTest ]; then
    bootstrap=$work_dir/bootstrap.sql
    {
        printf "\\set control_db '%s'\n" "$control_database"
        printf "\\set shard_db '%s'\n" "$shard_database"
        cat "$script_dir/bootstrap.sql"
    } > "$bootstrap"
    invoke_sql_file "$admin_database" "$bootstrap"
fi

if [ "$action" = Migrate ] || [ "$action" = SetupTest ]; then
    invoke_migrations "$control_database" control
    invoke_migrations "$shard_database" shard
fi

if [ "$action" = SetupTest ]; then
    invoke_sql_file "$shard_database" "$script_dir/seeds/shard.sql"
    seed=$work_dir/control-seed.sql
    {
        printf "\\set shard_id '%s'\n" "$shard_id"
        cat "$script_dir/seeds/control.sql"
    } > "$seed"
    invoke_sql_file "$control_database" "$seed"
    printf '%s\n' 'Test setup complete. Application passwords: database/TEST-CREDENTIALS.md'
fi
