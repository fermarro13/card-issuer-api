# Local authorization verification environment

## Status

Delivered. `go test -count=1 ./...`, `go vet ./...`, `go test -race ./...`, and `git diff --check` passed with Go 1.27.1. The rebuilt Compose functional profile passed, including the mTLS authorization-vault boundary. Independent architect, QA, and security review remain pending.

## Goal

Make the existing card credential-and-lifecycle verification feature runnable and testable on a developer workstation. A fresh Docker Compose stack must issue a card through the public API, activate it, and obtain an mTLS authorization decision from the separate authorization service.

The local environment must preserve the established boundaries: no full PAN or CVV in PostgreSQL, logs, audit rows, idempotency rows, fixtures, response replays, or source-controlled files.

## Current gap

- `cmd/authorization` and its Docker image target exist, but Compose does not start that service or publish its localhost-only mTLS port.
- The API and authorization binaries each construct a separate `vault.InMemory` instance. Its keyed PAN lookup map is process-local, so the authorization binary cannot resolve a PAN issued by the API.
- The functional authorization test is skipped unless an externally provisioned authorization URL, certificates, and vault are supplied.

## Design decisions

- Keep the authorization verifier as a separately deployed service. Do not fold it into the public API merely to share memory.
- Add a development-only vault service that owns the existing `InMemory` adapter. The API and authorization services use clients for that shared service. The development vault remains reachable only on the Compose `internal` network and has no host port.
- Keep the development vault non-durable. Restarting or recreating it invalidates prior development credentials; a fresh stack and newly issued card are required. This is explicit local-test behavior, not a production vault design.
- Generate a local CA, an authorization-server certificate, and a mapped bank-client certificate at startup under `.local/authorization-pki`. This ignored directory lets Postman use the client certificate without committing keys. The functional test receives the same files read-only.
- Bind the authorization service only to `127.0.0.1:${AUTHORIZATION_PORT:-8443}`. Attach it to both `internal` and `frontend`; a container connected only to the internal network cannot publish a host port with the current Docker Engine.
- Preserve `CREDENTIAL_VAULT_MODE=external` as the production path. Add an explicit development mode that is rejected outside `development` and `test`; it must never claim to be PCI-compliant or serve production traffic.

## Implementation changes

1. Define the shared development-vault transport.

   - Keep `internal/vault.CredentialVault` as the consumer-facing port.
   - Add a development-only HTTP handler and client in a child package such as `internal/vault/development`. The handler wraps the existing `vault.InMemory` implementation; the client implements `CredentialVault`.
   - Use three internal endpoints: provision a card ID, verify PAN/CVV, and revoke a card ID. Validate methods, paths, exact JSON bodies, and bounded request sizes. Return `Cache-Control: no-store` for responses that contain a transient disclosure.
   - Do not log request bodies, credentials, lookup tokens, or derived values. Return generic errors to callers.
   - Add `cmd/devvault`, which fails unless `APP_ENV` is `development` or `test`, requires the test vault key, binds only to its configured internal address, and exposes a minimal local health check.

2. Select the vault explicitly in both application binaries.

   - Extend `internal/config` with a development-vault URL and an allowed development-only vault mode.
   - In `cmd/api` and `cmd/authorization`, construct the development HTTP client when that mode is selected. Retain the current external-vault failure gate for production until a real vendor/HSM adapter is supplied.
   - Remove direct per-process `NewInMemory` construction from those two binaries. Keep it for unit tests and as the implementation inside `cmd/devvault`.
   - Give the HTTP client fixed dial, TLS-handshake, response-header, and total request timeouts; propagate request contexts; close response bodies; and reject malformed responses.

3. Complete the authorization service's local configuration.

   - Add a health-check command path to `cmd/authorization`, analogous to the API and executor commands, so Compose can wait for its TLS listener before running functional tests.
   - Let the certificate-fingerprint-to-bank mapping come from either the existing JSON environment variable or an explicitly configured read-only mapping file. Reject missing, malformed, or conflicting sources.
   - Keep TLS 1.3 and `RequireAndVerifyClientCert`; preserve the rule that the bank identity is derived only from the verified client certificate fingerprint.

4. Add deterministic local PKI provisioning without source-controlled keys.

   - Add a small `cmd/localpki` program and Docker target. On an empty `.local/authorization-pki` directory it creates a local server CA, a client CA, an authorization-server certificate, a mapped bank-client certificate, and the fingerprint mapping file.
   - Write PEM files with restrictive permissions where the host permits them, create files atomically, and make reruns idempotent. Refuse a partial or invalid directory rather than silently mixing certificates from different CAs.
   - Mount the directory read-write only in `pki-init`; mount it read-only in `authorization` and `functional-tests`. Verify `.local` remains ignored by Git and excluded from image build contexts.
   - Document that this PKI is development-only and must never be used outside the local machine.

5. Wire the complete local topology in Compose.

   - Add `pki-init`, `devvault`, and `authorization` services. `pki-init` completes before services requiring certificates start; `devvault` is healthy before the API and authorization service start.
   - Configure the API and authorization service with the same development-vault URL on the internal network. Do not publish the development-vault port.
   - Give `authorization` the existing authorization Docker build target, database connection settings, generated certificate paths, client-CA path, and fingerprint-mapping file. Publish only `127.0.0.1:8443` by default.
   - Keep PostgreSQL and executor internal-only. Keep the API's already-added frontend attachment. Add the same frontend attachment only to authorization, solely for the localhost mTLS binding.
   - Make `functional-tests` depend on a healthy authorization service and mount the generated local certificates read-only. Supply its existing `FUNCTIONAL_AUTHORIZATION_*` variables from fixed in-container file paths.

6. Turn the skipped authorization scenario into the local end-to-end contract.

   - Retain the existing functional test's approval, invalid credential, replay, suspended, closed, expired, cross-bank, unmapped-certificate, and unexpected-body-field cases.
   - Remove the environment-driven skip for the Compose path; fail the functional profile if PKI, vault, or authorization service setup is incomplete.
   - Add focused unit tests for the development vault client/handler: provision then verify, invalid credential, revoke, malformed input, unavailable vault, no-store headers, timeout/cancellation, and no raw credentials in safe errors.
   - Add configuration tests proving development-vault mode is rejected outside development/test and that external mode cannot fall back to the development service.
   - Extend the PowerShell and shell Compose smoke tests to wait for the authorization listener and execute the authorization functional scenario.

7. Document local use and manual verification.

   - Update `README.md`, `.env.example`, and the Postman collection with the authorization service, its local port, generated certificate locations, and the clear distinction between `base_url` and `authorization_base_url`.
   - Document the local workflow: start the stack, import the bank client certificate and key into Postman for `localhost:8443`, issue and activate a card, copy the one-time credentials only into transient collection variables, submit a new transaction reference, and clear the variables afterward.
   - Document the automated equivalent: `docker compose --profile functional run --rm functional-tests`.

## Verification

Run these checks after implementation:

1. `go test -count=1 ./...`, `go vet ./...`, and `go test -race ./...` pass.
2. A fresh `docker compose up --build -d` reports healthy API, executor, development vault, and authorization services; `db-init` and `pki-init` exit successfully.
3. `http://localhost:8080/health/live` and `/health/ready` return 200. The authorization listener accepts only TLS 1.3 with a valid mapped client certificate on `https://localhost:8443`.
4. `docker compose --profile functional run --rm functional-tests` exercises the complete issue → activate → verify path and all authorization safety cases without skips.
5. A request with no client certificate, an unmapped certificate, invalid JSON, a body-supplied bank ID, invalid credentials, or a card belonging to another bank is denied safely.
6. A repeated transaction reference returns the first safe decision without re-evaluating credentials.
7. Database inspection confirms `bank.cards` contains only a masked PAN and `bank.authorization_verifications` contains only the documented safe decision fields. Logs, audit records, idempotency records, and replay responses contain neither PAN nor CVV.
8. `git status --short` contains no generated certificate or credential files.

## Assumptions and non-goals

- This plan is a local-development completion of the existing feature, not a production PCI implementation.
- The development vault's process-local lookup state intentionally disappears when it restarts. Production requires an independently provisioned external PCI-compliant vault/HSM adapter with its own durability and reconciliation guarantees.
- Local client certificates authenticate only the seeded Test Bank. Managing real bank certificate issuance, revocation, rotation, and a production CA remains outside this scope.
- The authorization decision remains credential-and-lifecycle verification only; no financial holds, balances, ledgers, fraud checks, merchant data, or spending controls are added.
