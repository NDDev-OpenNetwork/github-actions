package tenant

import "testing"

// The values the fleet ran on before it could serve more than one account.
func TestDefaultTenantIsByteIdenticalToTheDeployedConstants(t *testing.T) {
	d, err := ByID(DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ got, want, field string }{
		{d.Owner, "NDDev-OpenNetwork", "owner"},
		{d.Repository, "NDDev-OpenNetwork/github-actions", "repository"},
		{d.AppSlug, "nddev-gha-fleet", "app slug"},
		{d.CredentialName, "nddev-gha-fleet", "credential name"},
	} {
		if c.got != c.want {
			t.Errorf("default tenant %s is %q, deployed constant was %q", c.field, c.got, c.want)
		}
	}
}
