package slabheal

import (
	"strings"
	"testing"
	"time"
)

func healable() Facts {
	return Facts{
		SUnreclaimBytes: 3 << 30, ThresholdBytes: 2 << 30,
		SelfState:   "open",
		OtherStates: map[string]string{"gha-runner-2": "open", "gha-runner-3": "open"},
		Cooldown:    12 * time.Hour, Now: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
	}
}

func TestDecideHealsOnlyAQuietOpenLoneMember(t *testing.T) {
	if decision := Decide(healable()); !decision.Heal {
		t.Fatalf("healable member refused: %s", decision.Reason)
	}
	for name, mutate := range map[string]func(*Facts){
		"below threshold":     func(f *Facts) { f.SUnreclaimBytes = 1 << 30 },
		"zero threshold":      func(f *Facts) { f.ThresholdBytes = 0; f.SUnreclaimBytes = 3 << 30 },
		"own gate closed":     func(f *Facts) { f.SelfState = "closed" },
		"peer draining":       func(f *Facts) { f.OtherStates["gha-runner-2"] = "closed" },
		"jobs on the member":  func(f *Facts) { f.NonWarmOccupants = 1 },
		"inside the cooldown": func(f *Facts) { f.LastHealAt = f.Now.Add(-time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			facts := healable()
			facts.OtherStates = map[string]string{"gha-runner-2": "open", "gha-runner-3": "open"}
			mutate(&facts)
			decision := Decide(facts)
			if decision.Heal {
				t.Fatal("guard did not hold")
			}
			if decision.Reason == "" {
				t.Fatal("a refusal must say why")
			}
		})
	}
}

func TestParseSUnreclaim(t *testing.T) {
	meminfo := "MemTotal: 16 kB\nSUnreclaim:     4276224 kB\nSlab: 1 kB\n"
	got, err := ParseSUnreclaim(strings.NewReader(meminfo))
	if err != nil || got != 4276224*1024 {
		t.Fatalf("got %d err %v", got, err)
	}
	if _, err := ParseSUnreclaim(strings.NewReader("MemTotal: 16 kB\n")); err == nil {
		t.Fatal("missing SUnreclaim line was accepted")
	}
}
