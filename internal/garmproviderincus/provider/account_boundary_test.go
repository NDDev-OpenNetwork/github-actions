package provider

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
)

// The prefix pins the account and the remainder must be one path segment, so
// an account-wide tenant cannot be used to reach another account's namespace
// through a crafted URL.
func TestAccountBoundaryAdmitsOneSegmentUnderTheAccountOnly(t *testing.T) {
	original := expectedAccountURLPrefix
	expectedAccountURLPrefix = map[string]struct{}{
		"https://github.com/example-media/": {},
	}
	t.Cleanup(func() { expectedAccountURLPrefix = original })

	admitted := []string{
		"https://github.com/example-media",
		"https://github.com/example-media/example-service",
		"https://github.com/example-media/another-repository",
	}
	for _, url := range admitted {
		if !isRegisteredRepositoryURL(url) {
			t.Fatalf("the account's own repository was refused: %s", url)
		}
	}

	refused := []string{
		"https://github.com/example-media/nested/path",
		"https://github.com/example-media-Evil/repository",
		"https://github.com/Someone-Else/repository",
		"https://github.com/example-media/repo/../../Someone-Else/repo",
	}
	for _, url := range refused {
		if isRegisteredRepositoryURL(url) {
			t.Fatalf("boundary admitted %s", url)
		}
	}
}

// The registry-wide boundary answers "is this any tenant we serve", which was
// the only question that existed while the fleet served one account. A pool
// asks a narrower one: it is declared for a tenant, carries that tenant's trust
// class and cache write scope, and hands the worker a credential chosen from
// them. Admitting another registered tenant's repository onto it gives that job
// the declaring tenant's privileges, and every layer above the provider says
// yes -- the App is installed, the entity exists, the scale set is enabled, and
// the queue intent is for a real account. Only this check refuses.
func TestPoolTenantBoundaryRefusesAnotherRegisteredTenant(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	// The reference host configuration declares no pool tenant, so this test
	// states the narrow rule against a tenant that was declared. The undeclared
	// case is a different rule and is held by the test below it.
	selected, err := tenant.ByID("example")
	if err != nil {
		t.Fatal(err)
	}
	declarePoolTenant(t, provider, "nddev-linux-standard", "example")

	for _, testCase := range []struct {
		name       string
		repository string
		admitted   bool
	}{
		{"own repository", "https://github.com/example-org/example-actions", true},
		{"own account, organization entity", "https://github.com/example-org", true},
		{"own account, another repository", "https://github.com/example-org/gds", true},
		// Both are in the registry, so isRegisteredRepositoryURL admits them.
		// Neither belongs to the tenant this pool was declared for.
		{"another registered tenant", "https://github.com/example-guild/example-project", false},
		{"another whole-account tenant", "https://github.com/example-media/anything", false},
		{"lookalike account", "https://github.com/example-org-evil/repository", false},
		{"nested path under the account", "https://github.com/example-org/a/b", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := repositoryWithinTenant(selected, testCase.repository); got != testCase.admitted {
				t.Fatalf("repositoryWithinTenant(%q) = %v, want %v", testCase.repository, got, testCase.admitted)
			}

			bootstrap := validBootstrap()
			bootstrap.RepoURL = testCase.repository
			err := provider.validateBootstrapParams(bootstrap)
			if testCase.admitted {
				if err != nil {
					t.Fatalf("validateBootstrapParams refused an in-tenant repository: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateBootstrapParams admitted a repository outside the pool's tenant")
			}
		})
	}
}

// A tenant that did not declare it serves a whole account keeps the tightest
// boundary the fleet can state: exactly the one repository the registry names.
func TestPoolTenantBoundaryKeepsSingleRepositoryTenantsNarrow(t *testing.T) {
	guild, err := tenant.ByID("example-guild")
	if err != nil {
		t.Fatal(err)
	}
	if guild.ServesWholeAccount {
		t.Skip("guild now serves a whole account; this case needs a different row")
	}
	if !repositoryWithinTenant(guild, "https://github.com/"+guild.Repository) {
		t.Fatal("a single-repository tenant was refused its own repository")
	}
	for _, refused := range []string{
		"https://github.com/" + guild.Owner,
		"https://github.com/" + guild.Owner + "/another-repository",
	} {
		if repositoryWithinTenant(guild, refused) {
			t.Fatalf("a single-repository tenant reached %s", refused)
		}
	}
}

// A pool that declares no tenant must keep the registry-wide boundary, not
// acquire the fleet's own account as if someone had written it down.
//
// Pool.TenantID defaults an undeclared tenant to nddev, which is right for
// reading a configuration and wrong for deciding admission. Reading that
// default as a declaration refused every other registered tenant on every
// deployed host: gha-runner-2 serves example-guild/example-project through its own
// scale set on this same pool, and every create was refused with "outside the
// boundary of tenant nddev declared by pool nddev-linux-integration" -- for a
// pool that declared nothing.
func TestAPoolThatDeclaresNoTenantKeepsTheRegistryBoundary(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	if _, declared, err := provider.poolTenant("nddev-linux-standard"); err != nil || declared {
		t.Fatalf("poolTenant reported declared=%v err=%v for a pool that declares nothing", declared, err)
	}
	for _, admitted := range []string{
		"https://github.com/example-org/example-actions",
		"https://github.com/example-guild/example-project",
	} {
		bootstrap := validBootstrap()
		bootstrap.RepoURL = admitted
		if err := provider.validateBootstrapParams(bootstrap); err != nil {
			t.Fatalf("a registered repository was refused by an undeclared pool tenant: %v", err)
		}
	}
	// The registry is still the boundary; it did not become unbounded.
	bootstrap := validBootstrap()
	bootstrap.RepoURL = "https://github.com/someone-else/repository"
	if err := provider.validateBootstrapParams(bootstrap); err == nil {
		t.Fatal("an unregistered account was admitted")
	}
}

func declarePoolTenant(t *testing.T, provider *Incus, flavor, id string) {
	t.Helper()
	for index := range provider.platform.Pools {
		if provider.platform.Pools[index].Name == flavor {
			provider.platform.Pools[index].Tenant = id
			return
		}
	}
	t.Fatalf("pool %q is not declared in the reference host configuration", flavor)
}
