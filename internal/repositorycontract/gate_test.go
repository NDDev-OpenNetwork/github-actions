package repositorycontract

import (
	"strings"
	"testing"
)

// main requires one status context. That is sound only while the context that
// is required sees every other proof, and the gate exists to be that context:
// it runs on always() so a path-filtered job that skips cannot leave it
// pending. A job added outside its needs is a proof that can fail while the
// required context reports success, which is how a red GARM derivative could
// merge. Depending on a job is not enough either -- the gate has to read the
// result, or the dependency only orders it.
func TestEveryProofReportsThroughTheGate(t *testing.T) {
	t.Parallel()
	workflow := readCIWorkflow(t)
	gate, exists := workflow.Jobs[gateJob]
	if !exists {
		t.Fatalf("ci.yml has no %q job, so no single context can cover the others", gateJob)
	}
	consulted := make(map[string]bool, len(gate.Needs))
	for _, need := range gate.Needs {
		if _, known := workflow.Jobs[need]; !known {
			t.Fatalf("the gate needs %q, which ci.yml does not define", need)
		}
		consulted[need] = true
	}
	for _, name := range sortedJobNames(workflow) {
		if name == gateJob {
			continue
		}
		if !consulted[name] {
			t.Fatalf("job %q reports to nothing the gate reads; it can fail while the required context succeeds", name)
		}
	}
	var script strings.Builder
	for _, step := range gate.Steps {
		script.WriteString(step.Run)
	}
	for _, need := range gate.Needs {
		if !strings.Contains(script.String(), "needs."+need+".result") {
			t.Fatalf("the gate depends on %q without reading its result, so the dependency only orders the job", need)
		}
	}
}
