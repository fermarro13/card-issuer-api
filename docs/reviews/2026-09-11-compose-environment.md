# ENV-COMPOSE-001 review and validation evidence

Scope: [approved Compose environment plan](../plans/2026-09-11-docker-compose-environment.md). Implementation is present; final container acceptance remains pending. Existing database migrations are unchanged.

## Reviews

- **Architect: APPROVED.** Reviewed the approved plan, existing database workflow, initialization order, separate runtime identities, shared readiness deadline, and graceful shutdown. Approval is for design.
- **Security: APPROVED for inspected code and available integration evidence.** Reviewed Compose/Dockerfile, credential quoting and separation, runtime-role guards, non-root configuration, HTTP responses, and actual PostgreSQL permission checks. No actionable source findings. This does not certify unexecuted containers.
- **QA: BLOCKED pending live Docker acceptance.** Source review and available host tests found no actionable defect. Actual image builds, clean-volume Compose startup, persistent-volume restart, and initialization-failure dependency behavior still require the container smoke test.
- **Automated-test gate: partial evidence only.** All available Go/database/static checks passed; Docker-dependent acceptance remains unavailable. No exception or full completion is claimed.

## Passing checks

- `go version`: `go version go1.27.1 windows/amd64`.
- Root `go test -count=1 -v ./...` with `CI_TEST_DATABASE=1` on a dedicated PostgreSQL 17.11 SCRAM-authenticated instance: all tests passed, no integration skip. Runtime database integration: 7.865 seconds; HTTP server tests: 2.811 seconds.
- Real database evidence covers restricted login accounts, missing-context RLS, forbidden ownership/authentication access, preserved staff credentials on repeated setup, wrong technical-password rejection, incompatible role rejection, and readiness outage/recovery while liveness remains healthy.
- Original `database/tests` suite: PASS, 15.076 seconds.
- Root `go vet ./...` and `go mod verify`: passed.
- `docker compose config --quiet`: passed.
- PowerShell parser checks for initialization and Compose smoke scripts: passed.
- Local documentation links checked.

## Preserved migration SHA-256

| Migration | SHA-256 |
| --- | --- |
| Control initial schema | `C5A19BE9DF1C2328C5152F63744A5B38E45E50E255B3AAA3EDCCEDC946FB1C50` |
| Shard initial schema | `18BAD6C71E2C470ED80CA91161383CAB2C63356C4B52F58AB5F97C43187B2043` |

## Container execution blocker

Docker Desktop was started, but its Linux/WSL2 engine failed with `Wsl/Service/RegisterDistro/CreateVm/HCS/HCS_E_HYPERV_NOT_INSTALLED`. The backend log reports that Windows Virtual Machine Platform and firmware virtualization must be available. The daemon returned an error rather than a usable engine.

No image build or live Compose result is claimed. Once the host supports the engine, run `./scripts/Test-Compose.ps1` from the repository root. That script builds both targets, creates an isolated project/volume, verifies startup and persistence, exercises PostgreSQL outage/recovery and failed-initialization gating, then removes its disposable project. Record that evidence and obtain final QA approval before marking this feature complete.
