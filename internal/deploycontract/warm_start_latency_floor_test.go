package deploycontract

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"
	"time"
)

// TestWarmStartLatencyFloorIsDerivedNotAsserted recomputes the warm-start
// decomposition from its primary sources instead of trusting the recorded
// summary. It exists so the below-five-second promotion gate can never be
// declared passed, relaxed or quietly dropped without new measurements: the
// floor is end-to-end latency minus every segment this repository can change,
// and it must stay above the gate for as long as warm VMs are unregistered.
func TestWarmStartLatencyFloorIsDerivedNotAsserted(t *testing.T) {
	var audit struct {
		Series struct {
			Target int `json:"target_p95_milliseconds_exclusive"`
		} `json:"series"`
		Samples []struct {
			LatencyMS      int `json:"latency_milliseconds"`
			PhaseDurations struct {
				AssignmentToProviderStart    int `json:"assignment_to_provider_start_milliseconds"`
				ProviderCreate               int `json:"provider_create_milliseconds"`
				AssignmentToProviderComplete int `json:"assignment_to_provider_complete_milliseconds"`
				GuestSetup                   int `json:"guest_assignment_setup_milliseconds"`
			} `json:"phase_durations"`
			RunnerSessionAt string `json:"runner_session_at"`
			GuestClock      struct {
				RunnerExecNS int64 `json:"runner_exec_unix_ns"`
			} `json:"guest_phase_clock"`
		} `json:"samples"`
	}
	readJSON(t, "../../config/direct-jit-nddev21-latency-audit.json", &audit)

	var hosted struct {
		Runs []struct {
			Sample struct {
				Environment string `json:"environment"`
			} `json:"sample"`
			Jobs []struct {
				QueueToStartMS *int `json:"queue_to_start_ms"`
			} `json:"jobs"`
		} `json:"runs"`
	}
	readJSON(t, "../../benchmark/evidence/phase0-pilots-2026-08-09.json", &hosted)

	var recorded struct {
		Gate struct {
			Target int `json:"target_milliseconds_exclusive"`
		} `json:"gate"`
		EndToEnd latencySegment            `json:"end_to_end"`
		Segments map[string]latencySegment `json:"segments"`
		Hosted   struct {
			Samples int `json:"samples"`
			Median  int `json:"median_milliseconds"`
			P95     int `json:"p95_milliseconds"`
		} `json:"hosted_reference"`
		Floor struct {
			Controllable int  `json:"controllable_median_milliseconds"`
			Floor        int  `json:"floor_median_milliseconds"`
			Reachable    bool `json:"reachable"`
		} `json:"floor"`
		Verdict struct {
			Passed    bool `json:"gate_passed"`
			Reachable bool `json:"gate_reachable_without_registering_warm_vms"`
		} `json:"verdict"`
	}
	readJSON(t, "../../config/warm-start-latency-decomposition.json", &recorded)

	if len(audit.Samples) != 20 {
		t.Fatalf("decomposition needs the full 20-sample series, got %d", len(audit.Samples))
	}
	endToEnd := make([]int, 0, len(audit.Samples))
	assignmentToStart := make([]int, 0, len(audit.Samples))
	providerCreate := make([]int, 0, len(audit.Samples))
	assignmentToComplete := make([]int, 0, len(audit.Samples))
	guestSetup := make([]int, 0, len(audit.Samples))
	runnerConnect := make([]int, 0, len(audit.Samples))
	for index, sample := range audit.Samples {
		session, err := time.Parse("2006-01-02 15:04:05Z", sample.RunnerSessionAt)
		if err != nil {
			t.Fatalf("sample %d runner session timestamp: %v", index+1, err)
		}
		// Both endpoints are guest-clock, so this subtraction stays inside one
		// clock domain exactly as ADR 0029 requires.
		connect := int(session.UnixMilli() - sample.GuestClock.RunnerExecNS/int64(time.Millisecond))
		if connect <= 0 {
			t.Fatalf("sample %d has a non-positive runner connect interval: %d", index+1, connect)
		}
		endToEnd = append(endToEnd, sample.LatencyMS)
		assignmentToStart = append(assignmentToStart, sample.PhaseDurations.AssignmentToProviderStart)
		providerCreate = append(providerCreate, sample.PhaseDurations.ProviderCreate)
		assignmentToComplete = append(assignmentToComplete, sample.PhaseDurations.AssignmentToProviderComplete)
		guestSetup = append(guestSetup, sample.PhaseDurations.GuestSetup)
		runnerConnect = append(runnerConnect, connect)
	}

	hostedQueue := make([]int, 0, 16)
	for _, run := range hosted.Runs {
		if run.Sample.Environment != "github-hosted" {
			continue
		}
		for _, job := range run.Jobs {
			if job.QueueToStartMS != nil {
				hostedQueue = append(hostedQueue, *job.QueueToStartMS)
			}
		}
	}
	if len(hostedQueue) == 0 {
		t.Fatal("hosted reference has no queue-to-start observations")
	}

	for name, observed := range map[string][]int{
		"end_to_end":                      endToEnd,
		"assignment_to_provider_start":    assignmentToStart,
		"provider_create":                 providerCreate,
		"assignment_to_provider_complete": assignmentToComplete,
		"guest_assignment_setup":          guestSetup,
		"runner_exec_to_broker_session":   runnerConnect,
	} {
		want := recorded.Segments[name]
		if name == "end_to_end" {
			want = recorded.EndToEnd
		}
		got := summarize(observed)
		got.Clock, got.Note = want.Clock, want.Note
		if got != want {
			t.Errorf("segment %q recomputes to %+v, recorded %+v", name, got, want)
		}
		// Every segment must name the clock it was measured on, because mixing
		// them is exactly the defect ADR 0029 was written to prevent.
		if want.Clock != "github" && want.Clock != "host" && want.Clock != "guest" {
			t.Errorf("segment %q does not name a valid clock domain: %q", name, want.Clock)
		}
		if want.Note == "" {
			t.Errorf("segment %q has no explanation of what it measures", name)
		}
	}
	if recorded.Hosted.Samples != len(hostedQueue) ||
		recorded.Hosted.Median != nearestRank(hostedQueue, 50) ||
		recorded.Hosted.P95 != nearestRank(hostedQueue, 95) {
		t.Errorf("hosted reference recomputes to n=%d median=%d p95=%d, recorded %+v",
			len(hostedQueue), nearestRank(hostedQueue, 50), nearestRank(hostedQueue, 95), recorded.Hosted)
	}

	// The floor deliberately subtracts only the segments this repository owns.
	// It does not use the runner-connect measurement, so the verdict does not
	// depend on the one-second resolution of the runner's own log timestamps.
	controllable := nearestRank(assignmentToComplete, 50) + nearestRank(guestSetup, 50)
	floor := nearestRank(endToEnd, 50) - controllable
	if recorded.Floor.Controllable != controllable || recorded.Floor.Floor != floor {
		t.Fatalf("floor recomputes to controllable=%d floor=%d, recorded controllable=%d floor=%d",
			controllable, floor, recorded.Floor.Controllable, recorded.Floor.Floor)
	}
	if recorded.Gate.Target != audit.Series.Target {
		t.Fatalf("decomposition gate %d does not match the series target %d", recorded.Gate.Target, audit.Series.Target)
	}
	if floor < audit.Series.Target {
		t.Fatalf("floor %d is now below the %d gate; re-measure and re-decide ADR 0031 instead of editing this test",
			floor, audit.Series.Target)
	}
	if recorded.Floor.Reachable || recorded.Verdict.Passed || recorded.Verdict.Reachable {
		t.Fatalf("the gate must stay recorded as failed and unreachable: floor=%+v verdict=%+v",
			recorded.Floor, recorded.Verdict)
	}
}

type latencySegment struct {
	Median  int    `json:"median_milliseconds"`
	P95     int    `json:"p95_milliseconds"`
	Minimum int    `json:"minimum_milliseconds"`
	Maximum int    `json:"maximum_milliseconds"`
	Samples int    `json:"samples"`
	Clock   string `json:"clock"`
	Note    string `json:"note"`
}

func summarize(values []int) latencySegment {
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	return latencySegment{
		Median:  nearestRank(values, 50),
		P95:     nearestRank(values, 95),
		Minimum: sorted[0],
		Maximum: sorted[len(sorted)-1],
		Samples: len(values),
	}
}

func nearestRank(values []int, percentile int) int {
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	rank := max(int(math.Ceil(float64(len(sorted))*float64(percentile)/100)), 1)
	return sorted[rank-1]
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(fmt.Errorf("decode %s: %w", path, err))
	}
}
