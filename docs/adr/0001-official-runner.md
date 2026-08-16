# ADR 0001: keep the official GitHub Actions runner

- Status: accepted
- Date: 2026-08-08

## Context

GitHub Actions execution includes step and post-step ordering, expressions,
contexts, command files, secret masking, JavaScript/composite/Docker actions,
job and service containers, cancellation, outputs, annotations and a changing
control-plane protocol. A custom engine would make compatibility a permanent
product responsibility.

## Decision

Use the official `actions/runner` binary as the only production execution
engine. NDDev engineering is limited to provisioning, admission, scheduling,
lifecycle, reconciliation, caching, images and telemetry.

## Consequences

- GitHub compatibility follows the official boundary.
- Runner updates require timely image-canary promotion.
- Execution-engine performance is not a primary optimization target.
- `act` remains useful for local preflight but is not a fleet executor.
- Alternative/Gitea runner engines are implementation references only.
