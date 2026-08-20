package telemetryattrs

import (
	"regexp"
	"testing"
)

func TestAttributeRegistryIsBoundedAndNamespaced(t *testing.T) {
	pattern := regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	for _, key := range []string{
		RunnerName, RunnerPool, RunnerWarmClaimed, AdmissionReason,
		AdmissionPreemptedWorkers, OperationOutcome,
	} {
		if !pattern.MatchString(key) || len(key) > 63 {
			t.Fatalf("invalid telemetry attribute key %q", key)
		}
	}
	if ServiceNamespace != "nddev-drakkars" || OutcomeSuccess != "success" || OutcomeError != "error" {
		t.Fatal("stable telemetry vocabulary changed")
	}
}
