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
