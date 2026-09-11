# Card Issuer API development harness

## Purpose

This harness governs work on the Card Issuer API. It exists to protect security, data isolation, correctness, and auditability before delivery speed. Agent definitions in this directory are project policy and should be followed for every feature.

## Required lifecycle

1. **Intake** — the orchestrator assigns an identifier, scope, success criteria, risks, and a log entry.
2. **Design gate** — the architect approves the design before implementation starts.
3. **Implementation and test gate** — the backend developer implements the approved scope, writes or updates unit tests, and runs the relevant automated tests successfully.
4. **QA gate** — the backend QA reviewer approves functional behavior and test evidence.
5. **Security gate** — the security reviewer approves the relevant security evidence.
6. **Closeout** — the orchestrator confirms every gate passed and records the outcome.

No feature is complete unless all gates have passed. The automated-test gate is separate from QA review: a review cannot substitute for passing test evidence.

## Toolchain policy

All Go implementation, formatting, static analysis, automated tests, and review evidence must use **Go 1.27.1**. The backend developer verifies this with `go version` before implementation and includes the command output in the feature handoff. QA rejects missing, failed, or mismatched toolchain evidence, and the orchestrator cannot close a feature without it.

Go versions are upgraded only through a dedicated maintenance feature. The architect reviews language, standard-library, dependency, and compatibility impact; the developer updates this declared version and reruns the relevant checks; QA and security approve the change; and the orchestrator records the result. No agent silently upgrades or mixes Go toolchain versions within a feature.

## Review response format

Every approval or rejection must state:

- Feature identifier and reviewed revision.
- Scope reviewed and evidence used.
- `APPROVED`, `BLOCKED`, or `APPROVED WITH USER EXCEPTION`.
- Findings, severity, and required remediation when not approved.
- Residual risk, if any.

## Plan persistence

`docs/plans/` is the canonical, version-controlled project memory for every generated implementation plan. Before implementation begins, the orchestrator must save the approved plan as a concise Markdown file in that directory, add it to `docs/plans/INDEX.md`, and link it from the feature's `DEVELOPMENT_LOG.md` entry. Use the filename format `YYYY-MM-DD-short-kebab-case-title.md`.

Plans must state the goal, implementation changes, test scenarios, and explicit assumptions. They must not contain PAN, secrets, credentials, or unnecessary cardholder data. A plan is immutable after approval; material scope or design changes require a new superseding plan that links to the earlier file.

## Non-negotiable engineering constraints

- Establish tenant identity from authenticated server-side context; never accept it as authoritative request input.
- Every tenant-owned read, write, update, and delete must be tenant-scoped in the repository layer.
- Use parameterized queries only. No query may interpolate untrusted values.
- Never log, expose, or use PAN in URLs, errors, ordinary responses, fixtures, or test output. Use only approved tokenization/vault patterns when persistent PAN handling is introduced.
- Make card lifecycle transitions explicit and validate them centrally.
- Make bulk status changes atomic by default and return a consistent result only after the transaction commits.
- Add negative tests for authorization/tenant isolation, invalid state transitions, and batch failure atomicity whenever the feature changes those areas.
- Use Go 1.27.1 for all Go development and its supporting evidence.

## Exceptions

Only the user may authorize an unmet gate. The orchestrator documents the unmet condition, risk, compensating control, affected scope, and an expiry or re-review trigger in `DEVELOPMENT_LOG.md`. An exception is never implicit.

## Interaction log

`DEVELOPMENT_LOG.md` is append-only by convention. Keep entries concise and human-readable. Record intake, material decisions, assignments, gate outcomes, blockers, exceptions, and completion. Never record PAN, CVV, secrets, access tokens, credentials, or unnecessary cardholder personal data.
