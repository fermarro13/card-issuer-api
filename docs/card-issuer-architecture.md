# Card Issuer Architecture

## Purpose

This document defines the first buildable application architecture for Card Issuer API: its public HTTP API, its independently deployed batch-executor daemon, and the contracts between them and PostgreSQL.

The system remains one Go module with shared internal domain packages. It is deployed as two binaries:

| Component | Command | Responsibility |
| --- | --- | --- |
| API | `cmd/api` | Authenticates staff, authorizes requests, routes to a bank shard, serves reads, manages reference data, creates reviewable batch drafts, and executes synchronous local card-status commands. |
| Executor | `cmd/executor` | Polls and executes triggered batches, processes credential-provider work, schedules expiry, and recovers expired leases. |

The executor is deliberately not a second public business API. It has only operational health and metrics endpoints. Both binaries use the same trusted shard-routing, tenant-context, repository, lifecycle-validation, audit, and configuration packages.

## Architectural decisions and trade-offs

### Database-backed work, no broker in v1

A batch draft is reviewable database state. Calling its execute endpoint moves it to `queued`; the executor then polls and claims it from PostgreSQL. PostgreSQL is both the durable source of truth and the work queue for batches.

This avoids operating RabbitMQ before there is a need for fan-out, independent consumers, or cross-service delivery guarantees. It also avoids the dual reliability problem of persisting a batch and publishing a message. The trade-off is bounded polling delay and the need to tune database polling fairly as load grows.

The existing outbox remains part of the design for credential-provider work and future integrations. It is not required to wake a batch worker in v1.

### Separate processes, shared Go packages

Running the API and executor as separate binaries is ordinary Go practice for workloads with different lifecycles. The API can scale for request latency while executor replicas scale for queue depth. A worker crash cannot exhaust HTTP capacity, and worker permissions/configuration can be constrained independently.

They must not communicate through a private HTTP API. They coordinate through durable PostgreSQL records, row locks, leases, and fencing versions.

### Explicit review before execution

Batch construction and execution are different commands:

1. Create an immutable `draft` with a card list, target status, and reason.
2. Review it through the normal batch read endpoints.
3. Explicitly trigger execution when ready.

This supports approval/review workflows without allowing draft membership or intent to change invisibly. A changed list, reason, or target status is a new draft.

### Local synchronous commands; remote work is asynchronous

Activation, suspension, resumption, and closure of one existing card make only local PostgreSQL changes. The API performs them synchronously in one shard transaction.

Issue and replacement require a credential provider and are therefore accepted asynchronously. The API persists a pending operation and opaque work request; the executor performs the provider call with a stable idempotency key. A local card is not reported as issued until provider confirmation is committed.

## HTTP conventions

All business routes are under `/v1`. UUIDs are represented as lowercase canonical strings. Times are RFC 3339 UTC instants. JSON uses `application/json`; failures use `application/problem+json` with at least `type`, `title`, `status`, `code`, `detail`, and `request_id`.

Clients may supply `X-Request-ID`; otherwise the API generates one. It is returned on every response and is recorded in audit records. Every client-initiated state-changing request requires `Idempotency-Key` with 1-256 characters. Reusing a key with a different normalized request produces `409 idempotency_conflict`.

Collection responses use cursor pagination:

```json
{
  "data": [],
  "next_cursor": "opaque-cursor-or-null"
}
```

Default page size is 50 and maximum page size is 200. Cursors are opaque and ordered by immutable creation time plus ID.

API versions are additive within `/v1`; incompatible changes require `/v2`. The API never returns password hashes, refresh tokens, provider credentials, PAN, CVV, or unredacted credential-provider responses.

## Authentication and authorization

### Authentication endpoints

| Method and path | Request | Result |
| --- | --- | --- |
| `POST /v1/auth/login` | `username`, `password` | `200` with access token and user summary; sets the refresh-token cookie. |
| `POST /v1/auth/refresh` | Refresh cookie | `200` with a new access token; rotates the refresh cookie. |
| `POST /v1/auth/logout` | Refresh cookie | `204`; revokes the current refresh session and clears the cookie. |
| `GET /v1/me` | Access token | `200` with the authenticated user summary. |
| `POST /v1/me/password` | `current_password`, `new_password` | `204`; changes the caller's password and invalidates refresh families as defined by the authentication design. |

The access token is a 15-minute bearer JWT. Refresh tokens are only Secure, HttpOnly, SameSite cookies and are never returned in JSON. Session rotation, replay detection, role/bank claims, and expiry-based access revocation follow `docs/database-design.md`.

### Roles and tenant selection

| Role | Scope | API permissions |
| --- | --- | --- |
| `issuer_operator` | Any bank | Full supported reference-data management, card issue/replacement/lifecycle, batches, and staff administration. |
| `issuer_readonly` | Any bank | All documented business reads. |
| `bank_operator` | Assigned bank only | Business reads and supported card-status changes, including batches. |
| `bank_readonly` | Assigned bank only | Business reads, history, and permitted audits. |

Every bank business path contains `{bankId}`. The API verifies the token before choosing a shard. A bank-role user may only use its assigned bank ID; an issuer-role user may select any active routed bank. Routing data, request bodies, cursors, and work records never override that trusted decision.

## Public resource API

Issuer operators manage banks and staff. All roles access only the resources their role and selected bank permit.

| Resource | Endpoints | Notes |
| --- | --- | --- |
| Banks | `GET`, `POST /v1/banks`; `GET`, `PATCH /v1/banks/{bankId}` | A bank resource maps to the shard-local Entity and its central routing entry. No delete endpoint exists. |
| Staff users | `GET`, `POST /v1/users`; `GET`, `PATCH /v1/users/{userId}`; `POST /v1/users/{userId}:disable`; `POST /v1/users/{userId}:set-password` | Issuer operators only. Role/bank assignment changes are audited and increment authorization version as required. |
| Products | `GET`, `POST /v1/banks/{bankId}/card-products`; `GET`, `PATCH /v1/banks/{bankId}/card-products/{productId}` | Product configuration is an opaque validated JSON object; updates advance configuration version. |
| Clients | `GET`, `POST /v1/banks/{bankId}/clients`; `GET`, `PATCH /v1/banks/{bankId}/clients/{clientId}` | Input is an opaque `external_client_ref` and optional `display_name`. |
| Account references | `GET`, `POST /v1/banks/{bankId}/account-references`; `GET`, `PATCH /v1/banks/{bankId}/account-references/{accountId}` | Input binds a client to an opaque `external_account_ref`. |
| Cards | `GET`, `POST /v1/banks/{bankId}/cards`; `GET /v1/banks/{bankId}/cards/{cardId}` | Listing supports client, account, product, and status filters. Generic card updates are not supported. |
| Card history and operations | `GET /v1/banks/{bankId}/cards/{cardId}/history`; `GET /v1/banks/{bankId}/cards/{cardId}/operations` | Append-only business evidence; no mutation routes. |

Creation and update responses return the current resource representation. Reference data is preserved rather than deleted; inactive status replaces destructive removal where applicable.

## Card commands

| Method and path | Behavior | Success |
| --- | --- | --- |
| `POST /v1/banks/{bankId}/cards` | Creates a `pending` card and queued `issue` operation from `client_id`, `account_reference_id`, `product_id`, and `reason`. Issuer operator only. | `202` with card and operation summaries. |
| `POST /v1/banks/{bankId}/cards/{cardId}:activate` | `issued` to `active`; when the card is a replacement, closes its predecessor in the same transaction. | `200` with card and operation. |
| `POST /v1/banks/{bankId}/cards/{cardId}:suspend` | `active` to `suspended`. | `200` with card and operation. |
| `POST /v1/banks/{bankId}/cards/{cardId}:resume` | `suspended` to `active`. | `200` with card and operation. |
| `POST /v1/banks/{bankId}/cards/{cardId}:close` | Moves an eligible nonterminal card to `closed`. | `200` with card and operation. |
| `POST /v1/banks/{bankId}/cards/{cardId}:replace` | Creates a linked pending successor and queued provider operation. Issuer operator only. | `202` with successor card and operation summaries. |

Each command body has a required nonempty `reason`. A request that targets the card's present status is a successful ignored operation: it writes operation/audit evidence but does not alter the card, increment its version, or append status history. Other invalid transitions return `422 invalid_card_transition`.

The lifecycle is:

```text
pending --provider confirmation--> issued --activate--> active --suspend--> suspended
                                             ^                        |
                                             |---------resume---------|

eligible nonterminal states --close--> closed
eligible nonterminal states --scheduled expiry--> expired
```

`closed` and `expired` are terminal. The executor creates system-attributed expiry operations for due cards; there is no public expire command. Replacement provisioning leaves the predecessor unchanged. Activating the confirmed successor atomically closes that predecessor.

## Batch API

### Build a reviewable batch

`POST /v1/banks/{bankId}/card-status-batches` constructs a draft.

```json
{
  "target_status": "suspended",
  "reason": "Suspected fraud review",
  "card_ids": [
    "11111111-1111-4111-8111-111111111111",
    "22222222-2222-4222-8222-222222222222"
  ]
}
```

The API rejects empty, duplicate, over-limit, malformed, inaccessible, or cross-bank card IDs. It canonicalizes membership before evaluating the idempotency fingerprint, so a reordered list has the same meaning. It persists the immutable draft header, item membership, associated queued operations, audit evidence, and creation idempotency record in one transaction, then returns `201`.

The response includes the batch state, target status, reason, item count, applied/ignored counts (initially zero), item summaries, creator, timestamps, and `links.execute`. Drafts are visible through:

- `GET /v1/banks/{bankId}/card-status-batches`
- `GET /v1/banks/{bankId}/card-status-batches/{batchId}`
- `GET /v1/banks/{bankId}/card-status-batches/{batchId}/items`

No update endpoint exists. A user makes a new draft to change membership, reason, or target status.

### Trigger execution

`POST /v1/banks/{bankId}/card-status-batches/{batchId}:execute` is the explicit approval/dispatch command. It is authorized for the original bank scope and checks that the batch is still a `draft`. In one transaction it changes the batch to `queued`, sets `next_attempt_at` to the current time, writes execution-request audit evidence, and returns `202` with the queued batch.

Repeating the command for an already queued, processing, or terminal batch returns its current representation and does not create duplicate work. The executor observes the queued row on its next bounded poll; no message broker or outbox event is needed for batch dispatch.

### Cancellation and retry

- `POST /v1/banks/{bankId}/card-status-batches/{batchId}:cancel` cancels only `draft` or unclaimed `queued` work. It is rejected once a worker has a valid processing lease.
- `POST /v1/banks/{bankId}/card-status-batches/{batchId}:retry` is allowed for terminal failed or cancelled batches. It creates a new linked draft with copied immutable request data and a new idempotency key; the client reviews and triggers the successor separately.

The original batch is never reopened, preserving its terminal result and audit evidence.

### Batch outcomes

At execution, the executor locks cards in deterministic UUID order and revalidates every non-ignored transition. An item already at target status becomes `ignored`; it creates no status-history row and does not change the card.

If all remaining items are valid, their card changes, versions, operations, item results, histories, audits, and batch result commit in one transaction. The batch is `succeeded`, with explicit applied and ignored counts.

If any non-ignored item is invalid, no card updates from that batch commit. The batch is terminal `failed`; its items are `not_applied` apart from any already identified ignored items, and failure/audit evidence is committed by the guarded failure path. The API never claims partial card application as a batch success.

## Executor daemon

### Work loops

The executor runs four independently bounded loops:

1. **Batch dispatcher:** polls eligible `queued` batches fairly across banks, claims one using a lease owner, expiry, and incremented fencing version, then executes it.
2. **Lease recovery:** safely reclaims expired processing leases only after observing row locks and fencing rules. A stale worker cannot commit after ownership changes.
3. **Credential worker:** claims outbox records for card issue/replacement, calls the provider with the logical operation ID as an idempotency key, and persists confirmed opaque credential references and outcomes.
4. **Expiry scheduler:** periodically finds due eligible cards and creates system-attributed expiry operations without duplicating an existing terminal outcome.

Before each batch execution attempt, the daemon reads the submitter's current enabled status, role, and bank assignment from the control database. It defers safely when central authorization is unavailable, fails the batch without card changes when permission is absent, and never relies on the originally submitted JWT.

No remote call occurs while a card-update transaction or card locks are held. Provider delivery is at least once; the provider contract must deduplicate by operation ID and support reconciliation after an uncertain result.

### Operational interface and configuration

The executor exposes only:

| Endpoint | Meaning |
| --- | --- |
| `GET /health/live` | Process is running; no dependency check. |
| `GET /health/ready` | Control/shard pools and worker dependencies needed to claim work are reachable. |
| `GET /metrics` | Protected or network-restricted Prometheus metrics. |

Important configuration includes executor identity, control/shard URLs, polling interval, maximum concurrent claims, per-bank concurrency, lease duration, retry/backoff limits, batch-size cap, expiry scan cadence, credential-provider timeout, and graceful-drain timeout. Values remain bounded configuration, selected from load testing rather than hard-coded assumptions.

On `SIGTERM`, the executor stops claiming new work, lets bounded in-flight transactions finish, releases resources, and relies on lease recovery for interrupted work.

## Persistence changes required before implementation

The initial migration currently models only `queued`, `processing`, `succeeded`, and `failed` batches with all-or-nothing `applied`/`not_applied` items. Add a new forward-only shard migration; never edit the applied initial migration. It must add:

- batch states `draft` and `cancelled`;
- item outcome `ignored` and constraints permitting an ignored item without `previous_status`;
- applied and ignored result counts on batch headers;
- an optional tenant-scoped `retry_of_batch_id` relationship;
- invariants for draft immutability, allowed state transitions, queued work claiming, cancellation, and successor retry provenance;
- indexes for draft review, queued dispatch, lease recovery, and retry lookup.

The implementation must update `docs/database-design.md` and `database/SCHEMA.md` where these new contract decisions supersede the initial all-items-applied batch description.

## Verification requirements

- API contract tests cover authentication, role/bank routing, error shapes, cursor behavior, idempotency replay/conflict, and secret exclusion.
- Card tests cover every permitted/forbidden transition, same-status no-ops, credential-provider failure/reconciliation, replacement activation, and scheduled expiry.
- Batch tests cover draft construction, exact card-list validation, immutability, review reads, trigger idempotency, cancellation races, linked retry, applied/ignored combinations, invalid-transition rollback, authorization loss, and central-auth outages.
- Executor integration tests cover concurrent workers, deterministic locks, lease expiry, fencing, crash/restart recovery, fair selection, lost commit acknowledgement, and no duplicate batch application.
- Deployment tests prove API and executor can start, become ready, scale, and shut down independently against the existing Compose PostgreSQL environment.
