package repositorycontract

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const branchProtectionPath = repositoryRoot + "/.github/branch-protection.yaml"

type branchProtection struct {
	SchemaVersion        int    `yaml:"schema_version"`
	Branch               string `yaml:"branch"`
	RequiredStatusChecks struct {
		Contexts []string `yaml:"contexts"`
		Strict   bool     `yaml:"strict"`
	} `yaml:"required_status_checks"`
	RequiredPullRequestReviews struct {
		RequiredApprovingReviewCount int  `yaml:"required_approving_review_count"`
		DismissStaleReviews          bool `yaml:"dismiss_stale_reviews"`
		RequireCodeOwnerReviews      bool `yaml:"require_code_owner_reviews"`
		RequireLastPushApproval      bool `yaml:"require_last_push_approval"`
	} `yaml:"required_pull_request_reviews"`
	EnforceAdmins                  bool `yaml:"enforce_admins"`
	RequiredConversationResolution bool `yaml:"required_conversation_resolution"`
	RequiredLinearHistory          bool `yaml:"required_linear_history"`
	AllowForcePushes               bool `yaml:"allow_force_pushes"`
	AllowDeletions                 bool `yaml:"allow_deletions"`
	BlockCreations                 bool `yaml:"block_creations"`
	LockBranch                     bool `yaml:"lock_branch"`
	AllowForkSyncing               bool `yaml:"allow_fork_syncing"`
	// Restrictions is push-restriction state. GitHub models "no restriction"
	// as an explicit null rather than an absent field, and the declaration
	// writes it out for the same reason every other field is written out, so
	// the type has to accept it.
	Restrictions *pushRestrictions `yaml:"restrictions"`
}

type pushRestrictions struct {
	Users []string `yaml:"users"`
	Teams []string `yaml:"teams"`
	Apps  []string `yaml:"apps"`
}

func readBranchProtection(t *testing.T) branchProtection {
	t.Helper()
	raw, err := os.ReadFile(branchProtectionPath)
	if err != nil {
		t.Fatal(err)
	}
	var declared branchProtection
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&declared); err != nil {
		t.Fatalf("decode branch protection declaration: %v", err)
	}
	if declared.SchemaVersion != 1 {
		t.Fatalf("branch protection schema_version = %d, want 1", declared.SchemaVersion)
	}
	return declared
}

// GitHub reports a check run under the job's display name, so the required
// context is `name:` and never the job key. A context naming something ci.yml
// does not produce is worse than requiring nothing: the check never reports,
// and every pull request waits on it forever rather than failing.
func TestRequiredContextsAreProducedByTheWorkflow(t *testing.T) {
	t.Parallel()
	declared := readBranchProtection(t)
	workflow := readCIWorkflow(t)

	produced := make(map[string]string, len(workflow.Jobs))
	for _, key := range sortedJobNames(workflow) {
		job := workflow.Jobs[key]
		if job.Name == "" {
			t.Fatalf("job %q declares no name, so the check run it reports has no stable context", key)
		}
		produced[job.Name] = key
	}
	if len(declared.RequiredStatusChecks.Contexts) == 0 {
		t.Fatal("no required status check is declared, so any pull request can merge unproven")
	}
	for _, context := range declared.RequiredStatusChecks.Contexts {
		if _, exists := produced[context]; !exists {
			t.Fatalf("required context %q is produced by no job in ci.yml; protection would wait on it forever", context)
		}
	}
}

// This is the invariant the repository got wrong: `Go verify` was required
// while `GARM derivative` was not, so a pull request that broke the derivative
// proof merged green. Requiring a leaf proof only makes that one proof
// blocking. Requiring the aggregate makes all of them blocking, and keeps
// doing so when a job is added, because a new job has to join the gate to
// exist at all -- TestEveryProofReportsThroughTheGate holds that separately.
func TestTheRequiredContextIsTheAggregateAndNotALeafProof(t *testing.T) {
	t.Parallel()
	declared := readBranchProtection(t)
	workflow := readCIWorkflow(t)

	gate, exists := workflow.Jobs[gateJob]
	if !exists {
		t.Fatalf("ci.yml has no %q job", gateJob)
	}
	for _, context := range declared.RequiredStatusChecks.Contexts {
		if context == gate.Name {
			continue
		}
		for _, key := range sortedJobNames(workflow) {
			if workflow.Jobs[key].Name != context {
				continue
			}
			t.Fatalf("required context %q is job %q, a proof the gate already reads; requiring it leaves every other proof non-blocking, which is how a red %q merged. Require %q instead.",
				context, key, "GARM derivative", gate.Name)
		}
	}
	if !declared.RequiredStatusChecks.Strict {
		t.Fatal("strict is false, so the gate can pass on a branch that was never merged with its base")
	}
}

// The gate is about to be the only required context, which makes its shell the
// thing that decides whether a merge is allowed. `always()` means it runs even
// when a dependency failed, so every result it can observe has to map to an
// explicit verdict. `skipped` is a pass only for the path-filtered derivative
// job; treating it as a pass anywhere else would let a proof opt out of being
// a proof, and `cancelled` is never a pass because a cancelled job proved
// nothing.
func TestGateVerdictCoversEveryResultItCanObserve(t *testing.T) {
	t.Parallel()
	workflow := readCIWorkflow(t)
	gate := workflow.Jobs[gateJob]

	var script strings.Builder
	for _, step := range gate.Steps {
		script.WriteString(step.Run)
	}
	if script.Len() == 0 {
		t.Fatal("the gate runs no script, so it cannot reach a verdict")
	}

	for _, testCase := range []struct {
		name    string
		results map[string]string
		merge   bool
	}{
		{"every proof passed", map[string]string{"changes": "success", "verify": "success", "garm-derivative": "success"}, true},
		{"derivative skipped by path filter", map[string]string{"changes": "success", "verify": "success", "garm-derivative": "skipped"}, true},
		{"derivative failed", map[string]string{"changes": "success", "verify": "success", "garm-derivative": "failure"}, false},
		{"derivative cancelled", map[string]string{"changes": "success", "verify": "success", "garm-derivative": "cancelled"}, false},
		{"verify failed", map[string]string{"changes": "success", "verify": "failure", "garm-derivative": "success"}, false},
		{"verify skipped", map[string]string{"changes": "success", "verify": "skipped", "garm-derivative": "success"}, false},
		{"verify cancelled", map[string]string{"changes": "success", "verify": "cancelled", "garm-derivative": "success"}, false},
		{"path detection failed", map[string]string{"changes": "failure", "verify": "success", "garm-derivative": "success"}, false},
		{"path detection skipped", map[string]string{"changes": "skipped", "verify": "success", "garm-derivative": "skipped"}, false},
	} {
		rendered := script.String()
		for job, result := range testCase.results {
			rendered = strings.ReplaceAll(rendered, "${{ needs."+job+".result }}", result)
		}
		if strings.Contains(rendered, "${{") {
			t.Fatalf("%s: the gate script still holds an unsubstituted expression, so this table does not cover what it reads:\n%s", testCase.name, rendered)
		}
		err := exec.Command("bash", "-c", rendered).Run()
		if merged := err == nil; merged != testCase.merge {
			t.Errorf("%s: gate allows merge = %v, want %v (results %v)", testCase.name, merged, testCase.merge, testCase.results)
		}
	}
}
