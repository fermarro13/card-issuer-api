# Batch Executor

**Feature:** `BATCH-EXECUTOR-001`  
**Status:** Complete. Architect, QA, security, automated-test, database, and Compose acceptance gates passed.
**Follow-on:** [Card-status Batch API](2026-09-14-card-status-batch-api.md)

## Goal

Deliver the independently deployable `cmd/executor` daemon defined in the card-issuer architecture. It must execute review-approved public card-status batches, recover expired leases, and perform daily per-card expiry processing without exposing a second business API or communicating with the API process.

## Implementation changes

- Add `cmd/executor` and an `internal/executor` application package. Follow the current domain/service/port structure: consume narrow interfaces defined by the executor, reuse `internal/domain` records, and keep PostgreSQL code in dedicated repository adapters. Do not revive the removed `internal/resource` package.
- Add an executor shard adapter beside the existing `BatchStore` and `CardStore`. It must establish transaction-local tenant context, use parameterized SQL, and expose only work claims, fenced completions/requeues, batch outcome writes, expiry-run/item work, and required card/history/operation/audit writes.
- Reuse the existing `controlrepository.Store.User` and `routingrepository.Store.Banks` through executor-owned ports; do not route through HTTP. Before every public-batch attempt, check the submitter's current enabled status, role, and bank assignment. Authorization loss fails the batch without card writes; unavailable control data is a retryable dependency failure.
- Move the card transition decision currently private to `internal/card` into a shared domain-level lifecycle validator. API card commands and executor batch/expiry work must share it.
- Dispatch batches fairly with deterministic per-shard round-robin across active routed banks, honoring configured global and per-bank concurrency. Claim and complete work with `SELECT ... FOR UPDATE SKIP LOCKED`, lease owner, expiry, and fencing version checks. A stale worker must be unable to commit results.
- Execute public batches atomically: lock cards in canonical UUID order; record already-target cards as ignored; otherwise apply every card, operation, item, history, audit, and aggregate update in one transaction. Invalid transitions fail the batch with no card updates. Classified transient failures requeue with configured bounded backoff; exhausted retries fail terminally.
- At daemon startup and each UTC-day boundary, ensure only the current date's expiry run exists for every active bank on the shard; never backfill historical dates. Select due cards in every nonterminal state (`pending`, `issued`, `active`, `suspended`). Process items independently, preserve the fixed three automatic retries after the initial failure, and retain the existing manual-retry semantics.
- Add allow-listed failure codes plus curated generic summaries to authorized batch and expiry read representations. Never return raw database, authorization, or internal error text; keep identifiers, cardholder data, and secrets out of logs and metrics.
- Add executor configuration separate from API authentication configuration. Require explicit executor identity, operational address, control/shard credentials, polling interval, claim/per-bank concurrency, lease duration, batch retry/backoff limits, batch-size cap, expiry backoff, and drain timeout. Validate values and do not embed tuning defaults.
- Add a dedicated `ci_executor_runtime` group/login and append-only control/shard migrations granting only executor-required access. Update bootstrap, Compose runtime account provisioning, environment documentation, and database-role verification. Do not edit existing migration files.
- Extend the Docker build with a separate executor binary/image target and add an internal-network-only Compose `executor` service. It gets its own required unique `EXECUTOR_ID`, health check, shutdown grace period, and restart lifecycle; do not publish its operational port to the host.
- Expose only executor `GET /health/live`, `GET /health/ready`, and Prometheus `GET /metrics`. Readiness checks control/shard dependencies. Emit identifier-free counters/gauges for claims, outcomes, retries, recoveries, loop health, and in-flight work.

## Tests and acceptance criteria

- Unit tests cover config validation, round-robin selection, concurrency caps, retry classification/backoff, authorization revalidation, deterministic locking, atomic batch outcomes, fencing, lease recovery, graceful draining, and current-date expiry scheduling with an injected clock.
- PostgreSQL integration tests prove executor-only privileges, tenant isolation, concurrent claim exclusion, stale-worker rejection, recovery after lease expiry or lost acknowledgement, no duplicate batch application, expiry aggregate correctness, retry exhaustion, and idempotent manual retry.
- Extend Compose and the existing `functional-tests` profile to verify that API and executor start, become ready, stop, and run independently. Exercise multi-replica behavior using distinct explicit identities.
- Record Go 1.27.1 evidence with `go version`, focused executor tests, `go test -count=1 ./...`, `go test -race ./...`, and `go vet ./...` before QA/security review.

## Decisions and boundaries

- Scope includes batch dispatch, lease recovery, and expiry scheduling; provider integrations, outbox dispatch, and public executor business routes remain out of scope.
- Executor uses a dedicated least-privilege database account and an explicitly injected unique identity per replica.
- Transient failures have bounded configurable retries. Public failures expose an allow-listed code and generic summary only.
- Executor tuning is mandatory configuration everywhere; the expiry schedule is fixed at 00:00 UTC and catches up only the current date.
- This plan reflects the committed `internal/domain` and feature-specific service/port refactor, plus the functional Compose test profile.

## Required gates

1. Architect approval of the persistence, fencing, transaction, authorization, and deployment design.
2. Implementation with Go 1.27.1 test evidence.
3. Independent QA and security approval before closeout.
