# ADR 0028: Process-scoped RustFS cache trust

- Status: accepted
- Date: 2026-08-10

## Context

Direct-JIT phase telemetry proved a representative warm activation reached the
external provider 862 ms after `JobAssigned` and completed provider activation
in 328 ms. Before starting the official runner, the per-job cache setup still
installed the RustFS CA under `/usr/local/share/ca-certificates` and rebuilt the
VM-wide trust store with `update-ca-certificates`. The VM is disposable, but a
global rebuild on every assignment is unnecessary latency and expands mutable
state beyond the job processes that need the private cache endpoint.

## Decision

Provider `v0.1.5-nddev.20` writes two runner-owned, mode `0400` files inside
the one-job cache directory:

- the validated RustFS CA certificate;
- a combined bundle made from the immutable image's system bundle followed by
  the validated RustFS CA.

The official runner receives the combined path through its existing `.env` as
`SSL_CERT_FILE`, `CURL_CA_BUNDLE` and `AWS_CA_BUNDLE`. The job-start hook still
delivers scoped RustFS credentials only after GitHub creates `GITHUB_ENV`.
Neither credentials nor the private CA are added to the golden image.

The setup must not call `update-ca-certificates` and must not write under
`/usr/local/share/ca-certificates`. The combined bundle retains public GitHub
trust while extending trust only for processes descended from this one-job
runner. Both files disappear with the VM.

## Consequences

The activation path avoids a global trust-store rebuild and has less mutable
surface. The standard canary now verifies ownership, permissions, all three
environment bindings, RustFS certificate validation and an authenticated
RustFS request. Promotion still requires a fresh statistical latency series;
the expected speedup is evidence to test, not an assumption.
