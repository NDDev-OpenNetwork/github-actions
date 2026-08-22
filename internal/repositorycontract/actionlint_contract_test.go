package repositorycontract

import (
	"os"
	"sort"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/garmbootstrap"
	"gopkg.in/yaml.v3"
)

// Actionlint's runner vocabulary is a public API: a missing label makes a
// published class unusable, while an extra label lets workflows request a
// class the fleet can never serve. Hold both directions to one authority.
func TestActionlintLabelsExactlyMatchPublishedClasses(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(repositoryRoot + "/.github/actionlint.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		SelfHostedRunner struct {
			Labels []string `yaml:"labels"`
		} `yaml:"self-hosted-runner"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	declared := append([]string(nil), config.SelfHostedRunner.Labels...)
	wanted := make([]string, 0, len(garmbootstrap.PublishedScaleSets()))
	for _, class := range garmbootstrap.PublishedScaleSets() {
		wanted = append(wanted, class.Name)
	}
	sort.Strings(declared)
	sort.Strings(wanted)
	if len(declared) != len(wanted) {
		t.Fatalf("actionlint declares %d runner labels, fleet publishes %d\ndeclared=%v\npublished=%v", len(declared), len(wanted), declared, wanted)
	}
	for index := range wanted {
		if declared[index] != wanted[index] {
			t.Fatalf("actionlint runner labels differ from the published fleet classes\ndeclared=%v\npublished=%v", declared, wanted)
		}
		if index > 0 && declared[index] == declared[index-1] {
			t.Fatalf("actionlint runner label %q is duplicated", declared[index])
		}
	}
}
