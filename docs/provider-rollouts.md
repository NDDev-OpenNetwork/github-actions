# Provider rollouts

`config/provider-rollout-contract.json` is the machine-readable rollout
contract for an Incus provider identity change. Provider binaries and the fleet
observer both resolve provider identity at process startup, so updating files
and restarting only GARM leaves inventory observation fail-closed and stale.

Apply the declared phases in order. Deploy and read back all platform policies
before activating the provider binary and config. Restart the manager, verify
queue identity preservation, then restart the observer. Acceptance requires a
fresh healthy sample, zero collection errors, inventory parity with the
provider, and a successful natural job bound to the new provider identity.

Do not restart worker VMs or manufacture a synthetic workload for acceptance.

## The five pins, measured

A provider identity bump moves five things as one operation, and each was
learned by omission on a live wave:

1. the binary at `/usr/local/libexec/gha-fleet/garm-provider-incus-nddev`;
2. `current_provider_identity` in `/etc/garm/provider-incus.toml`
   (mode 0640 root:garm is part of the contract);
3. `previous_provider_identities` — one release of rolling compatibility, the
   only thing letting in-flight work from the outgoing release finish;
4. the `platform.yaml` provider pin on all five hosts — the observer and
   controller resolve identity at startup, and a missed pin reads as
   `platform_unhealthy`;
5. **stale-stamped warm instances**. Warm must be exact-current. Since
   v0.1.5-nddev.107 the reconciler recycles an outdated-identity or
   outdated-image warm instance itself (`recycled_stale` in the pool
   result) and refills at the current release, so this pin self-heals
   within a timer cycle; verify by watching the stamps converge rather
   than deleting anything by hand. Before .107 the reconciler refused a
   stale inventory outright and `gha-warm-pool@` failed closed until the
   instances were deleted manually — the .102→.103 and .104→.105 waves
   both paid that toll, which is why the recycle is now code. A boundary
   violation (controller, trust, ownership, stopped, autostart) still
   fails the reconcile: currency is recyclable, contract is not.

Rollback is a rebuild: every release records `source_commit` and a
reproducible `binary_sha256` in `config/provider-derivative.yaml`, so no
binary backup copies are kept on hosts.

## The scripted wave

The estate carries the executable form of this page:
`deploy/github-actions/scripts/rollout-provider.sh <worktree> <version>`
(github-device-sync-estate). It refuses manifest/toml drift, rebuilds the
binary reproducibly from the manifest’s own `source_commit` and refuses a
sha mismatch, deploys services then members in the pin order above, and
verifies live identity, observer health, zero failed units and warm
convergence onto the new release. Edit and review the estate toml and the
five platform pins first — the script deploys reviewed state, it does not
invent it.
