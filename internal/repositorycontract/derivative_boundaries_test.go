package repositorycontract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The provider derivative boundary is the set of directories the provider
// binary compiles, and CI reads it from the build graph rather than from a list
// someone remembered to extend. This asserts the property both jobs depend on:
// every package the binary links is inside the boundary, and a package only the
// observer uses is outside it.
//
// It used to assert that five path strings appeared somewhere in ci.yml. That
// passed while the list named eight of eighteen packages, and a change to
// internal/cachebroker shipped under a manifest digest that no longer described
// main.
func TestProviderDerivativeBoundaryIsTheProviderBuildGraph(t *testing.T) {
	script := exec.Command("./scripts/provider-package-directories.sh")
	script.Dir = repositoryRoot
	output, err := script.CombinedOutput()
	if err != nil {
		t.Fatalf("provider-package-directories.sh: %v\n%s", err, output)
	}
	boundary := map[string]bool{}
	for _, line := range strings.Fields(string(output)) {
		boundary[line] = true
	}
	if len(boundary) == 0 {
		t.Fatal("provider derivative boundary is empty")
	}
	for _, required := range []string{
		"cmd/garm-provider-incus-nddev", "internal/garmproviderincus/provider",
		"internal/provideradmission", "internal/providerjournal", "internal/providerrelease",
		"internal/cachebroker", "internal/queueintent",
	} {
		if !boundary[required] {
			t.Fatalf("provider derivative boundary omits %s: %v", required, boundary)
		}
	}
	if boundary["internal/providerretry"] {
		t.Fatal("observer-only providerretry package still invalidates the external provider artifact")
	}
}

// The boundary is only useful if the workflow actually consults it. A job that
// stopped exporting the set would silently classify every change as
// provider=false, which is the failure this whole contract exists to prevent.
func TestWorkflowResolvesTheProviderBoundaryItEnforces(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repositoryRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(content)
	if strings.Count(workflow, "scripts/provider-package-directories.sh") != 2 {
		t.Fatal("both the classifier and the drift gate must resolve the boundary from the build graph")
	}
	if strings.Count(workflow, "PROVIDER_PACKAGE_DIRECTORIES") < 4 {
		t.Fatal("the resolved boundary is not passed to both jobs that enforce it")
	}
	if strings.Contains(workflow, "internal/providerretry/") {
		t.Fatal("observer-only providerretry package still invalidates the external provider artifact")
	}
}
