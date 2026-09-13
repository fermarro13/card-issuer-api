#!/bin/sh
# Isolated Docker Compose acceptance test for Linux and macOS.
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
app_port=18080

usage() { printf '%s\n' 'Usage: sh scripts/Test-Compose.sh [--app-port PORT]'; }
while [ "$#" -gt 0 ]; do
    case "$1" in
        --app-port) app_port=${2-}; shift 2 ;;
        --help|-h) usage; exit 0 ;;
        *) printf 'Unknown or incomplete option: %s\n' "$1" >&2; exit 1 ;;
    esac
done
printf '%s' "$app_port" | grep -Eq '^[0-9]+$' && [ "$app_port" -ge 1024 ] && [ "$app_port" -le 65535 ] || { printf '%s\n' 'App port must be between 1024 and 65535.' >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { printf '%s\n' 'docker is required.' >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { printf '%s\n' 'Docker Compose v2 is required.' >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { printf '%s\n' 'curl is required.' >&2; exit 1; }

if command -v uuidgen >/dev/null 2>&1; then
    suffix=$(uuidgen | tr -d '-' | tr '[:upper:]' '[:lower:]')
else
    suffix=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
fi
project=ci-smoke-$suffix
printf '%s' "$project" | grep -Eq '^ci-smoke-[0-9a-f]{32}$' || { printf '%s\n' 'Unable to create a safe Compose project name.' >&2; exit 1; }

compose() {
    APP_PORT=$app_port \
    CONTROL_DATABASE=card_issuer_control \
    SHARD_DATABASE=card_issuer_shard_01 \
    SHARD_ID=shard_01 \
    POSTGRES_PASSWORD='Dev-Postgres-Admin!2026' \
    CONTROL_DB_PASSWORD='Dev-Control-Reader!2026' \
    SHARD_DB_PASSWORD='Dev-Shard-Runtime!2026' \
    docker compose --project-name "$project" --project-directory "$root" --env-file "$root/.env.example" -f "$root/compose.yaml" "$@"
}

fail() { printf '%s\n' "$*" >&2; exit 1; }
assert_equal() { [ "$1" = "$2" ] || fail "$3"; }
query() {
    database=$1
    statement=$2
    result=$(printf '%s\n' "$statement" | compose exec -T postgres psql -X -w -qAt -v ON_ERROR_STOP=1 -U postgres -d "$database") || fail 'Database verification failed.'
    printf '%s' "$result" | tr -d '\r\n'
}
wait_http() {
    path=$1
    expected=$2
    deadline=$(( $(date +%s) + 60 ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        status=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 4 "http://127.0.0.1:$app_port$path" || true)
        [ "$status" = "$expected" ] && return 0
        sleep 1
    done
    fail "$path did not return HTTP $expected within 60 seconds."
}
cleanup() {
    if printf '%s' "$project" | grep -Eq '^ci-smoke-[0-9a-f]{32}$'; then
        compose down --volumes --remove-orphans >/dev/null 2>&1 || true
    fi
}
trap cleanup 0 1 2 15

compose config --quiet
printf '%s\n' 'Building and starting a fresh isolated Compose project...'
compose up --build -d >/dev/null
wait_http /health/live 200
wait_http /health/ready 200
assert_equal "$(query card_issuer_control 'SELECT count(*) FROM control.users;')" 4 'Expected four staff accounts.'
assert_equal "$(query card_issuer_control 'SELECT count(*) FROM ci_meta.schema_migrations;')" 1 'Control migration missing.'
assert_equal "$(query card_issuer_shard_01 'SELECT count(*) FROM ci_meta.schema_migrations;')" 1 'Shard migration missing.'
app_id=$(compose ps -q app | tr -d '\r\n')
app_user=$(docker inspect --format '{{.Config.User}}' "$app_id")
assert_equal "$app_user" '10001:10001' 'Application must run as non-root.'
assert_equal "$(query postgres "SELECT count(*) FROM pg_roles WHERE rolname IN ('ci_app_control','ci_app_shard') AND NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolbypassrls OR rolreplication);")" 2 'Runtime privilege attributes are incorrect.'

printf '%s\n' 'Checking retained data and passwords through down/up...'
query card_issuer_control "UPDATE control.users SET password_hash=password_hash||'changed' WHERE normalized_username='bank_operator';" >/dev/null
before=$(query card_issuer_control "SELECT md5(password_hash) FROM control.users WHERE normalized_username='bank_operator';")
compose down >/dev/null
compose up -d >/dev/null
wait_http /health/ready 200
assert_equal "$(query card_issuer_control "SELECT md5(password_hash) FROM control.users WHERE normalized_username='bank_operator';")" "$before" 'Restart reset staff password.'
assert_equal "$(query card_issuer_control 'SELECT count(*) FROM control.authentication_audit_events;')" 4 'Restart duplicated test accounts/audits.'

printf '%s\n' 'Checking liveness during a PostgreSQL outage and readiness recovery...'
compose stop postgres >/dev/null
wait_http /health/live 200
wait_http /health/ready 503
compose start postgres >/dev/null
wait_http /health/ready 200

printf '%s\n' 'Checking that failed initialization blocks a newly starting app...'
checksum=$(query card_issuer_control 'SELECT checksum FROM ci_meta.schema_migrations WHERE version=1;')
query card_issuer_control "UPDATE ci_meta.schema_migrations SET checksum='intentional-test-corruption' WHERE version=1;" >/dev/null
compose rm --stop --force app db-init >/dev/null
if compose up -d app >/dev/null 2>&1; then
    fail 'Initialization failure was ignored.'
fi
[ -z "$(compose ps --status running -q app | tr -d '\r\n')" ] || fail 'App ran despite failed initialization.'
printf '%s' "$checksum" | grep -Eq '^[0-9a-f]{64}$' || fail 'Unexpected migration checksum.'
query card_issuer_control "UPDATE ci_meta.schema_migrations SET checksum='$checksum' WHERE version=1;" >/dev/null
compose rm --force db-init >/dev/null
compose up -d >/dev/null
wait_http /health/ready 200
printf '%s\n' 'PASS: clean startup, persistence, initialization gates, non-root execution and outage recovery.'
