# PostgreSQL creation scripts and default test users

Approved user plan for DB-SCRIPTS-001. Credential values are documented separately in the test credentials file, not project governance records.

## Summary

Implement the full PostgreSQL 17 design on an existing instance: control authentication/routing database and one bank data shard. Supply executable SQL, a PowerShell/psql runner, four default application test accounts, and documentation. Authentication endpoints, lifecycle execution, and workers are deferred.

## Implementation

- Separate administrative role/database bootstrap from ordered transactional migrations. Default names: card_issuer_control and card_issuer_shard_01, with overrides. Track per-database migration versions and checksums; serialize runs, reject changed migrations, and never automatically reset databases.
- Implement all 16 documented domain/authentication tables plus the routing directory, with UUID keys, timestamptz, restrictive tenant-aware foreign keys, indexes, same-client/card consistency, immutable tenant ownership, and append-only runtime audit/history access.
- Enable and force RLS on all bank-owned tables using transaction-local app.entity_id. Separate ownership, authentication-runtime, routing-reader, and business-runtime roles without runtime ownership or bypass privileges.
- Enforce the four application roles and assignments, trimmed/lowercase ASCII usernames, refresh ancestry/expiry constraints, and increasing auth_version after authorization/password/status changes.
- Constrain card states to pending, issued, active, suspended, closed, expired; replacement links a new card. Preserve documented batch/item states. Operations: queued/processing/succeeded/failed; outbox: pending/processing/delivered/failed; idempotency: processing/succeeded/failed.
- Automatically seed one bank and one enabled user per role through the test setup command, separately from migrations. Store verified independently salted Argon2id hashes (19 MiB, two iterations, parallelism one); document testing passwords separately. Use fixed IDs, preserve changed accounts on reruns, reject identity collisions, and provision shard bank before routable directory placement. No session/token/customer/card seeds.

## Validation

- Test clean/repeated installation, checksums, migration failure rollback, serialization, and interrupted seed recovery using disposable databases.
- Verify all password/hash pairs and assignments, username uniqueness, runtime isolation including pooled-context reset, cross-bank/wrong-client/mismatched-card constraints, and authentication/audit access restrictions.
- Test role validity, token ancestry/branching/expiry, auth_version updates, and transaction rollback across related card/batch records without claiming worker implementation.
- Use a small Go 1.27.1 integration harness and obtain architect, automated-test, QA, and security approvals. Persist/index this plan and log outcomes.

## Assumptions

PostgreSQL 17 and administrative access are execution prerequisites. Supply required psql and Go 1.27.1 before collecting passing evidence. Tables start unpartitioned; retention jobs, remote credential effects, benchmarks, and API permission enforcement are deferred. Application accounts are distinct from PostgreSQL connection accounts.
