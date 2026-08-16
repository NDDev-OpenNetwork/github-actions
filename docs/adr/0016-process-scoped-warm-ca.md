# ADR 0016: Process-scoped CA for warm activation

## Status

Accepted for canary deployment. The p95 latency gate remains open.

## Context

The first event-driven GARM canary reduced job creation-to-start from 19 to 12
seconds. Its remaining assignment-to-runner-listening interval was at most
7.766 seconds. The warm agent placed GARM's public callback CA into the global
system trust store and ran `update-ca-certificates` for every one-job
assignment. That repeated system mutation is unnecessary: only the installer's
short-lived curl processes connect to the private worker gateway. The official
runner connects to GitHub with the immutable image's normal system trust.

## Decision

Provider `v0.1.5-nddev.6` embeds the already-validated public CA bundle in the
root-owned assignment as base64 and stops injecting the separate CA sidecar.
The assignment, running as `runner`:

1. creates a private temporary CA file;
2. copies the immutable image's system bundle into it;
3. appends the decoded GARM callback bundle;
4. changes the file to mode `0400`;
5. exports only its path through `CURL_CA_BUNDLE` to the pinned GARM installer;
6. deletes the CA and installer files on every exit path.

The callback token and CA payload variables are not exported. Inherited Bash
xtrace remains redirected to a pre-opened `/dev/null` descriptor before the
upstream installer executes. The metadata and callback URLs remain exact
allowlisted TLS endpoints; certificate verification is not disabled.

The warm agent retains support for the old root-installed CA sidecar so current
golden images remain rollback-compatible, but the new provider never creates
that sidecar. A failed decode, empty bundle file or TLS validation stops
activation before the runner starts.

## Consequences

Warm assignment no longer mutates `/usr/local/share/ca-certificates` or
`/etc/ssl/certs`, removes one Incus file operation and moves CA assembly from a
global trust-store rebuild to a process-scoped file copy. Cold workers and the
official Actions runner are unchanged.

Runtime promotion requires an official-runner canary proving successful TLS,
zero token-shaped diagnostic matches, one-job destruction, clean replenishment
and a measured latency comparison. The previous provider binary remains the
rollback artifact until the production reliability gate passes.
