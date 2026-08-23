package telemetryattrs

import (
	"regexp"
	"testing"
)

func TestAttributeRegistryIsBoundedAndNamespaced(t *testing.T) {
	pattern := regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	for _, key := range []string{
		RunnerName, InstanceName, RunnerPool, RunnerWarmClaimed, AdmissionReason,
		AdmissionPreemptedWorkers, OperationOutcome, GitHubRepository,
		GitHubRepositoryID, GitHubWorkflowRunID, GitHubRunAttempt, GitHubJobName,
		GitHubWorkflowRef, GitHubCommitSHA,
	} {
		if !pattern.MatchString(key) || len(key) > 63 {
			t.Fatalf("invalid telemetry attribute key %q", key)
		}
	}
	if ServiceNamespace != "nddev-drakkars" || OutcomeSuccess != "success" || OutcomeError != "error" {
		t.Fatal("stable telemetry vocabulary changed")
	}
}
