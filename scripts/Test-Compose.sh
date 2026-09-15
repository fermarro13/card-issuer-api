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
    AUTH_DB_PASSWORD='Dev-Auth-Runtime!2026' \
    SHARD_DB_PASSWORD='Dev-Shard-Runtime!2026' \
    EXECUTOR_DB_PASSWORD='Dev-Executor-Runtime!2026' \
    EXECUTOR_ID='executor-smoke-01' \
    AUTH_JWT_PRIVATE_KEY_B64='nWGxne/9WmC6hEr0kuwsxERJxWl7MmkZcDusAxyuf2DXWpgBgrEKt9VL/tPJZAc6DuFy89qmIyWvAhpo9wdRGg' \
    AUTH_JWT_ISSUER='card-issuer-api' \
    AUTH_JWT_AUDIENCE='card-issuer-api' \
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
    base_url=$1
    path=$2
    expected=$3
    deadline=$(( $(date +%s) + 60 ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        check="import sys, httpx; response=httpx.get('$base_url$path', timeout=4); sys.exit(0 if response.status_code == $expected else 1)"
        compose --profile functional run --rm --no-deps --entrypoint python functional-tests -c "$check" >/dev/null 2>&1 && return 0
        sleep 1
    done
    fail "$base_url$path did not return HTTP $expected within 60 seconds."
}
wait_container_health() {
    container=$1
    name=$2
    deadline=$(( $(date +%s) + 60 ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container" 2>/dev/null || true)
        [ "$status" = healthy ] && return 0
        sleep 1
    done
    fail "$name did not become healthy within 60 seconds."
}
assert_executor_identity() {
    container=$1
    identity=$2
    actual=$(docker inspect --format "{{range .Config.Env}}{{if eq . \"EXECUTOR_ID=$identity\"}}{{.}}{{end}}{{end}}" "$container") || fail 'Could not inspect executor identity.'
    assert_equal "$actual" "EXECUTOR_ID=$identity" "Executor identity was not explicitly set to $identity."
}
replica_a_name=$project-executor-replica-a
replica_b_name=$project-executor-replica-b
cleanup() {
    if printf '%s' "$project" | grep -Eq '^ci-smoke-[0-9a-f]{32}$'; then
        docker rm --force "$replica_a_name" "$replica_b_name" >/dev/null 2>&1 || true
        compose down --volumes --remove-orphans >/dev/null 2>&1 || true
    fi
}
trap cleanup 0 1 2 15

compose config --quiet
printf '%s\n' 'Building and starting a fresh isolated Compose project...'
compose up --build -d >/dev/null
wait_http http://app:8080 /health/live 200
wait_http http://app:8080 /health/ready 200
assert_equal "$(query card_issuer_control 'SELECT count(*) FROM control.users;')" 4 'Expected four staff accounts.'
assert_equal "$(query card_issuer_control 'SELECT count(*) FROM ci_meta.schema_migrations;')" 3 'Control migrations missing.'
assert_equal "$(query card_issuer_shard_01 'SELECT count(*) FROM ci_meta.schema_migrations;')" 5 'Shard migrations missing.'
app_id=$(compose ps -q app | tr -d '\r\n')
app_user=$(docker inspect --format '{{.Config.User}}' "$app_id")
assert_equal "$app_user" '10001:10001' 'Application must run as non-root.'
executor_id=$(compose ps -q executor | tr -d '\r\n')
executor_user=$(docker inspect --format '{{.Config.User}}' "$executor_id")
assert_equal "$executor_user" '10001:10001' 'Executor must run as non-root.'
assert_equal "$(query postgres "SELECT count(*) FROM pg_roles WHERE rolname IN ('ci_app_control','ci_app_auth','ci_app_shard','ci_app_executor') AND NOT (rolsuper OR rolcreatedb OR rolcreaterole OR rolbypassrls OR rolreplication);")" 4 'Runtime privilege attributes are incorrect.'

printf '%s\n' 'Checking API and executor independent lifecycle...'
compose stop executor >/dev/null
wait_http http://app:8080 /health/live 200
wait_http http://app:8080 /health/ready 200
compose start executor >/dev/null
wait_container_health "$executor_id" 'executor'
compose stop app >/dev/null
wait_container_health "$executor_id" 'executor while API is stopped'
wait_http http://executor:8090 /health/ready 200
compose start app >/dev/null
wait_http http://app:8080 /health/ready 200

printf '%s\n' 'Checking multi-replica executor identities...'
compose run -d --no-deps --name "$replica_a_name" -e EXECUTOR_ID=executor-smoke-02 executor >/dev/null
compose run -d --no-deps --name "$replica_b_name" -e EXECUTOR_ID=executor-smoke-03 executor >/dev/null
wait_container_health "$replica_a_name" 'executor replica A'
wait_container_health "$replica_b_name" 'executor replica B'
assert_executor_identity "$replica_a_name" 'executor-smoke-02'
assert_executor_identity "$replica_b_name" 'executor-smoke-03'

printf '%s\n' 'Running the Compose functional profile...'
compose --profile functional run --rm functional-tests >/dev/null

printf '%s\n' 'Checking retained data and passwords through down/up...'
query card_issuer_control "UPDATE control.users SET password_hash=password_hash||'changed' WHERE normalized_username='bank_operator';" >/dev/null
before=$(query card_issuer_control "SELECT md5(password_hash) FROM control.users WHERE normalized_username='bank_operator';")
audit_before=$(query card_issuer_control 'SELECT count(*) FROM control.authentication_audit_events;')
compose down >/dev/null
compose up -d >/dev/null
wait_http http://app:8080 /health/ready 200
assert_equal "$(query card_issuer_control "SELECT md5(password_hash) FROM control.users WHERE normalized_username='bank_operator';")" "$before" 'Restart reset staff password.'
assert_equal "$(query card_issuer_control 'SELECT count(*) FROM control.authentication_audit_events;')" "$audit_before" 'Restart duplicated authentication audits.'

printf '%s\n' 'Checking liveness during a PostgreSQL outage and readiness recovery...'
compose stop postgres >/dev/null
wait_http http://app:8080 /health/live 200
wait_http http://app:8080 /health/ready 503
compose start postgres >/dev/null
wait_http http://app:8080 /health/ready 200

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
wait_http http://app:8080 /health/ready 200
printf '%s\n' 'PASS: startup, functional profile, independent API/executor lifecycle, replica identities, persistence, initialization gates, non-root execution and outage recovery.'
