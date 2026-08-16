package config

import (
	"strings"
	"testing"
)

// A GitHub scale set name is unique per forge entity, not globally. Refusing a
// second pool that shares the class name imposed a rule the forge does not
// have, and it was the reason a second tenant could not be declared on a host
// that had room for it. The local pool name stays unique because it is this
// host's own Incus identity.
func TestPoolsMayShareAClassNameAcrossTenants(t *testing.T) {
	platform, err := Load("../../config/server-gha-runner-2.yaml")
	if err != nil {
		t.Fatal(err)
	}
	integration, exists := platform.Pool("nddev-linux-integration")
	if !exists {
		t.Fatal("gha-runner-2 no longer declares the integration pool this test describes")
	}
	if integration.TenantID() != "nddev" {
		t.Fatalf("an undeclared tenant read as %q, want the fleet's own", integration.TenantID())
	}

	second := integration
	second.Name = "example-media-linux-integration"
	second.Tenant = "example-media"
	second.Warm = WarmPool{}
	platform.Pools = append(platform.Pools, second)
	if err := platform.Validate(); err != nil {
		t.Fatalf("a second tenant's pool sharing the class name was refused: %v", err)
	}

	// The same tenant twice is still a collision, because that pair is what
	// GitHub itself keeps unique.
	collision := integration
	collision.Name = "nddev-linux-integration-copy"
	platform.Pools = append(platform.Pools, collision)
	err = platform.Validate()
	if err == nil || !strings.Contains(err.Error(), "unique for its tenant") {
		t.Fatalf("one tenant declaring the class name twice was accepted: %v", err)
	}
}

// A pool naming a tenant the registry does not know would reconcile against an
// account the fleet holds no credential for, so it is refused where the policy
// is read rather than at the first API call.
func TestPoolTenantMustBeInTheClosedRegistry(t *testing.T) {
	platform, err := Load("../../config/server-gha-runner-2.yaml")
	if err != nil {
		t.Fatal(err)
	}
	platform.Pools[0].Tenant = "not-a-tenant"
	err = platform.Validate()
	if err == nil || !strings.Contains(err.Error(), "known tenant") {
		t.Fatalf("an unknown tenant was accepted: %v", err)
	}
}
