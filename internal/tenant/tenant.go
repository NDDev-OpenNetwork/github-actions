// Package tenant enumerates the accounts this fleet may serve.
//
// It sits outside both the GARM reconciler and the GitHub App bootstrapper
// because it is a fleet-wide fact that each of them must read the same way: an
// App registered for an account the reconciler would refuse is a deployment
// that fails after the one irreversible step in the whole flow.
package tenant

import (
	"fmt"
	"slices"
)

// Tenant is one account this fleet may serve.
//
// The set is closed and compiled in. Serving a second account widens who can
// reach these pools, and that widening is the whole security question here, so
// it is enumerated in source where it shows up in a diff and a review — not
// read from a file, where an owner is one typo away from pointing the fleet at
// an account nobody meant to serve. Every identity check in this package
// compares against the selected tenant and fails closed on anything else,
// exactly as it did when there was one account and the values were constants.
//
// One tenant is one GitHub App. The App stays private and owned by the account
// it serves, so GitHub itself still permits exactly one installation, on that
// account, covering only its repositories. Multi-tenancy repeats that control
// per account rather than relaxing it: two private Apps, each locked to its own
// owner, is not the same thing as one App reachable by two owners.
type Tenant struct {
	// ID selects the tenant on the command line. It is not an identity and is
	// never compared against anything GitHub or GARM returns.
	ID string
	// Owner is the account that owns the App, receives its installation, and
	// backs the organization entity.
	Owner string
	// Repository is the exact repository the repository entity serves, in
	// `owner/name` form. It always belongs to Owner.
	Repository string
	// ManagedRepositories is the closed set that may receive a repository-
	// scoped Scale Set from this tenant's App credential. Organization-scoped
	// serving remains available when ServesWholeAccount is true, but an exact
	// repository entity needs this reviewed identity before GARM mutation.
	ManagedRepositories []string
	// AppSlug is the slug the verified installation metadata must carry.
	AppSlug string
	// CredentialName is the GARM credential this tenant's entities bind to.
	// Distinct per tenant so one GARM can hold both without either entity
	// silently adopting the other's credential.
	CredentialName string
	// HomepageURL is the exact homepage the App manifest must declare.
	HomepageURL string
	// ServesWholeAccount widens this tenant's provider boundary from the one
	// repository above to every repository Owner holds. Set it only for a
	// tenant whose scale sets hang from an organization entity, because that
	// is the entity that receives jobs from repositories Repository does not
	// name. Leaving it false keeps the tightest boundary the fleet can state.
	//
	// It is declared rather than inferred: the provider is the layer that
	// does not trust what GARM hands it, so how wide it may be is an
	// onboarding decision with a review attached, not a runtime observation.
	ServesWholeAccount bool
}

// DefaultID is the account the fleet ran on before it could serve more
// than one, and stays the default so an operator who passes no tenant gets the
// behaviour that is already deployed.
const DefaultID = "example"

// : The closed set. Adding a row is a deliberate act with a review attached.
var tenants = map[string]Tenant{
	DefaultID: {
		ID:             DefaultID,
		Owner:          "example-org",
		Repository:     "example-org/example-actions",
		AppSlug:        "example-actions-fleet",
		CredentialName: "example-actions-fleet",
		HomepageURL:    "https://github.com/example-org/example-actions",
		ManagedRepositories: []string{
			"example-org/example-actions",
			"example-org/example-library",
		},
		// The NDDev production entity is organization-scoped so the same
		// reviewed fleet can replace the legacy organization runners for GDS
		// and the other repositories owned by this account.
		ServesWholeAccount: true,
	},
	"example-guild": {
		ID:             "example-guild",
		Owner:          "example-guild",
		Repository:     "example-guild/example-project",
		AppSlug:        "example-guild-fleet",
		CredentialName: "example-guild-fleet",
		HomepageURL:    "https://github.com/example-guild/example-project",
		ManagedRepositories: []string{
			"example-guild/example-project",
		},
		// This account's scale sets hang from an organization entity like the
		// other two, so its jobs arrive from repositories the row above does
		// not name. Leaving the boundary at the single repository meant the
		// provider refused every create for the rest of the account, after
		// GARM and GitHub had both already said yes.
		ServesWholeAccount: true,
	},
	"example-media": {
		ID:             "example-media",
		Owner:          "example-media",
		Repository:     "example-media/example-service",
		AppSlug:        "example-media-fleet",
		CredentialName: "example-media-fleet",
		HomepageURL:    "https://github.com/example-media/example-service",
		ManagedRepositories: []string{
			"example-media/example-service",
		},
		// Eight repositories send jobs to this account's scale sets, and the
		// row above names one. Its entity is an organization, so the provider
		// boundary is the account rather than that single repository.
		ServesWholeAccount: true,
	},
}

// TenantByID returns the tenant an operator selected, or refuses. An unknown id
// is a refusal rather than a fallback to the default: silently serving the
// wrong account is the failure this whole file exists to prevent.
func ByID(id string) (Tenant, error) {
	if id == "" {
		id = DefaultID
	}
	tenant, ok := tenants[id]
	if !ok {
		return Tenant{}, fmt.Errorf("unknown tenant %q", id)
	}
	return tenant, nil
}

// WithRepository selects one reviewed repository entity without widening the
// tenant or accepting an arbitrary same-owner path. An empty repository keeps
// the tenant's historical primary entity.
func WithRepository(selected Tenant, repository string) (Tenant, error) {
	if repository == "" || repository == selected.Repository {
		return selected, nil
	}
	if !slices.Contains(selected.ManagedRepositories, repository) {
		return Tenant{}, fmt.Errorf("repository %q is not a managed repository of tenant %q", repository, selected.ID)
	}
	selected.Repository = repository
	return selected, nil
}

// TenantIDs lists the selectable tenants for help text and error messages.
func IDs() []string {
	ids := make([]string, 0, len(tenants))
	for id := range tenants {
		ids = append(ids, id)
	}
	return ids
}

// RepositoryURLs is the closed set of exact repository URLs the fleet may
// build a runner for. The provider asserts a boundary rather than trusting
// whatever GARM hands it, and before multi-tenancy that boundary was a single
// constant. Deriving it here keeps the boundary closed and code-reviewed while
// making it exactly as wide as the registry above, so onboarding a tenant is
// one place.
func RepositoryURLs() map[string]struct{} {
	urls := make(map[string]struct{})
	for _, tenant := range tenants {
		for _, repository := range tenant.ManagedRepositories {
			urls["https://github.com/"+repository] = struct{}{}
		}
	}
	return urls
}

// AccountURLPrefixes is the set of account prefixes whose every repository the
// fleet may build a runner for. It holds only tenants that declared they serve
// a whole account, because an organization entity receives jobs from
// repositories the registry never names one by one -- which is otherwise a
// create refused per retry, after every other layer has already said yes.
func AccountURLPrefixes() map[string]struct{} {
	prefixes := make(map[string]struct{}, len(tenants))
	for _, tenant := range tenants {
		if tenant.ServesWholeAccount {
			prefixes["https://github.com/"+tenant.Owner+"/"] = struct{}{}
		}
	}
	return prefixes
}

// validate rejects a registry row that could not describe a real account. It
// runs in a test rather than at init: a malformed row is a source defect to
// catch before shipping, not a runtime condition to handle.
func (t Tenant) Validate() error {
	if t.ID == "" || t.Owner == "" || t.AppSlug == "" || t.CredentialName == "" {
		return fmt.Errorf("tenant %q is missing a required field", t.ID)
	}
	if t.Repository != t.Owner+"/"+repositoryName(t.Repository) {
		return fmt.Errorf("tenant %q repository %q is outside its owner", t.ID, t.Repository)
	}
	if t.HomepageURL != "https://github.com/"+t.Repository {
		return fmt.Errorf("tenant %q homepage does not name its repository", t.ID)
	}
	if len(t.ManagedRepositories) == 0 || !slices.IsSorted(t.ManagedRepositories) {
		return fmt.Errorf("tenant %q managed repositories must be non-empty and sorted", t.ID)
	}
	for index, repository := range t.ManagedRepositories {
		if repository != t.Owner+"/"+repositoryName(repository) {
			return fmt.Errorf("tenant %q managed repository %q is outside its owner", t.ID, repository)
		}
		if index > 0 && repository == t.ManagedRepositories[index-1] {
			return fmt.Errorf("tenant %q managed repository %q is duplicated", t.ID, repository)
		}
	}
	if !slices.Contains(t.ManagedRepositories, t.Repository) {
		return fmt.Errorf("tenant %q primary repository is not managed", t.ID)
	}
	return nil
}

func repositoryName(repository string) string {
	for i := len(repository) - 1; i >= 0; i-- {
		if repository[i] == '/' {
			return repository[i+1:]
		}
	}
	return repository
}
