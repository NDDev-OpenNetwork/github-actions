# ADR 0007: close nested virtualization on Incus 6.0 workers

Status: accepted for the secure pilot on 2026-08-08.

## Context

The Example legacy host is itself a DigitalOcean KVM guest and must expose nested
KVM to run full worker VMs. Workflow code inside a worker must not receive the
same capability. Incus 6.0.0 accepts `security.nesting=false` on a VM profile,
but a runtime probe showed VMX in `/proc/cpuinfo` and `/dev/kvm` in the guest.
The exact upstream `v6.0.0` QEMU driver always emits `-cpu host,hv_passthrough`
and contains no VM implementation for that setting.

The stock GARM Incus provider v0.1.5 creates a fixed instance-config map and
does not accept instance config through its validated pool `extra_specs`.
Putting `raw.qemu` on the Incus profile would require allowing low-level
configuration for both containers and VMs because profiles are not typed.

## Decision

Keep the Ubuntu-supported Incus 6.0 LTS baseline for the coexistence pilot and
set exactly `raw.qemu=-cpu host,-vmx,-svm` on every VM instance before it is
started. The value is a compiled policy constant, not operator- or pool-supplied
input. Use it in the golden-image builder, image smoke VM and an exact-source
NDDev derivative of provider v0.1.5.

Keep `security.nesting=false` on the managed VM profile, where Incus 6.0 accepts
it, but do not duplicate it in the individual VM configuration sent by the
provider. A managed Scale Set canary proved that this host's Incus 6.0 API
rejects the duplicated instance key as unknown before creating a VM. Expanded
instance configuration must still contain the profile-owned value, and the
provider continues to verify it when adopting or reconciling a worker.

Set `restricted.virtual-machines.lowlevel=allow` only in the dedicated
`gha-fleet` project so Incus accepts that instance field. Explicitly retain
`restricted.containers.lowlevel=block` and `limits.containers=0`. Do not place
the value on a profile. Workers receive neither the Incus socket nor the HTTPS
API route. The image smoke must fail unless VMX, SVM and `/dev/kvm` are all
absent.

## Consequences

- Workers retain host CPU performance while losing nested-hypervisor features.
- A compromised manager/provider can submit other low-level VM configuration;
  this is a documented manager-boundary risk and requires a pinned binary,
  restricted identity, canary rollout and no worker API access.
- The provider derivative stays small and can be dropped when an Incus LTS
  whose VM driver enforces `security.nesting=false` passes the same runtime
  probe.
- An Incus package migration is avoided while legacy runners and production
  tenants share the host; a later upgrade remains a separately gated change.
