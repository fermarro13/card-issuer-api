# Issuer authorization vault and credential verifier

## Goal

Add a PCI-scoped credential-verification boundary that lets an authenticated bank request a card authorization check. The check approves only when credentials validate and the card is active and unexpired. The issuer business API, shard database, logs, audits, idempotency records, fixtures, and replay responses must not retain raw payment credentials.

## Implementation changes

- Add a `CredentialVault` interface with provision, verify, and revoke operations. Provision returns a masked card display plus a transient credential disclosure; verification returns only an internal card identity or a safe credential failure.
- Add a standalone authorization service and a mutually authenticated TLS bank-client boundary. Map an enabled client certificate fingerprint to exactly one bank; never trust a bank identifier supplied in the request body.
- Add `POST /v1/authorization-verifications`. Its request contains an opaque bank transaction reference and transient credential fields. Its response contains only the transaction reference, a server decision ID, and `approved` or `declined`.
- Add a test-only in-memory vault adapter. It holds no raw values: it maps a keyed lookup token to the internal card ID and derives verification data from protected test-only key material. Reject this adapter outside development and test environments.
- Require a production implementation to be an external PCI-compliant vault/HSM adapter. Do not add vendor or provider fields to cards.
- Add immutable `masked_pan` to `bank.cards`. Store no full account number or verification code in project databases. Existing cards remain null until a subsequent issuance or replacement supplies a masked display.
- Add tenant-scoped `bank.authorization_verifications` with a unique bank transaction reference, decision ID, nullable resolved card ID, safe internal decision code, and timestamps. Store neither request bodies nor payment-data fingerprints. Replays return the original safe decision without revalidation.
- Change issuance and replacement to provision through the vault using the card UUID as correlation and idempotency identity. Persist only the masked display. Return a transient disclosure only in the initial successful response; sanitized idempotency replay responses never disclose credentials again.
- If a card transaction fails after vault provisioning, request vault revocation. Document reconciliation by card UUID for uncertain outcomes; do not write credential values to the existing outbox.
- Approve only after vault verification succeeds and the existing card is `active` and unexpired. Decline all other cases generically to the bank while recording a safe internal reason. Do not add balance checks, holds, ledger entries, spending controls, fraud scoring, or merchant/amount authorization.

## Test scenarios

- Raw payment credentials never appear in PostgreSQL, SQL parameters, logs, traces, audit records, errors, fixtures, idempotency records, response replays, or outbox payloads.
- Valid credentials approve only active, unexpired cards. Invalid credentials and suspended, closed, or expired cards decline.
- mTLS identity maps to one enabled bank and cannot authorize a different bank's card.
- Only the masked display persists. A transient disclosure appears only on the initial issuance response and cannot be recovered after a lost response.
- Duplicate transaction references return the first sanitized decision without retaining or recomputing sensitive inputs.
- Test vault configuration is rejected outside development/test, while production configuration requires a vault/HSM implementation.

## Assumptions

- The issuer operates the required PCI-scoped authorization environment, but this design does not retain verification codes or their hashes.
- The masked display is the only persisted account-number-derived representation.
- A real vault/HSM vendor, certificate authority, network deployment, and PCI assessment are external prerequisites for production use.
- The first release is credential-and-lifecycle verification only; financial authorization is deferred.
