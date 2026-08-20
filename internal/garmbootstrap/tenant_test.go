package garmbootstrap

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
)

func TestPublishedScaleSetCatalogContainsNoRepositoryIdentity(t *testing.T) {
	raw, err := json.Marshal(PublishedScaleSets())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "example-org/") || strings.Contains(string(raw), "github.com/") {
		t.Fatalf("public class catalog contains repository identity: %s", raw)
	}
}

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
