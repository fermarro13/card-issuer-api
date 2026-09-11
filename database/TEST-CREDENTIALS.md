# Application test credentials

These deliberately public passwords are for the test seed only. They are application staff accounts, not PostgreSQL connection accounts. Run `Invoke-Database.ps1 -Action SetupTest` to insert them. Authentication endpoints are not implemented yet.

| Username / role | Bank assignment | Password |
| --- | --- | --- |
| `issuer_operator` | None; issuer scope | `Test-Issuer-Operator!2026` |
| `issuer_readonly` | None; issuer scope | `Test-Issuer-Readonly!2026` |
| `bank_operator` | Test Bank | `Test-Bank-Operator!2026` |
| `bank_readonly` | Test Bank | `Test-Bank-Readonly!2026` |

Test Bank ID: `10000000-0000-4000-8000-000000000001`. User IDs end in `0001` through `0004`, respectively, under `20000000-0000-4000-8000-00000000`.

Seed SQL contains only independently salted Argon2id password hashes with version 19, memory 19,456 KiB, iterations 2, and parallelism 1. Existing accounts are never reset by rerunning the seed; passwords above apply only to newly inserted accounts.
