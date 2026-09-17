# Card Issuer Architecture

## Purpose

This document defines the target application architecture for Card Issuer API: its public HTTP API, its independently deployed batch-executor daemon, and the contracts between them and PostgreSQL.

**Implementation status (2026-09-16):** The API, `cmd/executor`, `cmd/authorization`, and development-only shared vault/PKI Compose topology are implemented. Authentication and batch-executor closeout gates are complete. Public-resource QA coverage remains blocked; card-status-batch QA/security and database-backed acceptance remain pending; local authorization independent architect, QA, and security review remains pending. Production authorization still requires an externally provisioned PCI-compliant vault/HSM and PKI.

The target system remains one Go module with shared internal domain packages. It is designed to be deployed as two binaries:

| Component | Command | Responsibility |
| --- | --- | --- |
| API | `cmd/api` | Authenticates staff, authorizes requests, routes to a bank shard, serves reads, manages reference data, creates reviewable batch drafts, and executes synchronous local card-status commands. |
| Executor | `cmd/executor` | Polls and executes triggered batches, schedules expiry, and recovers expired leases. |

The executor is deliberately not a second public business API. It has only operational health and metrics endpoints. Both binaries use the same trusted shard-routing, tenant-context, repository, lifecycle-validation, audit, and configuration packages.

## Architectural decisions and trade-offs

### Database-backed work, no broker in v1

A batch draft is reviewable database state. Calling its execute endpoint moves it to `queued`; the executor then polls and claims it from PostgreSQL. PostgreSQL is both the durable source of truth and the work queue for batches. Public card-status batches are atomic; daily system expiry runs are a distinct per-card workflow and deliberately permit partial completion.

This avoids operating RabbitMQ before there is a need for fan-out, independent consumers, or cross-service delivery guarantees. It also avoids the dual reliability problem of persisting a batch and publishing a message. The trade-off is bounded polling delay and the need to tune database polling fairly as load grows.

The existing outbox remains available for future integrations. It is not required to wake a batch worker or process card issue/replacement in v1.

### Separate processes, shared Go packages

Running the API and executor as separate binaries is ordinary Go practice for workloads with different lifecycles. The API can scale for request latency while executor replicas scale for queue depth. A worker crash cannot exhaust HTTP capacity, and worker permissions/configuration can be constrained independently.

They must not communicate through a private HTTP API. They coordinate through durable PostgreSQL records, row locks, leases, and fencing versions.

### Explicit review before execution

Batch construction and execution are different commands:

1. Create an immutable `draft` with a card list, target status, and reason.
2. Review it through the normal batch read endpoints.
3. Explicitly trigger execution when ready.

This supports approval/review workflows without allowing draft membership or intent to change invisibly. A changed list, reason, or target status is a new draft.

### Local synchronous commands; batches are asynchronous

Issue, replacement, activation, suspension, resumption, and closure make only local PostgreSQL changes. The API performs each individual card command synchronously in one shard transaction.

Issue and replacement provision through the PCI-scoped credential vault using the new card UUID as correlation and idempotency identity. They persist only the vault's immutable masked display with card/status/audit evidence and a sanitized idempotency result. The initial successful issuance or replacement response may contain a one-time credential disclosure; its idempotency replay never does. PAN, CVV, verification-code hashes, raw provider responses, and payment-data fingerprints are never stored, logged, audited, or put in outbox messages. If local persistence fails after vault provisioning, the issuer requests vault revocation and reconciles uncertain outcomes by card UUID.

## HTTP conventions

All business routes are under `/v1`. UUIDs are represented as lowercase canonical strings. Times are RFC 3339 UTC instants. JSON uses `application/json`; failures use `application/problem+json` with at least `type`, `title`, `status`, `code`, `detail`, and `request_id`.

Clients may supply `X-Request-ID`; otherwise the API generates one. It is returned on every response and is recorded in audit records. Every client-initiated state-changing request requires `Idempotency-Key` with 1-256 characters, except the authentication POST endpoints. Reusing a key with a different normalized request produces `409 idempotency_conflict`. Authentication flows are excluded because replaying their secret-bearing results would require encrypted response storage and would conflict with refresh-token replay revocation.

Collection responses use cursor pagination:

```json
{
  "data": [],
  "next_cursor": "opaque-cursor-or-null"
}
```

Default page size is 50 and maximum page size is 200. Cursors are opaque and ordered by immutable creation time plus ID.

API versions are additive within `/v1`; incompatible changes require `/v2`. Except for the one-time issuance/replacement disclosure in the PCI-scoped flow, the API never returns password hashes, refresh tokens, credentials, PAN, CVV, or unredacted vault responses.

## Bank authorization-verification boundary

`cmd/authorization` is a separately deployed TLS 1.3 service. It accepts only mutually authenticated bank clients at `POST /v1/authorization-verifications`; its request holds an opaque bank transaction reference and transient credential fields. The service derives the bank solely from the enabled client certificate's SHA-256 fingerprint mapping and never accepts a bank identifier in the request body. The response contains only that transaction reference, a server decision ID, and `approved` or `declined`.

The service records a tenant-scoped, unique transaction-reference decision with a nullable resolved card ID and allow-listed decision code. It stores no request body, credential, verification-code hash, or payment-data fingerprint. A duplicate reference returns the first safe decision without revalidation. Approval requires a successful vault check plus an `active`, unexpired same-bank card; every other case is a generic decline. No holds, balances, ledger entries, spending controls, fraud results, merchant data, or amounts are processed.

## Authentication and authorization

### Authentication endpoints

| Method and path | Request | Result |
| --- | --- | --- |
| `POST /v1/auth/login` | `username`, `password` | `200` with access token and user summary; sets the refresh-token cookie. |
| `POST /v1/auth/refresh` | Refresh cookie | `200` with a new access token; rotates the refresh cookie. |
| `POST /v1/auth/logout` | Refresh cookie | `204`; revokes the current refresh session and clears the cookie. |
| `GET /v1/me` | Access token | `200` with the authenticated user summary. |
| `POST /v1/me/password` | `current_password`, `new_password` | `204`; changes the caller's password and invalidates refresh families as defined by the authentication design. |

The access token is a 15-minute EdDSA bearer JWT signed by an application-owned Ed25519 key. Refresh tokens are only `__Host-card-issuer-refresh` Secure, HttpOnly, SameSite=Strict cookies (Path `/`, no Domain) and are never returned in JSON. Session rotation, replay detection, role/bank claims, and expiry-based access revocation follow `docs/database-design.md`.

### Roles and tenant selection

| Role | Scope | API permissions |
| --- | --- | --- |
| `issuer_operator` | Any bank | Full supported reference-data management, card issue/replacement/lifecycle, batches, bank-directory, and staff administration. |
| `issuer_readonly` | Any bank | All documented business reads. |
| `bank_operator` | Assigned bank only | Business reads; creation of products, clients, account references, cards, and replacements; and supported card-status changes, including batches. |
| `bank_readonly` | Assigned bank only | Business reads, history, and permitted audits. |

Every bank business path contains `{bankId}`. The API verifies the token before choosing a shard. A bank-role user may only use its assigned bank ID; an issuer-role user may select any active routed bank. Routing data, request bodies, cursors, and work records never override that trusted decision.

## Public resource API

Only `issuer_operator` may manage or read bank-directory resources and staff. Bank-scoped roles have no access to bank-directory resources, including their assigned bank; `issuer_readonly` likewise has no access because these are administrative rather than business-read resources. All roles access only the resources their role and selected bank permit.

| Resource | Endpoints | Notes |
| --- | --- | --- |
| Banks | `GET`, `POST /v1/banks`; `GET`, `PATCH /v1/banks/{bankId}` | `issuer_operator` only. A bank resource maps to the shard-local Entity and its central routing entry. No delete endpoint exists. |
| Staff users | `GET`, `POST /v1/users`; `GET`, `PATCH /v1/users/{userId}`; `POST /v1/users/{userId}:disable`; `POST /v1/users/{userId}:set-password` | Issuer operators only. Role/bank assignment changes are audited and increment authorization version as required. |
| Products | `GET`, `POST /v1/banks/{bankId}/card-products`; `GET`, `PATCH /v1/banks/{bankId}/card-products/{productId}` | Issuer and assigned bank operators may create; PATCH remains issuer-only. Product configuration is an opaque validated JSON object; updates advance configuration version. |
| Clients | `GET`, `POST /v1/banks/{bankId}/clients`; `GET`, `PATCH /v1/banks/{bankId}/clients/{clientId}` | Issuer and assigned bank operators may create; PATCH remains issuer-only. Input is an opaque `external_client_ref` and optional `display_name`. |
| Account references | `GET`, `POST /v1/banks/{bankId}/account-references`; `GET`, `PATCH /v1/banks/{bankId}/account-references/{accountId}` | Issuer and assigned bank operators may create; PATCH remains issuer-only. Input binds a client to an opaque `external_account_ref`. |
| Cards | `GET`, `POST /v1/banks/{bankId}/cards`; `GET /v1/banks/{bankId}/cards/{cardId}` | Issuer and assigned bank operators may issue. Listing supports client, account, product, and status filters. Generic card updates are not supported. |
| Card history and operations | `GET /v1/banks/{bankId}/cards/{cardId}/history`; `GET /v1/banks/{bankId}/cards/{cardId}/operations` | Append-only business evidence; no mutation routes. |

Creation and update responses return the current resource representation. Reference data is preserved rather than deleted; inactive status replaces destructive removal where applicable.

## Card commands

| Method and path | Behavior | Success |
| --- | --- | --- |
| `POST /v1/banks/{bankId}/cards` | Provisions an `issued` card through the credential vault from `client_id`, `account_reference_id`, `product_id`, and `reason`. Issuer or assigned bank operator. | `201` with card/operation summaries and a one-time credential disclosure. |
| `POST /v1/banks/{bankId}/cards/{cardId}:activate` | `issued` to `active`; when the card is a replacement, closes its predecessor in the same transaction. | `200` with card and operation. |
| `POST /v1/banks/{bankId}/cards/{cardId}:suspend` | `active` to `suspended`. | `200` with card and operation. |
| `POST /v1/banks/{bankId}/cards/{cardId}:resume` | `suspended` to `active`. | `200` with card and operation. |
| `POST /v1/banks/{bankId}/cards/{cardId}:close` | Moves an eligible nonterminal card to `closed`. | `200` with card and operation. |
| `POST /v1/banks/{bankId}/cards/{cardId}:replace` | Creates a linked `issued` successor through the credential vault. Issuer or assigned bank operator. | `201` with successor card/operation summaries and a one-time credential disclosure. |

Each command body has a required nonempty `reason`. A request that targets the card's present status is a successful ignored operation: it writes operation/audit evidence but does not alter the card, increment its version, or append status history. Other invalid transitions return `422 invalid_card_transition`.

The lifecycle is:

```text
pending --local issue/replace transaction--> issued --activate--> active --suspend--> suspended
                                             ^                        |
                                             |---------resume---------|

eligible nonterminal states --close--> closed
eligible nonterminal states --daily scheduled expiry (00:00 UTC)--> expired
```

`closed` and `expired` are terminal. At `00:00` UTC each day, the executor creates one system-attributed expiry run for due eligible cards. An expiry run is not atomic: each card is handled in its own shard transaction and has its own result: `pending`, `expired`, `skipped_already_expired`, or `manual_retry_required` with a safe failure code. A failed card receives up to three automatic retries after its initial attempt. Before every retry, the executor rereads and locks that card; it retries only if the card is still not expired, otherwise it records `skipped_already_expired`. After the third retry fails, the item is aborted as `manual_retry_required`. An `issuer_operator` may explicitly retry only that exhausted item through `POST /v1/banks/{bankId}/card-expiry-runs/{expiryRunId}/items/{itemId}:retry`; the command requires an idempotency key and nonempty reason, queues the item, and returns `202`. If that manual attempt fails, it returns to `manual_retry_required` without restarting automatic retries. Replacement leaves the predecessor unchanged. Activating the issued successor atomically closes that predecessor.

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

The executor runs three independently bounded loops:

1. **Batch dispatcher:** polls eligible `queued` batches fairly across banks, claims one using a lease owner, expiry, and incremented fencing version, then executes it.
2. **Lease recovery:** safely reclaims expired processing leases only after observing row locks and fencing rules. A stale worker cannot commit after ownership changes.
3. **Expiry scheduler:** starts the daily expiry run at `00:00` UTC, creates one system-attributed item per due eligible card, and processes items independently. It performs at most three automatic retries after an item's initial failed attempt, only while the card remains not expired; exhausted items require an explicit manual retry.

Before each batch execution attempt, the daemon reads the submitter's current enabled status, role, and bank assignment from the control database. It defers safely when central authorization is unavailable, fails the batch without card changes when permission is absent, and never relies on the originally submitted JWT.

No remote call occurs while a card-update transaction or card locks are held. A future provider integration must define its own at-least-once delivery, idempotency, and reconciliation contract before it is enabled.

### Operational interface and configuration

The executor exposes only:

| Endpoint | Meaning |
| --- | --- |
| `GET /health/live` | Process is running; no dependency check. |
| `GET /health/ready` | Control/shard pools and worker dependencies needed to claim work are reachable. |
| `GET /metrics` | Protected or network-restricted Prometheus metrics. |

Important configuration includes executor identity, control/shard URLs, polling interval, maximum concurrent claims, per-bank concurrency, lease duration, batch retry/backoff limits, batch-size cap, the fixed daily expiry schedule (`00:00` UTC), expiry-retry backoff, and graceful-drain timeout. Expiry automatic retries are fixed at three per item after the initial attempt. Values other than these settled expiry defaults remain bounded configuration, selected from load testing rather than hard-coded assumptions.

On `SIGTERM`, the executor stops claiming new work, lets bounded in-flight transactions finish, releases resources, and relies on lease recovery for interrupted work.

## Application package responsibilities

The API composes feature services explicitly: `internal/bank` owns bank directory and provisioning; `internal/staff` issuer staff administration; `internal/catalog` card products, clients, and account references; `internal/card` card reads and lifecycle workflows; and `internal/batch` card-status batch workflows. `internal/tenant` owns trusted routing decisions, while `internal/idempotency` owns request fingerprints. Each feature defines the repository and transaction capabilities it consumes; PostgreSQL adapters remain under `internal/repository`, and `internal/domain` contains only shared persistence-neutral data contracts.

## Persistence implementation status

The shard initial migration now implements the public-batch persistence model beyond the original all-or-nothing `queued`/`processing`/`succeeded`/`failed` shape:

- batch states `draft` and `cancelled`;
- item outcome `ignored` and constraints permitting an ignored item without `previous_status`;
- applied and ignored result counts on batch headers;
- an optional tenant-scoped `retry_of_batch_id` relationship;
- invariants for draft-only creation and membership, exact membership before dispatch, allowed state transitions, fenced queued-work claiming, cancellation, successor retry provenance, and terminal counts matching item outcomes;
- indexes for draft review, queued dispatch, lease recovery, and retry lookup.

It also implements a separate system expiry-run header and per-card-item model rather than reusing the atomic public status-batch result. The schema enforces one run per bank/UTC date, system `expire` operations for its items, per-item attempts/outcomes/safe failure codes, fenced claims, exact terminal aggregate counts, and retry selection only while the card is not `expired`. An item becomes `manual_retry_required` after its initial attempt and three automatic retries fail.

`docs/database-design.md` and `database/SCHEMA.md` reflect these settled initial-schema contracts. The API implements issuer-operator authorization, command idempotency, and audit co-commit for the manual-retry endpoint using those persistence primitives.

## Verification requirements

- API contract tests cover authentication, role/bank routing, error shapes, cursor behavior, idempotency replay/conflict, and secret exclusion. They verify that every bank-directory endpoint rejects `issuer_readonly`, `bank_operator`, and `bank_readonly`, including a bank operator requesting its assigned bank; only `issuer_operator` can manually retry an exhausted expiry item.
- Card tests cover synchronous vault-backed issue/replacement success, one-time credential disclosure with sanitized replay, idempotency replay/conflict, permitted/forbidden transitions, same-status no-ops, replacement activation, and scheduled expiry.
- Database-harness tests cover draft construction, exact membership before dispatch, cancellation, linked retry provenance, per-item expiry retries, terminal expiry aggregates, and rejection of a retry once the card is expired. They require a disposable PostgreSQL 17 instance and `CI_TEST_DATABASE=1` to execute the integration path.
- Automated, database, and Compose coverage now exercises contract authorization, cursor/idempotency behavior, concurrent workers, deterministic locks, lease expiry/fencing, crash recovery, fair selection, lost-acknowledgement safety, duplicate-application prevention, independent expiry outcomes, manual retry, and API/executor lifecycle behavior. The remaining feature-specific review gates are stated in the implementation status above.
- The local authorization functional contract also exercises issue → activate → mTLS verification through the shared development vault. It is development-only and does not validate a production vault/HSM or production PKI deployment.
