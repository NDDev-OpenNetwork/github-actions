package repositorycontract

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/garmbootstrap"
	"gopkg.in/yaml.v3"
)

const canaryWorkflowPath = repositoryRoot + "/.github/workflows/self-hosted-canary.yml"

type canaryWorkflow struct {
	On struct {
		WorkflowDispatch struct {
			Inputs map[string]struct {
				Type    string   `yaml:"type"`
				Default string   `yaml:"default"`
				Options []string `yaml:"options"`
			} `yaml:"inputs"`
		} `yaml:"workflow_dispatch"`
	} `yaml:"on"`
}

// The canary is the only way to put a real job through a class before a
// consumer does, and it can only exercise a class its dispatch input offers. A
// class that is published and missing here is a class whose first real job is
// the proof, which is the opposite of what a canary is for -- nddev-linux-fast
// sat published and unofferable while it was also unbuildable everywhere.
//
// The list is held to garmbootstrap.PublishedScaleSets rather than to a second
// copy of the names, so registering a new class and forgetting to canary it
// fails here instead of in production.
func TestCanaryCanReachEveryPublishedClass(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(canaryWorkflowPath)
	if err != nil {
		t.Fatal(err)
	}
	var workflow canaryWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	input, exists := workflow.On.WorkflowDispatch.Inputs["runner_label"]
	if !exists {
		t.Fatal("the canary no longer takes a runner_label input")
	}
	if input.Type != "choice" {
		t.Fatalf("runner_label is a %q input; a free-text label cannot be held to the published set", input.Type)
	}
	for _, class := range garmbootstrap.PublishedScaleSets() {
		if !slices.Contains(input.Options, class.Name) {
			t.Fatalf("scale set %q is published and the canary cannot select it", class.Name)
		}
	}
	// The manual-JIT label is deliberately not a scale set and stays offered.
	for _, option := range input.Options {
		if option == "nddev-canary" {
			continue
		}
		if !slices.ContainsFunc(garmbootstrap.PublishedScaleSets(), func(c garmbootstrap.ScaleSetClass) bool {
			return c.Name == option
		}) {
			t.Fatalf("the canary offers %q, which no published class serves", option)
		}
	}
	if !strings.HasPrefix(input.Default, "nddev-") {
		t.Fatalf("the canary defaults to %q", input.Default)
	}
}
