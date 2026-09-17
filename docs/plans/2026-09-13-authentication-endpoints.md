# AUTH-API-001 — Authentication endpoints

**Status:** Complete. Architect, QA, security, automated-test, PostgreSQL integration, and Docker verification gates passed.

## Goal

Implement the public authentication endpoints against the existing control-database authentication schema: login, refresh, logout, current user, and password change.

## Implementation changes

- Add a `ci_app_auth` Compose runtime login limited to `ci_auth_runtime`; configure the API with `AUTH_DATABASE`, `AUTH_DB_USER`, and `AUTH_DB_PASSWORD` while retaining the routing reader account.
- Add required application-owned Ed25519 JWT private-key configuration, issuer, and audience. Issue 15-minute JWTs and validate only `EdDSA` signatures.
- Implement isolated authentication repository, service, JWT/password, and HTTP-handler layers. Use transactions and the User-then-session lock order for refresh and logout.
- Add Argon2id verification and password changes. New passwords must be at least 13 Unicode characters and contain an uppercase letter, a number, and punctuation or symbol.
- Implement one-time rotating opaque refresh tokens stored only as SHA-256 hashes. Send them exclusively in the `__Host-card-issuer-refresh` Secure, HttpOnly, SameSite=Strict cookie.
- Return login/refresh token metadata plus a user summary containing only username, status, and role; return the same summary from `/v1/me`. Add request-ID and problem-response middleware.
- Record central sanitized authentication audits, including committed refresh-replay revocation evidence. Password changes invalidate refresh families through the existing authorization-version rule; access tokens remain valid until expiry.
- Document the approved exception that authentication POST endpoints do not require `Idempotency-Key`, because replaying secret-bearing responses requires undefined encrypted-result storage and conflicts with refresh replay detection.

## Test scenarios

- Unit-test JSON validation, password validation and hashes, JWT issuance/validation, and error response behavior.
- Contract-test authentication responses, cookies, request IDs, bearer challenges, secret exclusion, and stateless `/v1/me` behavior.
- Integration-test login, rotation, replay revocation, logout, disabled/version-mismatched/expired sessions, password invalidation, and audit redaction with `ci_app_auth`.
- Run formatting, vet, and relevant tests with Go 1.27.1 before QA and security review.

## Assumptions

- “Self-signed” means an application-controlled Ed25519 key pair, not an X.509 certificate.
- No database schema migration, CORS, MFA, registration, recovery, signing-key rotation, or immediate access-token revocation is included.
