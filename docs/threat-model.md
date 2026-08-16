# Threat model

Status: pilot security baseline. This model covers GitHub-hosted control traffic,
the NDDev runner host, disposable worker VMs and local cache services.

## Assets

- GitHub App private key and runner-registration authority;
- repository, release and deployment credentials;
- source code and private artifacts;
- CI host, Incus API, manager journal and management network;
- golden images and update metadata;
- trusted compiler caches and promoted release inputs;
- ExamplePlatform and Captcha production tenants sharing the current virtual host;
- diagnostic logs, which may contain data that GitHub masking did not redact.

## Adversaries and failure sources

- malicious code in a dependency, action, branch, pull request or fork;
- compromised repository credentials or GitHub account;
- cache-poisoning producer;
- vulnerable or malicious runner/provider/image dependency;
- accidental operator action, stale automation or duplicate webhook;
- resource-exhaustion burst from many coding-agent commits;
- failed disk, host reboot, network partition or external GitHub outage.

Workflow code is treated as hostile to its worker even for private repositories.
Release jobs are not trusted merely because they run on a protected branch.

## Trust boundaries

1. GitHub control plane to manager GitHub App.
2. Manager to Incus management API.
3. Host kernel/hypervisor to disposable worker VM.
4. Worker VM to RustFS and the OCI registry.
5. Worker egress to GitHub and the public internet.
6. Trusted, untrusted and release cache namespaces.
7. CI platform services to retained ExamplePlatform/Captcha tenants.

The full VM is the general workflow-code security boundary. A Docker container
inside that VM is a workload mechanism, not another host trust boundary.

## Required controls

| Threat | Required control |
| --- | --- |
| cross-job persistence | one job per immutable full VM; destroy after execution |
| host takeover through Docker | never expose host Docker/Incus sockets or devices |
| nested hypervisor abuse | remove VMX/SVM in QEMU; prove `/dev/kvm` absent in every image smoke |
| duplicate/orphan workers | durable idempotency, leases, bidirectional reconciliation |
| secret theft from untrusted PR | GitHub-hosted or no-secret disposable pool, no private routes |
| cache poisoning | trust-scoped namespaces, immutable promotion, no release writes |
| manager credential theft | GitHub App key only on manager, least privilege, rotation |
| image compromise | reproducible build, digest pin, scan, parity test, canary promotion |
| update outage | staged image rollout and retained previous digest |
| resource starvation | host reserve, quotas, weighted fairness, disk circuit breaker |
| log secret exposure | external encrypted storage, redaction, access control, retention |
| lateral movement | default-deny worker network and explicit per-pool egress |
| production tenant impact | separate resources/networks now; separate host failure domain later |

## Non-negotiable prohibitions

- no host `/var/run/docker.sock` or Incus socket in a worker;
- no persistent general-purpose runner;
- no shared writable workspace between jobs;
- no registration token, PAT, App key or deploy credential in an image;
- no public/fork code on a trusted runner group;
- no route from a general worker to host management or production networks;
- no shared writable cache from untrusted code into trusted or release jobs;
- no `pull_request_target` execution of an untrusted checkout;
- no floating image, action or runner dependency in production;
- no destructive cleanup target resolved from a broad path or unverified glob.

## Cache policy

RustFS access is granted with bucket/prefix-scoped credentials. Namespace keys
include repository, trust, platform, architecture, toolchain, dependency-lock
digest and ref class. Untrusted writers receive an isolated prefix. Release jobs
cannot write the shared cache and consume only explicitly promoted immutable
objects for security-sensitive inputs.

Cache entries are performance hints, never the sole copy of source or release
artifacts. Every plane has quota, TTL and garbage collection. Admission stops
before free disk falls below the configured safety threshold.

For the Incus 6.0 bridge pilot, the default-deny ACL governs host and external
traffic at network level. This LTS does not accept ACL properties on bridged
NICs, so `security.port_isolation` blocks intra-bridge traffic while IPv4 and
MAC anti-spoofing constrain each NIC. The host's public address, RFC1918
networks, link-local metadata ranges and sensitive bridge ports are explicit
rejects; reject precedence prevents a broader allow from overriding them. A
second, reconciled UFW layer retains deny-by-default host input and forwarding.
Its bridge exceptions are limited to DHCP, DNS, the two scoped local cache
ports, the restricted GARM worker-gateway port and TCP 80/443 forwarding from
the worker subnet; unrelated UFW rules are never adopted or deleted by the
fleet reconciler. GARM's administrative API stays on loopback and is not routed
through that gateway.

Incus 6.0.0 does not enforce `security.nesting=false` for virtual machines.
The project therefore allows VM low-level instance configuration so the
trusted image builder and NDDev-pinned provider can apply exactly
`raw.qemu=-cpu host,-vmx,-svm`. This is a deliberate reduction in
defense-in-depth at the manager-to-Incus boundary, not authority granted to a
worker: containers and container low-level features are disabled, the worker
cannot reach the Incus API/socket, and the provider does not expose arbitrary
QEMU arguments as pool extra specs. Compromise of the manager/provider remains
equivalent to compromise of the VM provisioning boundary and is covered by
binary pinning, restricted service identity and canary rollout.

## Release pool

The release pool has a distinct runner group and repository allowlist, a separate
image, no warm capacity, no mutable cache writes, and explicit egress allowlists.
Cloud and deployment access uses GitHub OIDC and short-lived credentials. A
release job cannot inherit a normal build worker merely because labels overlap.

## Residual risk

- One virtual runner host remains a single failure and compromise domain.
- GitHub remains an external control-plane dependency.
- Full VMs reduce but do not eliminate hypervisor/kernel escape risk.
- GARM's Incus provider needs runtime fault-injection evidence before production.
- RustFS version and durability behavior need workload and recovery validation.
- Diagnostics collected outside GitHub may bypass GitHub's masking behavior.

Risk acceptance for production requires the gates in `docs/roadmap.md`; design
review or a successful happy-path run alone is insufficient.
