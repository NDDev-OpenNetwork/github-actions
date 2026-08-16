package repositorycontract

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NDDev-OpenNetwork/github-actions/internal/garmbootstrap"
)

const ciWorkflowPath = repositoryRoot + "/.github/workflows/ci.yml"

// gateJob is the aggregate that always reports. Branch protection can require
// one context and still cover every proof, but only while every proof is wired
// into it, which is what the tests below hold.
const gateJob = "gate"

type ciWorkflow struct {
	Jobs map[string]ciJob `yaml:"jobs"`
}

type ciJob struct {
	Name  string        `yaml:"name"`
	Needs jobReferences `yaml:"needs"`
	Steps []ciStep      `yaml:"steps"`
}

type ciStep struct {
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
	Run  string         `yaml:"run"`
}

// jobReferences reads the two shapes GitHub accepts for the same field:
// `needs: changes` and `needs: [changes, verify]`.
type jobReferences []string

func (j *jobReferences) UnmarshalYAML(node *yaml.Node) error {
	var single string
	if err := node.Decode(&single); err == nil {
		*j = jobReferences{single}
		return nil
	}
	var many []string
	if err := node.Decode(&many); err != nil {
		return fmt.Errorf("needs must be a job name or a list of them: %w", err)
	}
	*j = many
	return nil
}

func readCIWorkflow(t *testing.T) ciWorkflow {
	t.Helper()
	raw, err := os.ReadFile(ciWorkflowPath)
	if err != nil {
		t.Fatal(err)
	}
	var workflow ciWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	if len(workflow.Jobs) == 0 {
		t.Fatal("ci.yml declares no jobs")
	}
	return workflow
}

// The toolchain CI verifies with is a security floor: seven reachable
// standard-library advisories resolve against go1.26.5 and none against
// go1.26.6, so `make vulncheck` answers differently on either side of it. It
// was written in the workflow and nowhere else, which made the documented
// verification command give one answer in CI and another on a workstation
// running the older toolchain. It lives in go.mod now and this holds the
// workflow to reading it, because two copies of a floor are a floor that
// drifts.
//
// The scope is ci.yml deliberately. representative-benchmark.yml installs the
// toolchain baked into the worker images, which is a different pin that moves
// with an image rebuild; TestBenchmarkWorkflowRequestsBakedToolchains binds
// that one and the two must not be made to follow each other.
func TestVerificationToolchainIsDeclaredOnlyInGoMod(t *testing.T) {
	t.Parallel()
	workflow := readCIWorkflow(t)
	installs := 0
	for _, name := range sortedJobNames(workflow) {
		for _, step := range workflow.Jobs[name].Steps {
			if !strings.HasPrefix(step.Uses, "actions/setup-go@") {
				continue
			}
			installs++
			if _, literal := step.With["go-version"]; literal {
				t.Fatalf("job %q names its own Go version; ci.yml must install from go.mod so this job and a workstation cannot disagree", name)
			}
			if file, _ := step.With["go-version-file"].(string); file != "go.mod" {
				t.Fatalf("job %q installs Go from %q, want go.mod", name, file)
			}
		}
	}
	if installs == 0 {
		t.Fatal("no job in ci.yml installs Go, so the verification toolchain is declared nowhere")
	}
}

// setup-go falls back to the `go` directive when go.mod carries no `toolchain`
// directive. That line is the language version the module compiles against and
// is far below the advisory floor, so deleting the toolchain directive would
// not be caught by the test above: the workflow would still read go.mod and
// would quietly install an older toolchain than the one this repository
// verified against.
func TestGoModDeclaresTheToolchainTheWorkflowReads(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(repositoryRoot + "/go.mod")
	if err != nil {
		t.Fatal(err)
	}
	declared := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if version, found := strings.CutPrefix(strings.TrimSpace(line), "toolchain "); found {
			declared = strings.TrimSpace(version)
		}
	}
	if declared == "" {
		t.Fatal("go.mod declares no toolchain directive, so ci.yml would install the language version from the go directive instead")
	}
	if !strings.HasPrefix(declared, "go1.") {
		t.Fatalf("go.mod toolchain is %q, want an exact go1.MINOR.PATCH release", declared)
	}
}

func sortedJobNames(workflow ciWorkflow) []string {
	names := make([]string, 0, len(workflow.Jobs))
	for name := range workflow.Jobs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// actionlint refuses a label it does not know, so its list has to carry every
// class the fleet publishes. It did not: nddev-linux-fast was published,
// registered and serving while a workflow naming it failed lint, which is a
// class nobody could use for a reason unrelated to the fleet.
func TestActionlintKnowsEveryPublishedClass(t *testing.T) {
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
	known := make(map[string]struct{}, len(config.SelfHostedRunner.Labels))
	for _, label := range config.SelfHostedRunner.Labels {
		known[label] = struct{}{}
	}
	for _, class := range garmbootstrap.PublishedScaleSets() {
		if _, ok := known[class.Name]; !ok {
			t.Errorf("the fleet publishes class %q and .github/actionlint.yaml does not know it, "+
				"so no workflow here can name it", class.Name)
		}
	}
}
