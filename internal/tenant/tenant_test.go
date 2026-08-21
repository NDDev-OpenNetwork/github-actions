package tenant

import "testing"

func TestEveryRegisteredTenantDescribesOneRealAccount(t *testing.T) {
	// A malformed row would bind the fleet to an account that cannot exist, and
	// the failure would surface as an opaque GitHub 404 during a deployment
	// rather than here.
	for id, selected := range tenants {
		if selected.ID != id {
			t.Errorf("tenant %q carries id %q", id, selected.ID)
		}
		if err := selected.Validate(); err != nil {
			t.Errorf("tenant %q: %v", id, err)
		}
	}
}

func TestTenantsDoNotShareACredentialName(t *testing.T) {
	// Two entities binding one credential is how a second account would end up
	// authenticating as the first. GARM matches credentials by name, so the
	// names have to be distinct before anything else can keep them apart.
	seen := map[string]string{}
	for id, selected := range tenants {
		if other, clash := seen[selected.CredentialName]; clash {
			t.Fatalf("tenants %q and %q share credential name %q", other, id, selected.CredentialName)
		}
		seen[selected.CredentialName] = id
	}
}

func TestTenantsDoNotShareAnOwnerOrApp(t *testing.T) {
	owners := map[string]string{}
	slugs := map[string]string{}
	for id, selected := range tenants {
		if other, clash := owners[selected.Owner]; clash {
			t.Errorf("tenants %q and %q claim owner %q", other, id, selected.Owner)
		}
		owners[selected.Owner] = id
		if other, clash := slugs[selected.AppSlug]; clash {
			t.Errorf("tenants %q and %q claim App slug %q", other, id, selected.AppSlug)
		}
		slugs[selected.AppSlug] = id
	}
}

func TestAnUnknownTenantIsRefusedRatherThanDefaulted(t *testing.T) {
	// The failure this guards is silent: falling back to the default would run
	// a reconciliation against the wrong account and report success.
	if _, err := ByID("does-not-exist"); err == nil {
		t.Fatal("an unknown tenant id was accepted")
	}
	selected, err := ByID("")
	if err != nil || selected.ID != DefaultID {
		t.Fatalf("empty id resolved to %+v, %v; want the default tenant", selected, err)
	}
}

func TestRepositoryEntitySelectionIsClosedAndExact(t *testing.T) {
	selected, err := ByID(DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	priority, err := WithRepository(selected, "example-org/example-library")
	if err != nil {
		t.Fatal(err)
	}
	if priority.Repository != "example-org/example-library" || selected.Repository != "example-org/example-actions" {
		t.Fatalf("repository selection mutated the registry or chose the wrong target: selected=%q priority=%q", selected.Repository, priority.Repository)
	}
	for _, repository := range []string{
		"example-org/public-repository",
		"another-owner/priority-library",
		"example-org/example-library/extra",
	} {
		if _, err := WithRepository(selected, repository); err == nil {
			t.Errorf("unreviewed repository %q was accepted", repository)
		}
	}
}
