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
5. **stale-stamped warm instances**. Warm must be exact-current: the warm
   reconciler refuses a stale-identity inventory outright and the
   `gha-warm-pool@` units fail closed until the stale instances are deleted
   (`incus delete --project gha-fleet <warm-*> --force` from a member).
   Observed on the .102→.103 and again on the .104→.105 waves; once the
   reconciler happened to self-recycle, so check the stamps after every bump
   rather than assuming either outcome.

Rollback is a rebuild: every release records `source_commit` and a
reproducible `binary_sha256` in `config/provider-derivative.yaml`, so no
binary backup copies are kept on hosts.
