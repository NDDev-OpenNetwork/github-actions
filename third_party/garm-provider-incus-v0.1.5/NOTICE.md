# garm-provider-incus attribution

The source under `internal/garmproviderincus/` and the executable entry point
under `cmd/garm-provider-incus-nddev/` are derived from
[`cloudbase/garm-provider-incus`](https://github.com/cloudbase/garm-provider-incus),
release `v0.1.5`, source commit
`f3ae31910c6443c31d841de268a377985e7c60a5`.

Copyright 2023 Cloudbase Solutions SRL. Licensed under the Apache License,
Version 2.0. A copy of that license is included in this directory.

NDDev modifications made in 2026:

- integrated the provider into the `github-actions` Go module;
- moved the Incus client imports from the vulnerable legacy SDK `v0.7.0` to
  the current major-version SDK `v7.3.0` while retaining the Incus 6.0 LTS API
  compatibility contract;
- rejected unknown provider configuration keys;
- required the Incus loopback TLS API and file-backed client authentication;
- disabled Unix-socket access and remote image sources;
- pinned one local image alias to one immutable SHA-256 fingerprint;
- restricted workers to Linux/amd64 full VMs using one-job JIT bootstrap;
- applied fixed secure-boot and nested-virtualization-denial instance policy;
- added provider provenance tags and ownership checks before mutations;
- replaced the upstream tests with hardened contract and ownership tests.

No upstream trademark endorsement is implied. Release binaries must also carry
the license material for their linked Go dependencies; the release pipeline
produces that inventory from the checked-in `go.mod` and `go.sum`.
