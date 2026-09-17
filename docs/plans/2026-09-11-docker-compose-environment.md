# Complete development environment with Docker Compose

Approved plan for ENV-COMPOSE-001. This succeeds the external-instance setup approach in [PostgreSQL Creation Scripts](2026-09-10-postgresql-creation-scripts.md); existing migrations remain unchanged.

**Status:** Delivered. A later live Compose functional run passed as part of LOCAL-AUTHORIZATION-VERIFICATION-001, superseding the original Docker-engine availability blocker. Architect and security approvals are recorded; formal QA closeout approval is not recorded.

## Outcome

Start PostgreSQL, initialize both databases, and launch the Go base server with `docker compose up --build`. Provide one compose.yaml and one multi-stage Dockerfile. Authentication endpoints and card operations remain deferred.

## Containers and initialization

- postgres: PostgreSQL 17.11, health check, persistent volume, control and shard databases.
- db-init: one-shot PowerShell 7/psql image reusing bootstrap, migration runner and seeds.
- app: Go 1.27.1 build, non-root execution, published at localhost:8080.
- Enforce healthy PostgreSQL, then successful initialization, then application startup. Preserve migration checksums, locking, transactions, Test Bank and existing test accounts/passwords. Repeated initialization preserves modified accounts.

## Server and configuration

- Root Go module, standard-library HTTP server and pgx database pools.
- GET /health/live returns 200 with status ok. GET /health/ready checks both connections within two seconds total and returns 200/ready or 503/not_ready without credentials/internal errors.
- Configure HTTP timeouts, small pools, and graceful SIGTERM shutdown.
- Provision separate connection accounts with ci_routing_reader and ci_business_runtime. Administrator credentials stay limited to database/initialization services.
- Supply development defaults and optional .env.example overrides. Technical database accounts differ from application test accounts.
- Keep PostgreSQL internal to the Compose network; publish API at 127.0.0.1:8080. Exclude .local, .git and unrelated build inputs.
- Document startup, logs, shutdown, rebuilds, and repeated initialization. Normal down preserves the volume; deletion is explicit.

## Validation and completion

- Validate Compose, builds and clean-volume startup; verify databases, migrations, users and health endpoints.
- Repeat startup; verify persistence, no duplicate seeds and preservation of changed passwords.
- Confirm failed initialization blocks startup, database outages cause readiness 503 and recovery restores readiness.
- Test runtime ownership/RLS restrictions, server behavior and existing database integration suite with Go 1.27.1.
- Obtain architect, automated-test, QA and security approvals and update documentation/logs.

## Assumptions

Local development environment only. Docker Desktop must be running for live container evidence; it was unavailable during planning. Technical testing credentials are documented outside this plan and the development log.
