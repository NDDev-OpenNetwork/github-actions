package deploycontract

import (
	"testing"
)

// ADR 0036 decided that tenancy is scoped to the entity a job arrived through,
// not to the pool it is placed on. This holds the configuration to that
// decision, because the obvious-looking alternative is destructive and #240
// still proposes it in writing.
//
// Declaring `tenant:` on a pool refuses every other tenant's jobs on that pool
// at create time. On 2026-08-15 that would have broken two of the three hosts:
//
//	gha-runner-1  scaleset-worker-nddev-linux-standard-2     NDDev-OpenNetwork
//	gha-runner-1  scaleset-worker-nddev-linux-standard-3     example-media
//	gha-runner-2  scaleset-worker-nddev-linux-integration-3  example-guild/ai_stp
//
// Two entities, one pool name, one host. A pool is provider-side policy -- image,
// resources, trust class, cache scope -- and a scale set is GitHub-side routing
// for one entity. One image serving many entities is the point of publishing a
// class, not a leak.
//
// If a pool ever should carry a tenant, this test is where the reason gets
// written down, and ADR 0036 is what has to change first.
func TestNoPoolDeclaresATenantBecauseTenancyIsNotAPoolProperty(t *testing.T) {
	t.Parallel()
	for name, platform := range declaredHostConfigs(t) {
		for _, pool := range platform.Pools {
			if pool.Tenant == "" {
				continue
			}
			t.Errorf("%s pool %q declares tenant %q. A pool serves every entity whose "+
				"scale set is registered against it, so this refuses the others at create "+
				"time -- after the App is installed, the scale set is registered and the job "+
				"is assigned. Read docs/adr/0036-tenancy-is-scoped-to-the-entity-not-the-pool.md "+
				"before changing this.", name, pool.Name, pool.Tenant)
		}
	}
}
