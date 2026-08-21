package tenant

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicTenantRegistryIsStrictAndEquivalentToExampleFixture(t *testing.T) {
	t.Parallel()
	registry, err := Load(filepath.Join("..", "..", "config", "tenant-registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if registry.DefaultID != ExampleRegistry().DefaultID || strings.Join(registry.IDs(), ",") != strings.Join(ExampleRegistry().IDs(), ",") {
		t.Fatalf("public registry=%#v example=%#v", registry, ExampleRegistry())
	}
	selected, err := registry.ByID("example")
	if err != nil {
		t.Fatal(err)
	}
	if repository, ok := selected.RepositoryForScaleSet("nddev-priority-standard"); !ok || repository != "example-org/example-library" {
		t.Fatalf("Priority binding=%q,%v", repository, ok)
	}
}

func TestTenantRegistryRejectsUnknownOrAmbiguousState(t *testing.T) {
	t.Parallel()
	valid := `schema_version: 1
default_tenant: one
tenants:
  - id: one
    owner: example-one
    repository: example-one/repository
    managed_repositories: [example-one/repository]
    app_slug: example-one-app
    credential_name: example-one-app
    homepage_url: https://github.com/example-one/repository
    serves_whole_account: false
`
	for name, content := range map[string]string{
		"unknown field":      valid + "unknown: true\n",
		"multiple documents": valid + "---\nschema_version: 1\n",
		"unmanaged binding":  strings.Replace(valid, "    serves_whole_account: false\n", "    serves_whole_account: false\n    scale_set_repositories: {class: example-one/other}\n", 1),
		"missing default":    strings.Replace(valid, "default_tenant: one", "default_tenant: absent", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(content)); err == nil {
				t.Fatal("invalid registry was accepted")
			}
		})
	}
}
