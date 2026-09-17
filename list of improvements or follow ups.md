# Improvement and Follow-up Register

## Audit context

This register records the codebase review performed on 2026-09-16. It used the
Go concurrency, security, testing, benchmark, and performance guidance, with
focused static inspection plus local verification.

The repository is a portfolio proof of concept. Recommendations therefore
favor contained changes that demonstrate engineering judgement over broad
production infrastructure.

### Verification completed

- `go test -count=1 ./...` passed. The PostgreSQL runtime-environment test
  remains skipped unless `CI_TEST_DATABASE=1` is set for a disposable database.
- `go test -race -count=1 ./...` passed.
- `go vet ./...` passed.
- Root-package coverage was approximately 25.8%.
- The scoped `gosec` scan was reviewed manually. Most results were static false
  positives involving fixed loopback health checks, trusted startup paths,
  test subprocesses, and intentional shutdown contexts; the password
  idempotency finding below is substantive.
- `govulncheck` and `golangci-lint` were unavailable, so dependency
  vulnerability reachability and a full lint baseline were not verified.

## Approved next improvement: keyed idempotency fingerprints

### Why it matters

Password-bearing staff requests are marshalled into normalized JSON and then
fingerprinted with plain SHA-256 before being persisted in idempotency records.
A database snapshot can therefore contain a fast offline password-guessing
oracle, bypassing the deliberate Argon2id password-hash cost.

Evidence path:

1. `internal/server/staff_handler.go` accepts and marshals user-create and
   password-reset bodies.
2. `internal/staff/service.go` passes those bodies to central idempotency.
3. `internal/idempotency/fingerprint.go` uses unkeyed SHA-256.
4. `internal/repository/control/store.go` persists the result in
   `request_fingerprint`.
5. `database/migrations/control/002_public_resource_api.sql` grants the auth
   runtime role access to the idempotency table.

### Approved implementation decisions

- Replace unkeyed SHA-256 with an immutable, concurrency-safe
  `idempotency.Fingerprinter` that produces HMAC-SHA-256 fingerprints.
- Add `API_IDEMPOTENCY_HMAC_KEY_B64` and `Config.IdempotencyKey`; require at
  least 32 decoded bytes and reject reuse of the cursor HMAC key.
- Use the clearly public POC development value
  `AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE` in Compose and `.env.example`.
  Reject that value when `APP_ENV=production`.
- Construct one fingerprinter in `cmd/api` and inject it into bank, catalog,
  card, batch, and staff services. All central and shard idempotency claims use
  it.
- Keep the existing 32-byte database columns and all HTTP contracts unchanged.
  No migration is needed.
- Do not add legacy SHA-256 dual-read support. Idempotency keys created before
  the change may conflict until their existing seven-day replay lifetime ends.
  This is an accepted POC trade-off.
- Key rotation and multi-key verification are deliberately out of scope.

### Required tests

- HMAC stability, key/payload sensitivity, key defensive copying, and invalid
  key rejection.
- Proof that HMAC output differs from plain SHA-256 of the same body.
- Staff user-create and password-reset tests that capture the submitted
  idempotency claim and prove the password body is not stored as a plain
  SHA-256 verifier.
- Configuration validation for missing, malformed, short, reused, and
  production-public keys without exposing secret values.
- Re-run normal tests, race tests, vet, and the existing scoped `gosec` check.

## High-value follow-ups

### 1. Make executor shutdown genuinely bounded

Priority: high. This is the strongest concurrency follow-up.

`internal/executor/service.go` detaches workers with
`context.WithoutCancel`. On shutdown it returns after `DrainTimeout`, but a
WaitGroup waiter may remain blocked. The worker can retain a database
transaction/connection, and the deferred `pgxpool.Close` in
`cmd/executor/main.go` blocks until checked-out connections return. As a
result, process shutdown can exceed the advertised drain timeout.

Recommended shape:

- Give each claimed job a context owned by `Run` and a deadline no greater than
  its lease.
- Allow current jobs to drain for `DrainTimeout`, then cancel their contexts.
- Add a fake store that blocks on `ctx.Done()` and assert shutdown returns,
  workers release reservations, and `InFlight` reaches zero.
- Add `go test -race -count=1 ./...` to CI. The current workflow runs only
  ordinary tests.

Avoid a new worker-pool abstraction: existing global and per-bank reservation
logic already bounds goroutine creation correctly.

### 2. Add direct card-saga contract tests

Priority: high. This is the strongest testing follow-up.

`internal/card/service.go` coordinates issuance/replacement, database
transactions, idempotency, and credential-vault compensation, but the package
has no direct unit test file or coverage. Add failure-injection fakes to prove:

- Vault credentials are revoked after repository, idempotency, or commit
  failures.
- Successful commits do not revoke credentials.
- Replays do not provision credentials again.
- Persisted replay data remains sanitized while the one-time response returns
  credentials only where intended.

Follow with direct tests for `internal/auth/service.go`, especially refresh
token replay, commit/rollback behavior, and password-change invalidation.

### 3. Promote database integration tests into an explicit CI gate

Priority: high.

The root CI workflow runs `go test ./...`; its runtime database test skips
unless `CI_TEST_DATABASE=1` is supplied. The separate `database/tests` module
is not visited by the root command. Introduce an `integration` build tag and
separate CI jobs for unit/race tests and disposable PostgreSQL integration
tests.

The current large runtime test also has order-coupled subtests. Split it into
fixture-backed scenarios that are independently runnable rather than relying on
state left by earlier subtests.

### 4. Harden login admission control

Priority: medium for the local POC; high if publicly exposed.

The login path has no rate limiting. Missing or disabled users return before
Argon2 verification, while valid usernames execute the expensive hash; generic
HTTP errors do not remove this timing distinction. Login also permits passwords
larger than the normal 1024-byte policy before verification.

For a future public deployment, add a bounded, concurrency-safe login limiter,
a dummy Argon2 verification for unknown/disabled users, strict credential input
length checks, an injectable clock, and race-tested concurrent cases. The
current loopback-only Compose deployment and explicit API-gateway exclusion
make this a follow-up rather than a POC blocker.

### 5. Fail closed for production-like configuration

Priority: medium.

`APP_ENV=production` rejects the development vault but currently permits
`PGSSLMODE=disable` and public POC JWT/cursor values. Documentation warns about
this, but configuration does not enforce it. Add production-only checks for
database TLS and known development keys when the project moves beyond a local
POC.

### 6. Avoid holding an authorization transaction across a remote vault call

Priority: medium.

The authorization service opens a transaction and obtains an advisory lock
before calling the credential vault. A slow vault call can occupy a scarce
connection in the five-connection pool. A POC-proportionate mitigation is a
service-wide or per-bank admission semaphore; a durable two-phase idempotency
redesign is intentionally larger than this repository currently needs.

### 7. Improve deterministic concurrency testing

Priority: medium.

There is no goroutine leak detection. Some tests depend on real sleeps and
negative timing assertions, including executor draining, readiness, and vault
deadlines. Use `testing/synctest` where appropriate, channel handshakes for
ordering, and `goleak` in goroutine-owning packages. Cover the executor
drain-timeout path specifically.

### 8. Add focused fuzz/property tests

Priority: medium.

No fuzz targets exist. Good low-cost candidates are:

- HMAC cursor encode/decode and malformed cursor rejection.
- JWT strict JSON parsing.
- Batch normalization: no panic, order-independent canonicalization, and
  duplicate rejection.

## Evidence-driven performance work

No benchmark or fuzz targets currently exist. Do not introduce micro-
optimizations without a measurement baseline.

### Collection pagination

Repository methods load all matching rows and HTTP code slices them in memory.
For example, `internal/repository/shard/reader.go` fetches all cards, while
`internal/server/resource.go` applies cursors and page limits afterward. This
is acceptable for POC-scale data but is not true database keyset pagination.

If larger datasets become a goal, move cursor predicates and `LIMIT + 1` into
repository queries while retaining HMAC cursor binding to resource and filter.

### Batch database round trips

A maximum 200-card batch may make hundreds of sequential calls while creating
a draft and roughly up to one thousand during application, depending on item
outcomes. This is a scaling hypothesis, not proof of poor performance.

Before changing SQL, add PostgreSQL-backed sub-benchmarks for sizes 1, 50, and
200 using `b.Loop()`, allocation reporting, elapsed latency, and cards/second.
Only optimize measured bottlenecks with set-based `UNNEST`/CTE queries or
`pgx.Batch`, while preserving tenant isolation, transactional atomicity, and
lease fencing.

## Strengths to preserve

- SQL is parameterized and tenant-scoped.
- JWT validation pins EdDSA and validates claims.
- Refresh tokens rotate and detect replay.
- Request JSON is strict and body size-limited.
- mTLS uses TLS 1.3 and maps client certificates to banks.
- Containers run non-root with read-only filesystems and dropped capabilities.
- Executor claims are bounded globally and per bank; shared state is protected
  with mutexes/typed atomics.
- Readiness fan-out is buffered correctly and cancellation-aware.
- Vault maps are synchronized and certificate mappings are copied before use.

