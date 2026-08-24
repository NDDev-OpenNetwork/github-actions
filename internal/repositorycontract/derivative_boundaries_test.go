package repositorycontract

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProviderDerivativeExcludesObserverOnlyRetryReader(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository contract test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	content, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(content)
	if strings.Contains(workflow, "internal/providerretry/") {
		t.Fatal("observer-only providerretry package still invalidates the external provider artifact")
	}
	for _, required := range []string{
		"cmd/garm-provider-incus-nddev/", "internal/garmproviderincus/",
		"internal/provideradmission/", "internal/providerjournal/", "internal/providerrelease/",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("provider derivative boundary omits %s", required)
		}
	}
}
