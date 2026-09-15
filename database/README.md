# PostgreSQL database scripts

Creates [the project design](../docs/database-design.md): a central control database (five tables) and one bank shard (fifteen tables). PostgreSQL **17** is the tested baseline. No PostgreSQL extensions are required.

The default development workflow is `docker compose up --build` from the repository root; see [the environment README](../README.md). Compose runs the portable shell initializer inside its PostgreSQL Linux container, provisions separate technical connection accounts, and starts the Go base server. It works with Docker Desktop on Windows/macOS and Docker Engine on Linux. The standalone workflow below remains available for an existing PostgreSQL instance.

## Run against your existing PostgreSQL instance

Install the PostgreSQL `psql` client and connect as an administrator with permission to create databases/roles and assume `ci_owner`. Bootstrap requires those privileges; migration-only runs require `CONNECT` and permission to assume `ci_owner` on each target database. Configure connection credentials through your normal libpq environment/service/password-file setup, outside source control. Both runners use `--no-password` so unattended calls fail instead of waiting for an interactive prompt.

On Linux/macOS, use `/bin/sh`; the runner requires `sha256sum` (commonly Linux) or `shasum` (included with macOS). On Windows, use PowerShell 7. The two runners have matching actions and validation:

Linux/macOS:

```sh
export PGHOST=localhost
export PGPORT=5432
export PGUSER=postgres
# Supply authentication through your existing libpq password file or environment.
sh ./database/Invoke-Database.sh --action SetupTest
```

Windows PowerShell:

```powershell
$env:PGHOST = 'localhost'
$env:PGPORT = '5432'
$env:PGUSER = 'postgres'
# Supply authentication through your existing libpq password file or environment.
./database/Invoke-Database.ps1 -Action SetupTest
```

`SetupTest` creates roles/databases if absent, applies migrations, provisions Test Bank on the shard, then inserts its central routing entry and four staff accounts. Their passwords are in [TEST-CREDENTIALS.md](TEST-CREDENTIALS.md). These are application accounts for the authentication API. All PostgreSQL groups created by bootstrap are `NOLOGIN`; Compose provisions its separate restricted login accounts through `Initialize-Compose.sh` and `compose-runtime.sql`.

To specify a client executable or different database names:

```sh
sh ./database/Invoke-Database.sh \
  --action SetupTest \
  --psql /usr/local/bin/psql \
  --control-database issuer_test_control \
  --shard-database issuer_test_shard \
  --shard-id shard_01
```

```powershell
./database/Invoke-Database.ps1 -Action SetupTest `
  -Psql 'C:/Program Files/PostgreSQL/17/bin/psql.exe' `
  -ControlDatabase issuer_test_control `
  -ShardDatabase issuer_test_shard `
  -ShardId shard_01
```

Database identifiers must be distinct ASCII identifiers of at most 63 characters. `ShardId` is the logical directory key, not a hostname or credential. Its default is `shard_01`. Database names default to `card_issuer_control` and `card_issuer_shard_01`; the administrative connection defaults to `postgres`.

For an empty schema without testing accounts, execute these two commands in order. Use the matching syntax for your platform:

```sh
sh ./database/Invoke-Database.sh --action Bootstrap
sh ./database/Invoke-Database.sh --action Migrate
```

```powershell
./database/Invoke-Database.ps1 -Action Bootstrap
./database/Invoke-Database.ps1 -Action Migrate
```

There is no automatic reset, destructive down migration, connection-account provisioning, or production deployment. Bootstrap rejects existing databases owned by a different role and runtime groups with unsafe attributes or inherited memberships. Provision dedicated connection accounts separately, granting each only its required component group. Never grant runtime logins membership in `ci_owner`.

## Migration and recovery behavior

Migration files are ordered numerically under `migrations/control` and `migrations/shard`. Each database has a restricted `ci_meta` schema recording its kind and applied filename/version/SHA-256 checksum. The runner normalizes CRLF to LF and hashes the exact text it executes. It checks the complete applied history before executing new files, rejects missing/changed/out-of-order migrations, and skips unchanged migrations. `-MigrationRoot` supports an explicitly chosen trusted migration directory, primarily for verification.

One PostgreSQL session holds the advisory lock while checking and applying all migrations for a database. Each migration and ledger insert commit in one transaction. Errors stop execution; the failed transaction rolls back and the session releases its lock. A control migration can commit before a shard migration fails; rerun after correcting the unapplied migration. There is no transaction spanning both databases.

Migration files must contain transactional SQL only: no transaction-control statements, database creation, concurrent indexes, or psql connection changes. Do not edit an applied migration; add the next numbered migration. Run SQL through the runner to retain ledger and locking guarantees. Bootstrap SQL is administrative and intentionally outside migration transactions. During the current pre-release development phase, recreate any local database that applied the superseded split shard migration before running this consolidated initial schema.

The test seeds have their own per-database transaction locks. A committed bank without a central route is safe to retry. `SetupTest` executes the shard seed before the central seed; do not run the central seed alone. A central failure rolls back its users/audits/placement together. Reruns preserve existing passwords, roles, assignments, status and creation evidence, and do not duplicate seed audits. Conflicting fixed UUIDs/usernames/bank references or existing placement on another shard fail. Existing paused placement stays paused. No customer, card, session, or refresh-token fixtures are part of setup.

## Permissions and tenant context

| PostgreSQL group | Access |
| --- | --- |
| `ci_owner` | Owns databases/schemas/tables; used only for migrations and controlled provisioning. |
| `ci_auth_runtime` | Control users/sessions/tokens: SELECT/INSERT/UPDATE; authentication audit: SELECT/INSERT; directory: SELECT. |
| `ci_routing_reader` | Control directory SELECT only. |
| `ci_business_runtime` | Shard tables: SELECT/INSERT/UPDATE, except audit/history are SELECT/INSERT only. No DELETE/TRUNCATE. |
| `ci_executor_runtime` | Control directory reads plus the narrow shard claim, fenced-result, expiry-run/item, card-operation/history, and audit grants required by `cmd/executor`. No auth/session/idempotency/reference-data or DDL access. |

Runtime groups cannot read migration metadata, create schema objects, assume ownership, or bypass RLS. Public database/schema access and helper-function execution are restricted. Only the tenant-context function is callable by business runtime; internal guards run as triggers.

The future application must authenticate and authorize the user **before** selecting a bank. PostgreSQL custom settings do not authenticate tenants: a trusted component with database access can choose any tenant context. The four staff roles are API authorization roles, not four database connection roles. Issuer scope still selects one bank per transaction.

For each business transaction, use a bound parameter with `SELECT set_config('app.entity_id', $1, true)` after beginning the transaction. The final `true` makes it transaction-local. Never use a persistent session setting. Explicit tenant predicates remain required in repository queries. RLS applies to all fourteen bank tables, including Entity, with `FORCE ROW LEVEL SECURITY`; no context yields zero visible rows/rejected inserts, malformed UUID context raises an error. Commit/rollback clears context when connections are reused.

## Schema and application responsibilities

[SCHEMA.md](SCHEMA.md) maps the concrete columns and constraints to the design. The SQL migrations are the authoritative executable definitions. The schema enforces tenant/card/client references, role assignments, normalized username uniqueness, stored status vocabularies, immutable identities and request attribution, password-related authorization-version increments, and refresh ancestry/lifetime bounds.

The API must still implement role/action authorization, current-user/session checks, token hashing and rotation/replay revocation, lock order (User then session), and co-committed success audits. Refresh token hashes are 32-byte SHA-256 digests of cryptographically random opaque values; password hashes are PHC-formatted Argon2id strings. SQL stores hashes but does not perform authentication. The hash prefix check is structural, not cryptographic verification of arbitrary inserted hashes.

Card transition rules, already-at-target behavior, vault issuance/replacement orchestration, and automatic version advancement remain application responsibilities. A status domain does not validate transitions. Issuance/replacement provisions through an external PCI vault/HSM boundary and stores only its masked display, local operation/history/audit evidence, and sanitized idempotency result. The database stores neither raw payment credentials nor their fingerprints or verification-code hashes; no credential material enters the outbox. Accepted-batch submission must atomically create all membership, operations, and idempotency records; the application validates nonempty/exact counts and prevents later item insertion. Header/request identities and existing item references are immutable, but the schema alone is not a batch admission or execution API.

Workers must enforce claim/fencing/retry rules, all-items success or failure for public batches, and co-commit card/history/audit results. The daily expiry worker processes cards independently, retries each failed item at most three times after its initial attempt, and never retries a card already expired; an issuer-operator-only API command may requeue an exhausted item without resetting its automatic-retry count. The available outbox is reserved for a future integration. JSON objects are schema-checked, but callers must sanitize/bound payloads and exclude credentials/customer dumps. Initial Client data is only an opaque external reference and optional display name. Retention, cleanup, deletion, legal holds, customer-profile expansion and product version history are deferred.

## Verify

The harness uses Go **1.27.1** and `psql` against a disposable PostgreSQL **17** instance. It selects the PowerShell runner on Windows and the POSIX-shell runner on Linux/macOS. It creates random `ci_test_*` databases and removes only those databases on completion. Cluster-wide `ci_*` group roles may be created and remain for reuse. Do not point this test harness at production.

Linux/macOS:

```sh
export GOTOOLCHAIN=go1.27.1
export CI_TEST_DATABASE=1
export PSQL=/usr/bin/psql
# PGHOST, PGPORT, PGUSER and libpq authentication refer to the disposable server.
cd database/tests
go version
go test -count=1 -v ./...
```

Windows PowerShell:

```powershell
$env:GOTOOLCHAIN = 'go1.27.1'
$env:CI_TEST_DATABASE = '1'
$env:PSQL = 'C:/Program Files/PostgreSQL/17/bin/psql.exe'
# PGHOST, PGPORT, PGUSER and libpq authentication refer to the disposable server.
Set-Location database/tests
go version
go test -count=1 -v ./...
```

Without `CI_TEST_DATABASE=1`, only password fixture verification runs and database integration is explicitly skipped. This is not sufficient for the integration gate. `verification/` SQL files are invoked by the harness on disposable databases; the fixture script commits test-only rows and should not be run against an application database.

Tests cover installation/reruns, locks, checksum rejection, failed migration rollback, interrupted seeds/collisions, independently salted password verification, RLS and connection reuse, tenant/card/client constraints, immutable attribution, public-batch state guards, per-card expiry retries, authentication guards, and audit permissions. Transaction rollback evidence demonstrates local PostgreSQL atomicity; it does not claim a working batch processor.
