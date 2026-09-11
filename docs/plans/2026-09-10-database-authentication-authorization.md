# Add authentication and authorization to the database design

## Summary

Extend `docs/database-design.md` with local authentication, JWT access tokens, rotating refresh tokens, and four fixed roles. This is a follow-on to [Save the Database Design Document](2026-09-10-database-design-document.md); preserve that approved plan unchanged. The deliverable is documentation only.

## Approved design changes

- Define `issuer_operator` with full supported business/user administration across banks; `bank_operator` with read access and all supported card-status changes, including permanent closure, for one bank; `bank_readonly` for one bank; and `issuer_readonly` for all banks.
- Assign exactly one constrained role per user. Bank operators cannot issue/replace cards, delete records, administer users, or modify banks, products, clients, and account references. No role bypasses lifecycle rules, atomicity, or audit preservation. All roles retain their own authentication actions.
- Add central User, Auth Session, Refresh Token, and Authentication Audit Event tables. Users are staff identities, separate from Clients; use no role-membership or configurable permission tables.
- Store UUID keys, a unique normalized username, Argon2id password hash, role, bank assignment where required, enabled state, `auth_version`, and actor/timestamp metadata. Bank roles require a directory-linked `entity_id`; issuer roles require no assignment.
- Sessions contain user/version references, absolute expiry, refresh activity, and revocation information. Refresh tokens contain unique hashes, session and parent links, issue/expiry/consumption times; enforce one successor per parent and same-session ancestry.
- Index user bank/role/status, user sessions, token hashes and session issuance, expiry cleanup, and authentication events by time/subject. Preserve consumed tokens through family validity and disable audited users rather than deleting them.
- Use 15-minute JWTs and seven-day non-sliding sessions/refresh families. Permit multiple sessions. Validate signature/algorithm, issuer, audience, identity/role/bank claims, and expiry. Signing keys stay outside business tables.
- Rotate opaque refresh tokens atomically with current user/session validation; serialize against revocation/authorization changes. Reuse revokes the family. Store hashes only, with no plaintext passwords or tokens.
- Keep ordinary access-token authorization stateless: issued JWT privileges remain valid until expiry. Logout prevents session refresh; disablement, role/bank changes, and password change/reset invalidate existing refresh sessions through `auth_version`. No access-token table or denylist.
- Permit issuer roles to select an authorized bank explicitly; bank roles use their verified assignment. Each business transaction remains tenant-scoped. Keep shard actor IDs as logical references to central users, with immutable role/context snapshots.
- Recheck the submitter's current enabled state, role, and bank before each batch execution attempt. Fail unauthorized work without card changes; defer when authorization is unavailable. Logout/token expiry alone does not cancel accepted work. State the execution checkpoint and absence of a distributed transaction across central authentication and shard writes.
- Let issuer operators provision users; bootstrap the first issuer operator operationally. No public registration. Audit authentication and user administration centrally without token/hash disclosure.
- Identify signing-key operations, client token transport/storage, recovery, MFA, and machine-to-machine authentication as future implementation decisions.

## Validation

- Review domain tables, diagrams, role matrix, indexes, actor references, routing, and batch checkpoints for consistency.
- Add future scenarios for all role/action/bank combinations, invalid bank claims, disabled users, stale JWT privileges, token rotation/replay/concurrency/expiry/revocation, queued-work authorization, and schema constraints.
- Check Markdown headings, tables, Mermaid block structure, and local links; confirm preservation of the original approved plan and documentation-only scope.
- Update the plan index and append delivery/review evidence to `DEVELOPMENT_LOG.md`. Do not claim runtime tests or independent implementation gates passed.

## Assumptions and defaults

- Local, pre-provisioned accounts use a platform-wide normalized staff username namespace and Argon2id hashes.
- Access JWTs expire after 15 minutes; session/refresh-family validity ends seven days after login without sliding extension.
- All-bank access authorizes a selected bank per business operation; it does not unify Client records or grant an unrestricted PostgreSQL role.
- Credential-service effects remain outside the shard transaction guarantee.
