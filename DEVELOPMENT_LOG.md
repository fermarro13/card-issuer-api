# Development Log

## 2026-09-16 — LOCAL-AUTHORIZATION-VERIFICATION-001: Local authorization verification environment

- User · Scope and plan approval: Implement [Local Authorization Verification Environment](docs/plans/2026-09-15-local-authorization-verification.md) so a fresh local Compose stack shares development credential state and runs the mTLS authorization scenario.
- Orchestrator · Boundaries: The development vault is non-durable, internal-only, and accepted only in `development` or `test`; production remains external-vault only. Generated PKI remains ignored and local-only.
- Implementation · Delivered: internal development-vault HTTP client/handler and non-durable `cmd/devvault`; explicit development-vault configuration; `cmd/localpki`; TLS authorization health probing and file-based fingerprint mapping; Compose PKI/vault/authorization wiring; unskipped authorization functional contract; Compose smoke coverage; and local/Postman instructions.
- Automated tests · PASSED: `go version` reports `go1.27.1 windows/amd64`; `go test -count=1 ./...`, `go vet ./...`, `go test -race ./...`, and `git diff --check` pass.
- Compose acceptance · PASSED: rebuilt the Compose images, recreated authorization, and ran `docker compose --profile functional run --build --rm functional-tests`; all three functional tests passed, including the mTLS authorization vault boundary.
- Orchestrator · Status: Implementation delivered; architect, QA, and security gates remain pending independent review.

## 2026-09-15 — ISSUER-AUTHORIZATION-VAULT-001: Issuer authorization vault and credential verifier

- User · Scope: Add a PCI-scoped credential-verification boundary for banks to check an issuer card's credentials and lifecycle eligibility.
- User · Decisions: Use a separate vault/HSM boundary, a test-only non-production adapter, mTLS bank authentication, masked account-number display only, and credential-plus-lifecycle decisions without financial authorization.
- Orchestrator · Plan: [Issuer Authorization Vault and Credential Verifier](docs/plans/2026-09-15-issuer-authorization-vault-and-credential-verifier.md).
- Implementation · Delivered: PCI credential-vault port; development/test-only keyed in-memory adapter; masked-display-only card persistence; one-time issuance/replacement disclosures with sanitized replays; tenant-scoped authorization decisions; and a separate TLS 1.3 client-certificate authorization binary.
- Automated tests · PASSED: `go version` reports `go1.27.1 windows/amd64`; focused vault, mTLS-handler, configuration, and integration tests plus `go test ./...` pass.
- Orchestrator · Status: Implementation delivered. Production external-vault/HSM and certificate/CA provisioning remain external prerequisites; QA and security review are pending.

## 2026-09-14 — BATCH-EXECUTOR-001: Batch executor

- User · Scope: Implement the remaining independently deployable executor daemon from the card-issuer architecture, including batch dispatch, lease recovery, daily expiry, and separate container deployment.
- User · Decisions: Dedicated least-privilege executor database account; explicit unique executor identity; bounded transient retries; required tuning configuration; internal-only operational interface; current-date-only UTC expiry catch-up; per-bank round-robin; all nonterminal due cards; allow-listed failure code plus curated generic summary.
- User · Plan persistence: Withdraw the earlier no-persistence exception for this feature.
- Orchestrator · Plan: [Batch Executor](docs/plans/2026-09-14-batch-executor.md), updated for the committed `internal/domain` and feature-specific service/port refactor.
- Architect · Gate: APPROVED. Required boundaries: append-only least-privilege executor migrations; consumer-owned ports with no HTTP/internal-resource dependency; per-claim lease-version fencing on every guarded completion, requeue, recovery, and expiry-item write; one all-or-nothing shard transaction for each public batch; live user/role/assignment/route revalidation before each attempt; and a separately configured, hardened, internal-only executor runtime. Control unavailability is retryable without card writes; authorization loss is terminal without card writes.
- Orchestrator · Status: Architect gate recorded; implementation in progress.
- Implementation · Delivered: separate `cmd/executor` daemon, executor-owned ports/shard adapter, shared domain lifecycle validation, fenced batch and expiry claims/recovery/results, live directory/routing authorization, expiry scheduling, sanitized diagnostics/metrics, dedicated least-privilege runtime role, append-only migrations, hardened internal Compose service, and verification coverage.
- Automated tests · PASSED: `go version` reports `go1.27.1 windows/amd64`; `go test -count=1 ./...`; `go test -race ./...` with GCC; `go vet ./...`; and `git diff --check` all pass.
- Database acceptance · PASSED: `CI_TEST_DATABASE=1` PostgreSQL executor integration acceptance and the database harness pass. They exercise tenant isolation, runtime grants, stale-worker fencing, concurrent claim exclusion, lease recovery, lost-acknowledgement safety, atomic batch application, expiry aggregates, retry exhaustion, and idempotent manual retry.
- Compose acceptance · PASSED: the isolated Docker Compose smoke/functional suite passes API and executor independent lifecycle checks, explicitly named multi-replica executor identities, persistence, initialization gating, non-root execution, and PostgreSQL outage/recovery.
- QA · APPROVED: fresh independent QA review after the final corrections, including ignored-operation terminalization/auditing and mixed/invalid multi-card batch atomicity.
- Security · APPROVED: fresh independent security review after the final corrections, including bounded expiry scheduling, completed-run reopening only for newly eligible cards, executor least-privilege denial coverage, RLS isolation, and fenced parameterized SQL.
- Orchestrator · Status: All required BATCH-EXECUTOR-001 gates passed; feature complete.

## 2026-09-14 — CARD-STATUS-BATCH-API-001: Card-status batch API

- User · Scope: Implement the remaining public API endpoints described in the card-issuer architecture.
- Orchestrator · Scope decision: The unimplemented public surface is the card-status batch API; existing card/expiry-retry routes are retained and executor work remains separate.
- Orchestrator · Plan: [Card-status Batch API](docs/plans/2026-09-14-card-status-batch-api.md).
- Architect · Gate: APPROVED. Required boundaries: trusted authenticated bank routing, tenant-scoped parameterized repository operations, canonical actor-bound idempotency, atomic draft evidence, immutable membership, and no schema changes.
- User · Exception: Do not persist future approved implementation plans; this explicit instruction waives plan-file persistence to reduce token use. Existing plans remain immutable; revisit if the user withdraws this exception.
- Implementation · Verified: authenticated bank-scoped create, list, read, item-list, execute, cancel, and linked-retry batch endpoints are present; draft construction, idempotency, audit evidence, and parameterized tenant-scoped repository operations are implemented without schema changes.
- Automated tests · PASSED: Go 1.27.1; `go test -count=1 ./...` and `go vet ./...` pass. Handler coverage includes create, execute, cancel, retry, and read-only-role rejection.
- QA and Security · Status: Independent review remains required before feature closeout; the database-backed batch acceptance matrix has not been run in this verification session.
- Orchestrator · Status: Implementing.

## 2026-09-13 — PUBLIC-RESOURCE-API-001: Public resource and card command API

- User · Scope: Implement the public resource API, card lifecycle commands, and manual expiry-item retry described in the approved plan.
- User · Decisions: Use a resumable bank-provisioning saga, seven-day idempotency replay, stable external keys, active/suspended-only replacement with one outstanding successor, one configured shard, and no issuer-operator continuity guard.
- Orchestrator · Plan: [Public Resource and Card Command API](docs/plans/2026-09-13-public-resource-api.md).
- Architect · Gate: APPROVED. Required boundaries: append-only migrations, auth-runtime-only control writes, authenticated active-route resolution, transaction-local tenant context, resumable provisioning, and sanitized durable idempotency replay.
- Implementation · Delivered: control/shard `002` migrations; authenticated resource routes; cursor signing; tenant-scoped repositories; bank provisioning; user/reference administration; card issue/lifecycle/replacement; and manual expiry retry.
- Automated tests · Evidence: `go version` reported Go 1.27.1; `go test ./...`, `go vet ./...`, standalone database tests, and `git diff --check` passed. An isolated password-enforcing PostgreSQL 17 run passed `CI_TEST_DATABASE=1 go test -count=1 -v ./internal/integration`, including the public-resource HTTP contract.
- Security · Gate: APPROVED. Re-reviewed target/actor-bound idempotency, resumable bank PATCH, tenant isolation, parameterization, privilege boundaries, and secret exclusion.
- QA · Gate: BLOCKED. The API integration path passes, but required coverage for the remaining resource/staff/provisioning/idempotency/lifecycle/manual-retry matrix is incomplete.
- Orchestrator · Status: Implementation delivered; closeout blocked by the QA coverage gate.

## 2026-09-10 — DB-SCRIPTS-001: PostgreSQL creation scripts

- User · Scope: Implement the approved full PostgreSQL 17 schema, versioned runner, separate test seeds, four application roles, and verified testing credentials.
- Orchestrator · Plan: [PostgreSQL Creation Scripts](docs/plans/2026-09-10-postgresql-creation-scripts.md).
- Architect · Gate: APPROVED. Reviewed schema boundaries, migration locking/checksums, role separation, RLS, token constraints, and deferred application responsibilities.
- Implementation · Delivered: [database scripts](database/README.md), control/shard migrations, bootstrap, versioned PowerShell runner, test seeds, schema reference, credentials document, verification SQL, and Go integration harness.
- Automated tests · Gate: PASSED with Go 1.27.1 and PostgreSQL 17.11; complete suite passed in 20.934s with no skips. `go vet` and module verification passed.
- QA · Gate: APPROVED. Verified installation/reruns, concurrent setup, checksum rejection, rollback, seed recovery/collisions, RLS, permissions, relationship constraints, and local transaction atomicity.
- Security · Gate: APPROVED. Verified immutable operation attribution, tenant-context boundaries, runtime privilege restrictions, append-only audit/history access, authentication constraints, and safe bootstrap refusal.
- Orchestrator · Status: Complete. Authentication endpoints, lifecycle workers, remote credential effects, retention, and application authorization remain explicitly deferred.

Concise, append-only record of material project interactions and decisions. Do not include PAN, CVV, secrets, tokens, credentials, or unnecessary cardholder data.

## 2026-09-10 — HARNESS-001: Development governance established

- User · Decision: Adopt an orchestrator-led development harness for the Card Issuer API.
- User · Gates: Require architect, automated-test, QA, and security approval before feature completion.
- User · Exception authority: Only the user may authorize a failed-gate exception.
- User · Logging: Record concise decisions and milestones in this file.
- Orchestrator · Status: Harness definitions created in `.agents/`.

## 2026-09-10 — HARNESS-002: Go toolchain governance established

- User · Decision: Require Go 1.27.1 for all Go implementation, formatting, analysis, tests, and review evidence.
- Orchestrator · Gate: Require `go version` evidence and passing relevant automated tests from Go 1.27.1 before feature completion.
- User · Upgrade policy: Adopt newer Go releases only through a dedicated, fully reviewed maintenance feature.
- Orchestrator · Status: Toolchain policy added to the agent harness.

## 2026-09-10 — HARNESS-003: Plan persistence established

- User · Decision: Persist every generated implementation plan in `docs/plans/` as project memory.
- Orchestrator · Rule: Save an approved plan before implementation and link it from the related feature log entry.
- Orchestrator · Status: Existing session plans preserved in `docs/plans/`.

## 2026-09-10 — HARNESS-004: Plan index established

- User · Decision: Maintain `docs/plans/INDEX.md` as the navigable index for persisted plans.
- Orchestrator · Rule: Add each approved plan to the index when it is saved, and keep its status current.

## 2026-09-10 — TOOLING-001: cc-skills installed

- User · Request: Install `github:samber/cc-skills` for this project.
- Orchestrator · Result: Marketplace installation was unavailable in the local Codex CLI; installed the documented project-local fallback at `.agents/skills/cc-skills`.
- Orchestrator · Version: `cc-skills` 2.4.0, source commit `f6dad7c`.

## 2026-09-10 — DB-DESIGN-001: Database design document

- User · Scope: Save the consolidated database design as Markdown only, including asynchronous atomic card-status batches and entity-based shard routing.
- User · Decisions: Independent clients per bank; bank-owned external accounts; virtual cards; one initial data shard; one target status per batch.
- Orchestrator · Plan: [Save the Database Design Document](docs/plans/2026-09-10-database-design-document.md).
- Orchestrator · Status: Documentation preparation; no infrastructure, executable migrations, or application code authorized by this delivery.
- Documentation · Delivered: [Database Design](docs/database-design.md), consolidating the domain model, three diagrams, keys/indexes, audit boundaries, atomic asynchronous batches, shard routing, partitioning, and unresolved implementation decisions.
- Documentation · Review: Checked relationship/card ownership, optional batch membership for standalone operations, rollback/failure records, worker fencing, uncertain commits, and the boundary between database success and remote effects.
- Documentation · Validation: Automated checks passed for 10 sections, three fenced Mermaid blocks, consistent Markdown tables, required batch/scalability coverage, and six local links across the document and project-history files.
- Orchestrator · Status: Documentation delivered. No runtime feature or database was implemented; Go tests and independent implementation/QA/security gates are not claimed by this documentation result.

## 2026-09-10 — DB-DESIGN-002: Authentication and authorization design

- User · Scope: Extend the database design document only; no authentication implementation or database changes.
- User · Decisions: Four fixed issuer/bank operator/read-only roles; local provisioned users; issuer operators administer users; bank operators may request all supported status changes including permanent closure.
- User · Token policy: Rotating refresh tokens, expiry-based access JWT revocation, and current-user authorization checks before each batch execution attempt.
- Orchestrator · Plan: [Database Authentication and Authorization](docs/plans/2026-09-10-database-authentication-authorization.md), a follow-on to DB-DESIGN-001; original approved plan preserved.
- Orchestrator · Status: Documentation preparation.
- Documentation · Delivered: [Database Design](docs/database-design.md), extending domain tables, diagrams, central authentication storage/indexes, four-role permissions, JWT/refresh behavior, actor attribution, shard selection, and batch execution authorization.
- Documentation · Defaults: Argon2id password hashes; 15-minute access JWTs; seven-day non-sliding refresh families; multiple sessions; no access-token denylist; role/bank/password changes invalidate refresh sessions through `auth_version` while issued JWTs retain privileges until expiry.
- Documentation · Review: Checked tenant scope versus central staff identity, same-session token ancestry, replay revocation commit behavior, audit-retention references, and the non-atomic authorization checkpoint before shard execution.
- Documentation · Validation: Structure checks passed for 11 numbered sections, four balanced Mermaid blocks, consistent tables, required authentication/batch coverage, and local links. SHA-256 comparison confirmed the original approved design plan is unchanged.
- Orchestrator · Status: Documentation delivered. No runtime authentication, migrations, or infrastructure implemented; runtime tests and independent implementation gates are not claimed.

## 2026-09-11 — ENV-COMPOSE-001: Complete development environment

- User · Scope: One Compose environment and one multi-stage Dockerfile for PostgreSQL, initialization, and a Go base server with health endpoints.
- Orchestrator · Plan: [Docker Compose Development Environment](docs/plans/2026-09-11-docker-compose-environment.md), succeeding the standalone external-instance setup while retaining its migrations.
- Architect · Gate: APPROVED; initialization sequencing, separate runtime identities, bounded health checks, and deferred business endpoints reviewed.
- Implementation · Delivered: Compose services, Docker build targets, Go server/pools/health endpoints, runtime-login provisioning, development configuration, documentation, and isolated Compose smoke script.
- Automated tests · Evidence: Go 1.27.1 server and SCRAM PostgreSQL 17.11 integration tests passed, including credential preservation, restricted logins, readiness outage/recovery and privilege guards. Original database suite passed; vet, module checks, Compose configuration and PowerShell parsing passed. Both migration hashes remain unchanged.
- Security · Gate: APPROVED for reviewed source and available PostgreSQL evidence; container execution remains unverified.
- QA · Gate: BLOCKED pending actual image builds and isolated Compose smoke tests. Docker Desktop cannot start its WSL2 engine because virtualization/Virtual Machine Platform is unavailable (HCS_E_HYPERV_NOT_INSTALLED).
- Orchestrator · Status: Implementation delivered; Docker-dependent acceptance remains pending. No failed-gate exception or full feature completion claimed. [Review evidence](docs/reviews/2026-09-11-compose-environment.md).

## 2026-09-13 — AUTH-API-001: Authentication endpoints

- User · Scope: Implement login, refresh, logout, current-user, and password-change endpoints using the existing control authentication schema.
- User · Decisions: Application-owned Ed25519 JWT key pair; user summaries contain username, status, and role; new passwords require 13 characters with uppercase, number, and symbol.
- User · Exception: Authentication POST endpoints do not require idempotency keys because secret-response replay storage is out of scope and conflicts with refresh replay revocation.
- Orchestrator · Plan: [Authentication Endpoints](docs/plans/2026-09-13-authentication-endpoints.md).
- Architect · Gate: BLOCKED. The saved plan transcribed the user-approved refresh-cookie name incorrectly; corrected to `__Host-card-issuer-refresh` before implementation.
- Architect · Gate: APPROVED. Required boundaries: distinct auth/routing/shard pools, exact refresh-cookie contract, EdDSA-only JWT validation, User-then-session locks, and redacted audit evidence.
- Implementation · Delivered: dedicated `ci_app_auth` runtime identity/pool; Ed25519 JWTs; Argon2id password verification/change; rotating refresh cookies; request IDs/problem responses; endpoint, concurrency, and redaction tests; and updated Compose/documentation.
- Automated tests · Evidence: `go version` reported Go 1.27.1; `go vet ./...` and `go test -count=1 ./...` passed. PostgreSQL integration coverage is present but skipped when `CI_TEST_DATABASE` is unset.
- Docker verification · PASSED: an isolated Compose smoke lifecycle and a disposable PostgreSQL 17 environment verified live login, refresh, current-user, and logout contracts; all verification resources were removed afterward.
- Automated tests · PASSED: `CI_TEST_DATABASE=1 go test -count=1 -v ./...` passed unskipped with Go 1.27.1 against a disposable PostgreSQL 17 instance. This exercised `ci_app_auth`, session/refresh rotation and replay handling, disabled/version/expiry rejection, password invalidation, audit redaction, runtime account restrictions, and readiness recovery. `go vet ./...` and `git diff --check` also passed.
- Security · Gate: APPROVED. JWT, cookie, token rotation, parameterized SQL, audit redaction, and isolation boundaries reviewed; production still requires the PostgreSQL integration run and protected JWT-key configuration.
- QA · Gate: BLOCKED pending `CI_TEST_DATABASE=1 go test -count=1 -v ./...` against disposable PostgreSQL 17. This must exercise live `ci_app_auth` grants, transactions, triggers, and concurrency before closeout.
- QA · Gate: Re-review requested with the unskipped PostgreSQL 17 evidence; the earlier execution blocker is resolved.
- QA · Gate: APPROVED. The unskipped PostgreSQL 17 suite covers authentication session/audit behavior, rotation/replay concurrency, disabled/version/expiry rejection, password invalidation, runtime-account restrictions, and recovery paths.
- Security · Gate: APPROVED. Re-reviewed the Docker test adapters and expired-session fixture: secret values are inherited by name rather than command arguments, SQL travels over stdin, and the immutable-session trigger is bypassed only within the uniquely named disposable test database.
- Orchestrator · Status: Complete. Architect approval, Go 1.27.1 test evidence, QA approval, and security approval are recorded.
- Orchestrator · Status: Implementation delivered; closeout blocked by the required unskipped PostgreSQL integration gate.

## Entry template

## YYYY-MM-DD — FEATURE-###: Short title

- Actor · Event: Concise decision, assignment, outcome, or blocker.
- Orchestrator · Status: Intake | Design approved | Implementing | Test gate passed | QA approved | Security approved | Complete | Blocked.
