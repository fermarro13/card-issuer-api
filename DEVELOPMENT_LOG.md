# Development Log

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

## Entry template

## YYYY-MM-DD — FEATURE-###: Short title

- Actor · Event: Concise decision, assignment, outcome, or blocker.
- Orchestrator · Status: Intake | Design approved | Implementing | Test gate passed | QA approved | Security approved | Complete | Blocked.
