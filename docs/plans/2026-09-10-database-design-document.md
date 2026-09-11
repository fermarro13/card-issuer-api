# Save the database design document

## Summary

Create `docs/database-design.md` by consolidating the database-design draft and approved batch-processing section. The deliverable is documentation only: no database infrastructure, executable migrations, or application code.

## Implementation changes

- Document PostgreSQL architecture, strict bank-owned data isolation, independent clients, and bank-owned external account references.
- Include entities, relationships, ER diagrams, keys, constraints, lifecycle operations, audit/history, idempotency, and outbox.
- Add Card Status Batch and Card Status Batch Item with asynchronous processing, one target status per batch, atomic card updates, worker ownership, retries, concurrency, and recovery.
- Describe tenant-aware indexes, pagination, connection management, monitoring, and `entity_id` routing to one initial data shard, with future relocation and partitioning strategies.
- Distinguish PostgreSQL transaction guarantees from external-service effects and identify unresolved implementation decisions.
- Index this approved plan and record the documentation delivery in `DEVELOPMENT_LOG.md`.

## Validation

- Review terminology, cardinalities, keys, tenant relationships, and transaction boundaries for consistency.
- Check the Markdown structure, relative links, required sections, and batch/scalability coverage.
- Confirm the changes introduce only documentation. Runtime tests and implementation review gates remain prerequisites for a future executable feature; this document does not claim those gates passed.

## Assumptions

- Existing session decisions are the source of the design; remaining implementation choices are explicitly identified.
- Accounts remain bank-owned, cards are virtual first, and all banks initially route to one data shard.
- The delivered design is a draft for subsequent implementation design, not a production-readiness approval.
