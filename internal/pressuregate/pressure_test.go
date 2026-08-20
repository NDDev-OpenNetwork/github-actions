package pressuregate

import (
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{Required: true, StaleAfterSeconds: 90, HeartbeatSeconds: 30, MinimumClosedSeconds: 20,
		CPUSomeClose: 20, CPUSomeReopen: 10, MemoryFullClose: 1, MemoryFullReopen: 0.1,
		IOFullClose: 5, IOFullReopen: 2, MaximumRecentOOMKills: 0}
}

func TestEvaluateClosesAndReopensWithHysteresis(t *testing.T) {
	now := time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC)
	healthy := Sample{ObservedAt: now, CPUSomeAvg10: 1}
	state, err := Evaluate(State{}, healthy, testPolicy(), now)
	if err != nil || state.State != StateClosed || state.Reason != "minimum-closed-period" {
		t.Fatalf("initial state = %#v err=%v", state, err)
	}
	state, err = Evaluate(state, Sample{ObservedAt: now.Add(time.Second), CPUSomeAvg10: 21}, testPolicy(), now.Add(time.Second))
	if err != nil || state.State != StateClosed || state.Reason != "cpu-pressure" {
		t.Fatalf("closed state = %#v err=%v", state, err)
	}
	state, err = Evaluate(state, Sample{ObservedAt: now.Add(25 * time.Second), CPUSomeAvg10: 15}, testPolicy(), now.Add(25*time.Second))
	if err != nil || state.State != StateClosed || state.Reason != "reopen-hysteresis" {
		t.Fatalf("hysteresis state = %#v err=%v", state, err)
	}
	state, err = Evaluate(state, Sample{ObservedAt: now.Add(26 * time.Second), CPUSomeAvg10: 5}, testPolicy(), now.Add(26*time.Second))
	if err != nil || state.State != StateOpen || state.Reason != "healthy" {
		t.Fatalf("reopened state = %#v err=%v", state, err)
	}
}

func TestParseMetadataFailsClosedOnStaleOrMalformedState(t *testing.T) {
	now := time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC)
	sample := Sample{ObservedAt: now, CPUSomeAvg10: 1}
	state := State{SchemaVersion: SchemaVersion, State: StateOpen, Reason: "healthy", StateSince: now, ObservedAt: now}
	metadata := Metadata(state, sample)
	if _, _, err := ParseMetadata(metadata, testPolicy(), now.Add(91*time.Second)); err == nil {
		t.Fatal("stale member pressure metadata was accepted")
	}
	metadata = Metadata(state, sample)
	metadata[MetadataState] = "unknown"
	if _, _, err := ParseMetadata(metadata, testPolicy(), now); err == nil {
		t.Fatal("unknown member pressure state was accepted")
	}
}
