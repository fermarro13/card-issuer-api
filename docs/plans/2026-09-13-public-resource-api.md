# PUBLIC-RESOURCE-API-001 — Public resource and card command API

**Status:** Delivered; security approved. Closeout remains blocked on the documented QA coverage gate.

## Goal

Implement the documented public resource API, card lifecycle commands, and the manual expiry-item retry command using authenticated, tenant-scoped PostgreSQL access.

## Implementation changes

- Add issuer-only bank-directory and staff administration endpoints; add bank-scoped reference-data, card, card-history, and operation endpoints with role-appropriate read/write access.
- Add signed cursor pagination, request validation, standard problem responses, and durable idempotency with a seven-day replay window for every state-changing endpoint.
- Add control and shard `002` migrations for administrative idempotency, narrow routing-write grants, and safe replay data while preserving existing migration history.
- Route only after authentication, require active routes on the configured initial shard, establish transaction-local tenant context, and provision banks through a resumable non-routable saga.
- Implement synchronous card issue, replacement, lifecycle transitions, and manual expiry retries with transactionally committed operations, histories where applicable, audits, and idempotency evidence.
- Keep bank/product/external reference keys immutable; omit local credential references and all sensitive credential data from public responses and logs.

## Test scenarios

- Contract-test authentication/role boundaries, JSON validation, request IDs, problem responses, secret exclusion, pagination/cursor tampering, and idempotency replay/conflict.
- Integration-test routing/RLS isolation, resumable bank provisioning, staff authorization-version/audit behavior, reference-data constraints, lifecycle validation, replacement activation, manual expiry retry, and transaction atomicity.
- Validate migrations and runtime grants, then run formatting, `go vet ./...`, normal tests, and the disposable PostgreSQL 17 integration suite using Go 1.27.1.

## Assumptions

- Only the configured initial `shard_01` is supported; multi-shard configuration, batch API/executor work, scheduled expiry processing, and expiry-run reads remain out of scope.
- Issuer operators may alter or disable their own account, including the last enabled issuer operator.
- Replacement is limited to active or suspended predecessors and permits at most one outstanding issued successor.
