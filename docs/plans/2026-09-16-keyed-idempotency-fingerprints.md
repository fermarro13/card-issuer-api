# Keyed Idempotency Fingerprints

## Goal

Replace deterministic SHA-256 request fingerprints with HMAC-SHA-256 so persisted fingerprints cannot be used as fast offline verifiers for password-bearing staff requests.

## Implementation changes

- Replace the package-level fingerprint function with an immutable, concurrency-safe `idempotency.Fingerprinter` that defensively copies a minimum 32-byte key and computes HMAC-SHA-256.
- Load a distinct Base64-encoded idempotency key into `Config.IdempotencyKey`, reject missing, malformed, short, cursor-key-reused, and production POC-key configurations, and document the public development default.
- Construct one fingerprinter in `cmd/api` and inject it into the bank, catalog, card, batch, and staff services; provide a deterministic fingerprinter in integration composition.
- Preserve all HTTP contracts and existing 32-byte database columns. Add no migration and no legacy SHA-256 dual-read behavior.

## Test scenarios

- Verify deterministic same-key output, payload and key sensitivity, defensive copying, short-key rejection, and divergence from plain SHA-256.
- Verify configuration rejects missing, malformed, short, reused, and production POC keys without exposing supplied values, while accepting standard and raw Base64.
- Verify staff user creation and password reset submit keyed HMAC fingerprints rather than SHA-256 of password-bearing JSON.
- Run the complete unit suite, race suite, vet, and the repository's existing focused gosec command.

## Assumptions and compatibility

- Existing idempotency keys created before deployment may conflict until their current seven-day replay window expires.
- Production provisions a unique idempotency secret distinct from the cursor secret.
- Executor shutdown, rate limiting, pagination, benchmarks, and broad CI changes remain out of scope.
