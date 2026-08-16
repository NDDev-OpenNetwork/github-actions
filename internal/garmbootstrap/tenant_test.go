package garmbootstrap

import (
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
)

// testTenant is the account these tests were written against, named once so a
// signature change does not scatter the same literal through every case.
func testTenant() tenant.Tenant {
	selected, err := tenant.ByID(tenant.DefaultID)
	if err != nil {
		panic(err)
	}
	return selected
}

func loadCredentialAnchorForTest(path string) (credentialAnchor, error) {
	return loadCredentialAnchor(path, testTenant())
}

func loadAppBundleForTest(directory string, now time.Time) (appBundle, error) {
	return loadAppBundle(directory, testTenant(), now)
}
