package stagetransaction

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lifecyclePlan mirrors the helper the other tests use, with a usable PATH:
// these stages fork and sleep, which the PATH=/nonexistent the other helper sets
// would make impossible for reasons unrelated to what is being tested.
func lifecyclePlan(t *testing.T, stageTimeout, grace time.Duration, body string) Plan {
	t.Helper()
	root := t.TempDir()
	script := filepath.Join(root, "stage")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(root, "evidence")
	if err := os.Mkdir(evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	return Plan{
		EvidenceRoot: evidence,
		OutputLimit:  4096,
		StageTimeout: stageTimeout,
		GracePeriod:  grace,
		Stages: []Stage{{
			ID:      "stage",
			Command: script,
			Dir:     root,
			Env:     []string{"PATH=/usr/bin:/bin"},
			Expect:  &Expectation{ExitStatus: 0},
		}},
	}
}

func evidenceRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "evidence")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

// A stage transaction publishes a receipt saying what happened. A stage with no
// deadline can leave it with no receipt at all, which is the one outcome the
// design has no way to describe -- so the budget is required rather than
// defaulted, because a default deadline is a deadline nobody chose.
func TestAPlanWithoutBudgetsIsRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	script := filepath.Join(root, "noop")
	if err := os.WriteFile(script, []byte("#!/bin/bash\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	stage := Stage{ID: "noop", Command: script, Dir: root, Expect: &Expectation{ExitStatus: 0}}
	for _, testCase := range []struct {
		name    string
		plan    Plan
		message string
	}{
		{"no stage timeout",
			Plan{EvidenceRoot: evidenceRoot(t), OutputLimit: 1024, Stages: []Stage{stage}, GracePeriod: time.Second},
			"states no stage timeout"},
		{"no grace period",
			Plan{EvidenceRoot: evidenceRoot(t), OutputLimit: 1024, Stages: []Stage{stage}, StageTimeout: time.Second},
			"states no grace period"},
	} {
		err := Run(context.Background(), testCase.plan)
		if err == nil {
			t.Errorf("%s: accepted", testCase.name)
			continue
		}
		if !strings.Contains(err.Error(), testCase.message) {
			t.Errorf("%s: error %q does not mention %q", testCase.name, err, testCase.message)
		}
	}
}

// The defect this replaces: a stage that ignores its deadline ran forever,
// because Run was handed a context that could not expire and the stage had no
// timeout of its own.
func TestAStageThatIgnoresItsDeadlineIsStillBounded(t *testing.T) {
	t.Parallel()
	// Traps TERM and keeps going, which is what a stage doing cleanup looks
	// like and what a stage that will not die looks like.
	plan := lifecyclePlan(t, 500*time.Millisecond, 500*time.Millisecond, "trap '' TERM; sleep 30")
	started := time.Now()
	err := Run(context.Background(), plan)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("a stage that ignores TERM and sleeps 30s reported success")
	}
	// Timeout plus grace plus slack. Without the bound this returns after 30s.
	if elapsed > 10*time.Second {
		t.Fatalf("the stage was not bounded: Run took %s", elapsed)
	}
}

// The defect this replaces: the cancellation reached the direct child only, so a
// grandchild kept running and kept stdout open, and Wait blocked on it long
// after the stage itself had exited.
func TestAForkedGrandchildDoesNotSuspendTheTransaction(t *testing.T) {
	t.Parallel()
	// The stage exits immediately; the grandchild inherits stdout and holds it
	// for thirty seconds. Waiting on output rather than on the process is what
	// used to hang here.
	plan := lifecyclePlan(t, 500*time.Millisecond, 500*time.Millisecond, "sleep 30 & exit 0")
	started := time.Now()
	_ = Run(context.Background(), plan)
	elapsed := time.Since(started)
	if elapsed > 10*time.Second {
		t.Fatalf("a grandchild holding stdout suspended the transaction: Run took %s", elapsed)
	}
}

// And the counter-check: an ordinary stage still runs to completion and is not
// cut short by the machinery above.
func TestAnOrdinaryStageStillCompletes(t *testing.T) {
	t.Parallel()
	plan := lifecyclePlan(t, 10*time.Second, time.Second, "printf 'done\n'")
	plan.Stages[0].Expect = &Expectation{ExitStatus: 0, Stdout: "done\n"}
	if err := Run(context.Background(), plan); err != nil {
		t.Fatalf("an ordinary stage failed: %v", err)
	}
	receipts, err := os.ReadFile(filepath.Join(plan.EvidenceRoot, "stages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(receipts), `"event":"finished"`) {
		t.Fatalf("no finished receipt was published:\n%s", receipts)
	}
}
