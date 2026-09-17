# Card Issuer API Architecture and Security Summary

## Purpose and conclusion

This document explains the main architectural choices behind the Card Issuer API proof of concept, the reasoning for its asynchronous batch-processing design, and the security and compliance improvements that should be addressed before a production deployment.

The project is intentionally structured as a modular, multi-tenant platform. Its main strengths are clear component ownership, isolated operational scaling, durable and recoverable batch execution, and role-based tenant access. The local Docker Compose environment is appropriate for development and functional testing, but it is not a production security baseline.

## Architecture decisions

### Separate components with clear responsibilities

The solution separates the public API, the executor daemon, and the credential-vault boundary. The API accepts authenticated requests and manages synchronous business workflows. The executor performs background batch work and scheduled card expiry. The vault boundary owns sensitive card credentials and credential verification.

This separation follows the single-responsibility principle at the component level. It also makes deployment and scaling more practical: a high API request rate can be addressed by scaling API instances, while a growing backlog can be handled by scaling executor instances or adjusting their controlled concurrency. A resource issue in one component does not require scaling or restarting the others.

The components are independently deployable and use separate runtime configuration and least-privilege database identities. The executor does not expose business endpoints or call the API process; it works directly against the durable database state that the API creates.

### PostgreSQL as the asynchronous handoff layer

Asynchronous work in this proof of concept is limited to card-status batches and scheduled expiry. For that scope, PostgreSQL is sufficient as the handoff layer between the API and executor. When an approved batch is dispatched, the API persists a queued record and returns a `202 Accepted` response. The executor polls eligible records, claims them with a lease and fencing version, and records the final outcome.

This design deliberately avoids introducing RabbitMQ or another message broker. A broker would add operational complexity without a proportional benefit while the only background workload is durable database-backed card state processing. PostgreSQL already provides the needed durability, transaction boundaries, locking, recovery of expired leases, and audit evidence. The API and executor remain decoupled because neither needs to invoke the other directly.

The approach also supports safe horizontal executor scaling. Work claiming uses database locking and lease ownership so that competing executors do not apply the same batch twice. Card records are locked in a deterministic order, and public card-status batches are atomic: either all applicable changes commit, or the batch fails without a partial state change. Retries are bounded and use backoff. A stale worker cannot commit after its lease has been taken over by another worker.

A message broker or transactional outbox should be reconsidered if the system later needs to publish events to external providers, support several independent consumers, absorb much larger backlogs, or guarantee delivery across multiple external systems.

### Automated card-expiry job

The executor also runs a scheduled expiry job at `00:00 UTC` each day. For every active bank on its assigned shard, it ensures that the current day's expiry run exists and identifies eligible cards whose expiry date has been reached. It does not backfill historical runs; it only manages the current UTC date. This makes the scheduling behavior predictable and avoids creating an unexpected backlog after a long outage.

Unlike a public card-status batch, an expiry run is intentionally processed one card at a time. Each card receives its own transaction, operation record, lifecycle-history record, audit evidence, and outcome. A temporary failure for one card therefore does not prevent other eligible cards from expiring. Before every attempt, the executor rereads and locks the card, confirming that it is still eligible and has not already been expired by another valid workflow.

The job records clear outcomes such as `expired`, `skipped_already_expired`, or `manual_retry_required`. After an initial failure, an item can receive up to three automatic retries with bounded backoff. If it still cannot be processed, automatic retries stop and an authorized issuer operator can create an explicit manual retry. This preserves visibility of unresolved work, prevents endless retries, and keeps a complete audit trail of automated and human actions.

Multiple executor replicas remain safe because expiry items are claimed with a lease owner and fencing version. If an executor stops unexpectedly, another executor can recover expired leases; a stale executor cannot later commit a result after it has lost ownership. This provides durable, recoverable background processing without needing a separate scheduler or message queue.

### Database strategy: control database and tenant shards

The database design separates shared platform information from tenant business data. The control database contains the platform directory, bank routing metadata, staff users, authentication sessions, refresh tokens, and authentication audit records. It answers questions such as which bank a user may access and which shard stores that bank's operational data.

Each tenant bank has its business data in a shard database. A shard holds bank-specific records such as card products, clients, account references, cards, lifecycle history, batch work, and tenant audit evidence. In the proof of concept, there is one shard database, but the routing model is designed to support additional shards as the platform grows.

For a request, the API first validates the JWT and role. It then derives the permitted bank context from server-side claims and uses the control database to resolve the bank's assigned shard. The application opens the appropriate tenant-scoped repository transaction and sets tenant context locally for that transaction. PostgreSQL row-level security and restricted runtime roles provide an additional guard so a query cannot read or modify another bank's data merely because a caller supplied a different identifier.

This split has practical benefits. Authentication and routing remain centralized even when a bank moves to another shard, while business data can be distributed as tenant count or workload grows. It also narrows the impact of a shard-level incident and avoids making sharding itself the authorization mechanism: access is still enforced through JWT validation, roles, server-side tenant resolution, tenant-scoped queries, and database policy.

Cross-database foreign keys are intentionally avoided. The control database stores routing and identity information, and each shard remains authoritative for its own business relationships. Bank provisioning must complete its local shard setup before the control directory makes that bank routable. This keeps the system consistent without pretending that transactions can span separate databases automatically.

### Credential-vault security boundary

Keeping credential handling outside the public API reduces the exposure of PAN and CVV data. The API persists masked card display data and uses the vault only for narrow provisioning, verification, and revocation operations. One-time credential disclosure is designed to limit repeated exposure, and the separate authorization service uses TLS 1.3 with client certificates for its local verification flow.

In production, the vault should be an external, hardened secret-management or HSM-backed service. It should be reachable only through authenticated, encrypted connections, ideally with mutual TLS, strict network segmentation, auditable access policies, key rotation, and controls appropriate to the organization's PCI DSS scope. This lets the vault receive stricter controls without imposing the same operational burden on every application component.

### Docker Compose for the proof of concept and Kubernetes for production

Docker Compose was selected for local development and testing because it starts the API, executor, PostgreSQL, development vault, local PKI, authorization service, and functional-test container together. Each service is defined independently, which makes local deployment, inspection, and testing straightforward without requiring a host installation of Go or PostgreSQL.

For production, Kubernetes is a more suitable orchestration platform. Whether deployed on premises or through a managed service such as Amazon EKS, Kubernetes provides stronger service orchestration, replica management, rolling deployment, health-based recovery, resource limits, secret integration, and network-policy enforcement. Docker Compose should remain a local development and test tool rather than the production platform.

### Functional tests in Compose

Pytest was chosen for functional API testing because it is flexible for calling HTTP endpoints, keeping test credentials, and verifying persisted PostgreSQL results. Packaging it as an optional Compose service makes the functional suite easy to run consistently on a local machine, with no separate Python installation required.

The test container is also easy to decouple later: the same tests can run in a CI pipeline against disposable infrastructure, with credentials supplied through the pipeline's secret mechanism. Production-like CI should run integration tests explicitly, rather than relying only on the default unit-test command.

## Authentication authorization and tenant isolation

The API uses short-lived JWT access tokens and rotating refresh tokens. Refresh tokens are stored as hashes, refresh rotation detects replay, and access tokens expire after a short lifetime. Refresh cookies are marked `Secure`, `HttpOnly`, and `SameSite=Strict`.

Authorization uses four roles and follows least privilege:

- `issuer_operator` and `issuer_readonly` operate across the card-issuer platform according to their permissions.
- `bank_operator` and `bank_readonly` are assigned to one bank and are constrained to that tenant.

This allows one cloud-hosted SaaS platform to operate the shared infrastructure while serving many banks as tenants. Tenant selection is derived from authenticated server-side context; tenant-scoped repositories, PostgreSQL row-level security, transaction-local tenant context, and restricted runtime roles provide defense in depth. A bank selector supplied by a client is never treated as authoritative by itself.

## Idempotency and concurrent requests

The project uses idempotency keys so that repeated commands do not create duplicate records or duplicate business operations. This is essential under concurrent load and covers common real-world cases such as a user retrying after an error, a front-end submitting twice, transient network failures, or retries introduced by a proxy.

Request fingerprints are protected with HMAC-SHA-256 rather than a plain hash. This is especially important for password-bearing administrative requests because a plain persisted hash could otherwise become a fast offline password-guessing verifier. Batch membership is canonicalized before fingerprinting, so the same set of cards has the same meaning even when its input order changes.

Idempotency is complemented by transactional persistence, deterministic locking, leases, fencing, bounded retries, and current authorization checks before the executor applies public batch work. Together, these controls make duplicate application and stale-worker updates substantially less likely.

## Observed security and compliance gaps

The following items are known improvement opportunities in the repository. Addressing them would further raise the project's security, operational maturity, and overall quality; however, they were not included in the scope of this delivery and remain planned follow-up work before production use with real customer or cardholder data. The login controls below are an exception: the listed application-layer protections are now implemented, while gateway and monitoring controls remain production work.

| Gap | Evidence and impact | Recommended production action |
| --- | --- | --- |
| Public API uses HTTP | The Compose API is published on `http://localhost:8080`, and the API process listens without TLS. HTTP can expose bearer tokens, request data, and responses if deployed beyond a tightly controlled local environment. | Terminate TLS at an API gateway or load balancer, redirect HTTP to HTTPS, enforce modern TLS settings, and use HSTS where appropriate. |
| Development secrets are committed and production checks are incomplete | Compose and `.env.example` include deliberately public development database passwords, a test vault key, a JWT private key, and HMAC keys. The code rejects the known development idempotency key in production, but it does not similarly reject the known development JWT or cursor key. | Store all production secrets in a managed secret system, use distinct per-environment values, rotate them, restrict access, and fail startup if known development keys or passwords are configured in production. |
| Database transport encryption is not enforced for production | The local environment uses `PGSSLMODE=disable`; configuration accepts that value even when `APP_ENV=production`. | Require `verify-full` or an equivalent validated TLS mode in production, manage the database CA securely, and reject insecure connection modes at startup. |
| Development vault is unauthenticated HTTP | The development vault is intentionally internal-only, non-durable, and restricted to development/test, but its protocol uses HTTP and has no client authentication. It must never become a production vault. | Integrate a production vault or HSM with TLS/mTLS, workload identity, authorization policies, audit logging, key lifecycle controls, and network isolation. |
| Login admission controls are local to each API instance | The API defaults to five login attempts per direct source IP per one-minute window, configured by `LOGIN_ATTEMPT_LIMIT_PER_MINUTE`, and returns `429 login_rate_limited` with `Retry-After` when it rejects an attempt. The development Compose stack sets it to `100` for its shared functional-test source IP. It rejects passwords longer than 1,024 bytes before authentication work, and performs an approved-parameter dummy Argon2id verification for unknown or disabled users. The in-memory limit is intentionally bounded and does not aggregate replicas; proxy headers are not trusted by the application. | Enforce coordinated per-client and account-aware limits, WAF/DDoS controls, and abuse monitoring at the trusted API gateway. Do not rely on this direct-peer limiter to identify individual clients behind a proxy. |
| Executor metrics have no handler-level authentication | `GET /metrics` is served without authentication. Compose keeps the executor on an internal network, but network placement alone is not a complete production access-control policy. | Restrict metrics with Kubernetes network policies and a dedicated monitoring identity, or add authenticated scrape protection; ensure metrics never contain cardholder data, secrets, or tenant identifiers. |
| Production operational controls are not yet implemented | The repository documents API gateway, load balancer, cloud deployment, production PKI, and external vault/HSM integration as follow-up work. | Define a production reference architecture with ingress/WAF, DDoS and rate controls, centralized logging, SIEM integration, vulnerability management, backup/restore testing, incident response, and compliance evidence. |
| Integration assurance is not a default CI gate | Database-backed integration tests require explicit configuration and are skipped in the ordinary root test command; the separate database test module is also not included by that command. | Make disposable PostgreSQL integration tests, race tests, dependency vulnerability scans, and SAST part of required CI gates before deployment. |

## Production readiness priority

Before a real deployment, the minimum priority is to introduce HTTPS and a gateway, replace every development secret, enforce database TLS, integrate a production vault/HSM, protect operational endpoints, and complete gateway-level login controls. The next layer should establish Kubernetes deployment controls, centralized observability, vulnerability and dependency scanning, disaster-recovery testing, and PCI DSS scope assessment with the organization's compliance team.

The current design is a strong proof-of-concept foundation because the critical boundaries already exist. Production readiness mainly requires hardening those boundaries, making insecure configuration impossible, and operating the system with the controls expected for sensitive financial workloads.
