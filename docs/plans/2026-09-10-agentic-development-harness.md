# Agentic Development Harness

## Summary

Create a Markdown-based governance harness in `.agents/` and a concise, redacted `DEVELOPMENT_LOG.md` at the repository root. The harness governs future Card Issuer API work; it does not implement the Go service.

## Implementation changes

- Define orchestrator, architect, backend developer, backend QA reviewer, and security reviewer responsibilities, authority boundaries, required evidence, and blocking conditions.
- Require feature intake, architect approval, implementation with unit tests, a separate passing automated-test gate, QA approval, security approval, and orchestrated closeout.
- Make user approval mandatory for every exception and document its scope, risk, compensating controls, and expiry/review trigger.
- Establish tenant isolation, PCI-relevant PAN handling, parameterized SQL, explicit lifecycle transitions, and atomic batch updates as non-negotiable constraints.
- Maintain an append-only, concise log that excludes PAN, secrets, credentials, and unnecessary cardholder data.

## Test scenarios

- Confirm each agent definition contains its required purpose, authority boundaries, inputs, outputs, and gate responsibilities.
- Simulate a feature and verify it cannot bypass architect, automated-test, QA, or security approval.
- Confirm the log template captures milestones without sensitive data.

## Assumptions

- `.agents/` is version-controlled project policy.
- Only the user may authorize an exception to a failed gate.
