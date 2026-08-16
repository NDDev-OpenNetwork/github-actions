# ADR 0005: bounded LVM storage for the single-host pilot

- Status: accepted for pilot
- Date: 2026-08-08

## Context

`server-example-legacy` currently exposes one 640-GB virtual ext4 root disk.
The filesystem is not mounted with project quotas and has no independent block
device for CI. Incus `dir` storage would therefore be both slower for image and
VM cloning and unable to enforce the required per-project disk ceiling. Incus
explicitly advises against Btrfs for virtual-machine workloads. Repartitioning
the production root disk would add unnecessary tenant risk.

The current server is a transitional DigitalOcean KVM guest, not the dedicated
NVMe host assumed by the target architecture. The storage choice must therefore
be bounded, reversible at the platform-resource level and clearly non-final.

## Decision

Create a 120-GiB loop-backed Incus LVM thin pool named `gha-lvm` with thin pool
`gha-thin`. Restrict project `gha-fleet` to 100 GiB aggregate disk use, at most
two VMs, four aggregate CPUs and 12,288 MiB aggregate memory. Only the cold
`nddev-linux-standard` profile is materialized initially.

The reconciler creates a missing pool but never changes or replaces an existing
pool in place. A driver, size or thin-pool mismatch is a hard failure requiring
operator investigation. No automated cleanup path deletes a storage pool.

## Consequences

- The pilot has enforceable capacity despite the host filesystem lacking
  project quotas.
- Thin provisioning enables VM images and snapshots without choosing the slow
  `dir` backend.
- The loop file still shares the production root disk and its failure domain;
  it is suitable for a bounded pilot, not for the final performance target.
- RustFS and the OCI registry receive separate quotas and paths; they do not use
  the VM thin pool or any ExamplePlatform/Captcha volume.
- Before production scale or warm-pool enablement, migrate `gha-lvm` to a
  dedicated measured block device/NVMe failure domain and repeat cold/warm,
  saturation, disk-pressure and recovery benchmarks.

## Capacity amendment: 200 GiB

The initial image pipeline intentionally retained a source image, the current
16-GiB optimized worker and the previous 50-GiB rollback image. After those
images and two manual disposable-VM proofs, Incus reported 101.39 GiB reserved
in the 119.76-GiB pool. This left less than the platform's 20-percent disk
headroom before the first GARM-managed VM even though LVM thin data and metadata
usage remained healthy at 58.15 and 26.51 percent.

The pilot pool is therefore grown once from 120 to 200 GiB before Scale Set
enablement. The project ceiling remains 100 GiB, maximum running capacity
remains one standard VM, and no image is deleted: current, source and rollback
identities remain available. On the 640-GB root filesystem the loop file used
106 GiB physically before growth and 336 GiB remained available. Even if the
loop reaches its new limit and a separate 100-GiB cache budget is consumed, the
root filesystem retains more than the required 20-percent reserve.

This is the only accepted in-place storage mutation. It uses Incus' documented
grow-only `size` operation for an Incus-managed loop pool and requires the
`storage_pool_loop_resize` API extension, an empty instance inventory and exact
pre/post checks. The normal reconciler remains fail-closed for storage drift;
after the controlled grow, its canonical desired size is 200 GiB and the second
apply must again be empty.

### Runtime evidence

The controlled grow completed at `2026-08-08T10:42:09Z` from merged commit
`d2ab1b127ee96bf0c6edcff598e7e81b0243f74d`. The installed controller SHA-256
was `7058c0ab54f49ef5e7af13baee3cee29356195b5856f371ff275c4f0e32342c0`;
the installed platform fingerprint became
`sha256:e98b695bf4029e479e7ef361f282f59a5a71ca260b1ebe29d629df52c34df6d5`.

Incus changed only the managed loop-pool size. The loop file became exactly
`214748364800` bytes and the thin data LV became `214324740096` bytes. Data and
metadata usage fell from `58.15%`/`26.51%` to `34.89%`/`18.07%`. Sorted alias
and image-inventory digests remained respectively
`40ad79513e4a4b5754a4dfca4040996585c3322d0bb7370c106665031e2ab7aa` and
`2330d253c8c65602d1c6c308f675231dc1196532c862be75a57640271cba6c80`.
The canonical reconciliation returned `changes: []`, cold preflight remained
`pilot_ready`, the instance inventory remained empty and every service,
legacy listener and retained application passed its unchanged-state check.
