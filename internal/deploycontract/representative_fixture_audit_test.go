package deploycontract

import (
	"os"
	"strings"
	"testing"
)

// TestRepresentativeFixtureAuditIsArithmeticallySound re-derives the fixture
// diagnosis from the recorded per-step durations and from the fixture itself,
// so the conclusion cannot drift from the numbers that produced it. It also
// refuses a recorded verdict that claims the 3x gate is reachable as things
// stand, or that the gate was relaxed to reach it.
func TestRepresentativeFixtureAuditIsArithmeticallySound(t *testing.T) {
	var audit struct {
		SchemaVersion int `json:"schema_version"`
		Fixture       struct {
			Path           string `json:"path"`
			SourceLines    int    `json:"source_lines"`
			LockedPackages int    `json:"locked_packages"`
		} `json:"fixture"`
		Cold      fixtureRun `json:"cold"`
		Warm      fixtureRun `json:"warm"`
		WarmCache struct {
			Hits        int `json:"sccache_hits"`
			Misses      int `json:"sccache_misses"`
			Errors      int `json:"sccache_errors"`
			HitRateFull int `json:"hit_rate_percent"`
		} `json:"warm_cache"`
		Analysis struct {
			Toolchain  coldWarmSeconds `json:"toolchain_install_seconds"`
			Dependency coldWarmSeconds `json:"dependency_resolution_seconds"`
			Compilees  coldWarmSeconds `json:"compile_and_test_seconds"`
			Projected  struct {
				Cold int `json:"cold_seconds"`
				Warm int `json:"warm_seconds"`
			} `json:"projected_after_baked_toolchains"`
		} `json:"analysis"`
		Verdict struct {
			Reachable bool   `json:"three_times_median_speedup_reachable_with_this_fixture"`
			Relaxed   bool   `json:"gate_relaxed"`
			Required  string `json:"required_change"`
		} `json:"verdict"`
	}
	readJSON(t, "../../config/representative-workload-fixture-audit.json", &audit)

	if audit.SchemaVersion != 1 || audit.Cold.WorkflowRunID == 0 || audit.Warm.WorkflowRunID == 0 ||
		audit.Cold.WorkflowRunID == audit.Warm.WorkflowRunID ||
		audit.Cold.RunnerName == audit.Warm.RunnerName ||
		!strings.HasPrefix(audit.Cold.RunnerName, "nddev-") ||
		!strings.HasPrefix(audit.Warm.RunnerName, "nddev-") {
		t.Fatalf("cold and warm must be two distinct nddev jobs: cold=%+v warm=%+v", audit.Cold, audit.Warm)
	}
	if audit.WarmCache.Hits <= 0 || audit.WarmCache.Misses != 0 || audit.WarmCache.Errors != 0 ||
		audit.WarmCache.HitRateFull != 100 {
		t.Fatalf("the diagnosis only holds for a full-hit warm run: %+v", audit.WarmCache)
	}

	// The fixture must still be the small one the diagnosis describes. Once it
	// is enlarged this test should fail and the audit be re-taken, which is the
	// intended signal that the measurement is stale.
	lock, err := os.ReadFile("../../benchmark/rust/Cargo.lock")
	if err != nil {
		t.Fatal(err)
	}
	if packages := strings.Count(string(lock), "[[package]]"); packages != audit.Fixture.LockedPackages {
		t.Fatalf("fixture now locks %d packages, audit recorded %d; re-measure before trusting the verdict",
			packages, audit.Fixture.LockedPackages)
	}

	for name, want := range map[string]coldWarmSeconds{
		"Install toolchain":    audit.Analysis.Toolchain,
		"Resolve dependencies": audit.Analysis.Dependency,
	} {
		if got := (coldWarmSeconds{audit.Cold.step(t, name), audit.Warm.step(t, name)}); got != want {
			t.Errorf("%q recomputes to %+v, recorded %+v", name, got, want)
		}
	}
	compile := coldWarmSeconds{
		Cold: audit.Cold.step(t, "Build workload") + audit.Cold.step(t, "Test workload"),
		Warm: audit.Warm.step(t, "Build workload") + audit.Warm.step(t, "Test workload"),
	}
	if compile != audit.Analysis.Compilees {
		t.Errorf("cacheable phase recomputes to %+v, recorded %+v", compile, audit.Analysis.Compilees)
	}
	projectedCold := audit.Analysis.Dependency.Cold + compile.Cold
	projectedWarm := audit.Analysis.Dependency.Warm + compile.Warm
	if projectedCold != audit.Analysis.Projected.Cold || projectedWarm != audit.Analysis.Projected.Warm {
		t.Fatalf("projection recomputes to cold=%d warm=%d, recorded cold=%d warm=%d",
			projectedCold, projectedWarm, audit.Analysis.Projected.Cold, audit.Analysis.Projected.Warm)
	}
	// Three times means the warm job must finish in a third of the cold one.
	if projectedWarm*3 <= projectedCold {
		t.Fatalf("the projection now reaches the 3x gate (cold=%d warm=%d); re-decide ADR 0032 rather than editing this test",
			projectedCold, projectedWarm)
	}
	if audit.Verdict.Reachable || audit.Verdict.Relaxed || audit.Verdict.Required == "" {
		t.Fatalf("the verdict must stay unreachable, unrelaxed and actionable: %+v", audit.Verdict)
	}
}

type fixtureRun struct {
	WorkflowRunID int64  `json:"workflow_run_id"`
	JobID         int64  `json:"job_id"`
	RunnerName    string `json:"runner_name"`
	Steps         []struct {
		Name    string `json:"name"`
		Seconds int    `json:"seconds"`
	} `json:"steps"`
}

func (r fixtureRun) step(t *testing.T, name string) int {
	t.Helper()
	for _, step := range r.Steps {
		if step.Name == name {
			return step.Seconds
		}
	}
	t.Fatalf("job %d has no %q step", r.JobID, name)
	return 0
}

type coldWarmSeconds struct {
	Cold int `json:"cold"`
	Warm int `json:"warm"`
}
