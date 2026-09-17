# CARD-STATUS-BATCH-API-001 — Card-status batch API

**Status:** Delivered. Independent QA/security review and database-backed batch acceptance remain pending before closeout.

## Goal

Implement the remaining public business endpoints in `docs/card-issuer-architecture.md`: reviewable card-status batch drafts, their reads, explicit dispatch, cancellation, and linked retry drafts.

## Implementation changes

- Add authenticated, bank-scoped HTTP routes for creating, listing, reading, inspecting items, executing, cancelling, and retrying card-status batches.
- Extend the resource and shard-repository boundaries with typed batch representations and parameterized, transaction-scoped operations.
- Create draft headers, canonicalized membership, queued per-card operations, audit evidence, and idempotency records atomically. Reuse the existing shard schema and tenant context; make no schema changes.
- Enforce issuer-operator or bank-operator authority only for batch mutations; retain the existing trusted bank-routing checks for every batch read and write.
- Bind idempotency to actor and batch action, canonicalize card IDs before fingerprinting, and return a conflict for a reused key with different semantic input.
- Implement dispatch, cancellation, and retry state transitions in their own tenant-scoped transactions. Retried work is a fresh linked draft with copied immutable request data and membership.

## Test scenarios

- HTTP contracts for role/bank authorization, validation, request IDs, problem responses, pagination/cursors, and no secret or credential exposure.
- Create idempotency replay/conflict, canonical reordered memberships, duplicate/cross-bank rejection, immutable draft reads, and queued-operation creation.
- Execute idempotency, draft-to-queued dispatch, repeat execution, cancellation boundaries, and failed/cancelled retry provenance.
- Repository/integration evidence for tenant isolation, parameterized queries, transaction rollback, and persisted audit/idempotency records.

## Assumptions

- “Remaining API endpoints” means the unimplemented public card-status batch endpoints. Existing authentication, reference-data, card-command, and manual expiry-retry endpoints are not rewritten; the executor daemon is separate application work, not a public business API endpoint.
- The initial maximum draft size is 200 card IDs, matching the documented API page maximum until a load-tested executor configuration supplies a different bounded limit.
- Batch target statuses are `active`, `suspended`, or `closed`; issuing, replacement, and expiry remain dedicated workflows.
- An empty JSON object is the normalized retry-command request body; its idempotency key is still required.

## Design approval

Architect review: **APPROVED**. The design keeps authorization and shard selection server-side, applies tenant context before every read/write, uses only parameterized SQL, co-commits draft evidence, and defers execution to the existing durable PostgreSQL queue contract. Executor claiming/fencing and scheduled expiry remain out of this feature’s scope.
