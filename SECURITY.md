# Security policy

This private repository controls infrastructure that can execute repository code
and receive short-lived credentials. Treat suspected vulnerabilities as private
incidents; do not place secrets, exploit details or production identifiers in a
normal issue.

Report findings directly to the repository owner, `@example-user`, through a
private authenticated channel. Include the affected commit, component, expected
boundary, observed behavior and a minimal safe reproduction. Never test against
production workers or retained ExamplePlatform/Captcha services without explicit scope.

## Supported state

Only the current `main` branch and currently deployed image digest are supported.
Pilot branches are non-production. Upstream runner, GARM, Incus provider and
RustFS artifacts are supported only at explicitly recorded digests.

## Response priorities

1. contain runner admission and credential issuance;
2. preserve the lifecycle/audit journal and bounded diagnostics;
3. revoke affected GitHub App, cache or deployment credentials;
4. destroy or quarantine workers and block egress;
5. prove ExamplePlatform/Captcha isolation;
6. patch, canary, rotate and document the incident.

The non-negotiable platform controls are documented in
[`docs/threat-model.md`](docs/threat-model.md).
