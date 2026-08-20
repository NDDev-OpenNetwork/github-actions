package tenant

import "testing"

// The values the fleet ran on before it could serve more than one account.
func TestDefaultTenantIsByteIdenticalToTheDeployedConstants(t *testing.T) {
	d, err := ByID(DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ got, want, field string }{
		{d.Owner, "example-org", "owner"},
		{d.Repository, "example-org/example-actions", "repository"},
		{d.AppSlug, "example-actions-fleet", "app slug"},
		{d.CredentialName, "example-actions-fleet", "credential name"},
	} {
		if c.got != c.want {
			t.Errorf("default tenant %s is %q, deployed constant was %q", c.field, c.got, c.want)
		}
	}
}
