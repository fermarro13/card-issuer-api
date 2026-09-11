# Go Version Governance in the Agent Harness

## Summary

Require Go 1.27.1 for Go development and review evidence through the agent harness. This is a harness policy; it does not yet add Go module, container, or CI configuration.

## Implementation changes

- Require Go 1.27.1 for implementation, formatting, static analysis, automated tests, and review evidence.
- Require the backend developer to include `go version` output in the feature handoff and run relevant tests with that toolchain.
- Require QA to reject missing, failed, or mismatched toolchain evidence.
- Require the architect to assess new language or standard-library dependencies against Go 1.27.1.
- Require the orchestrator to verify toolchain evidence and passing relevant tests before closeout.
- Adopt a newer Go release only through a dedicated maintenance feature that passes architecture, test, QA, and security gates.

## Test scenarios

- Confirm each relevant agent role references Go 1.27.1 consistently.
- Confirm the orchestrator checklist requires `go version` evidence and test evidence from Go 1.27.1.
- Confirm the development log records the toolchain decision and controlled upgrade process.

## Assumptions

- “Latest Go version” refers to the current stable Go 1.27.1 release.
- No agent silently upgrades or mixes Go toolchain versions inside a feature.
