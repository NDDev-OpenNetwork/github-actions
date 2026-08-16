package tenant

import "testing"

// NDDev's production Scale Sets hang from the organization entity. Keeping
// this assertion explicit prevents a registry cleanup from silently shrinking
// the provider boundary back to github-actions and stranding every other
// repository, including GDS, after a job has already been assigned.
func TestNDDevOrganizationTenantServesItsAccount(t *testing.T) {
	row := tenants[DefaultID]
	if !row.ServesWholeAccount {
		t.Fatal("NDDev organization tenant must serve the whole account")
	}
	want := "https://github.com/NDDev-OpenNetwork/"
	if _, present := AccountURLPrefixes()[want]; !present {
		t.Fatalf("NDDev account boundary %q is missing", want)
	}
}

// A tenant that has not declared it serves a whole account keeps the tightest
// boundary the fleet can state: the one repository its row names.
func TestAccountPrefixesHoldOnlyTenantsThatDeclaredIt(t *testing.T) {
	prefixes := AccountURLPrefixes()
	for id, row := range tenants {
		prefix := "https://github.com/" + row.Owner + "/"
		_, present := prefixes[prefix]
		if present != row.ServesWholeAccount {
			t.Fatalf("tenant %q declares ServesWholeAccount=%t but its prefix present=%t",
				id, row.ServesWholeAccount, present)
		}
	}
}

// Every tenant keeps its exact repository regardless, so widening one tenant
// can never narrow another.
func TestExactRepositoryURLsCoverEveryTenant(t *testing.T) {
	urls := RepositoryURLs()
	if len(urls) != len(tenants) {
		t.Fatalf("exact boundary holds %d of %d tenants", len(urls), len(tenants))
	}
	for id, row := range tenants {
		if _, present := urls["https://github.com/"+row.Repository]; !present {
			t.Fatalf("tenant %q is missing from the exact boundary", id)
		}
	}
}
