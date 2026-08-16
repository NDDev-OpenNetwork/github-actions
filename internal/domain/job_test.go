package domain

import "testing"

func TestJobKeyIsDeterministicAndAttemptScoped(t *testing.T) {
	t.Parallel()

	identity := WebhookJobIdentity{RepositoryID: 42, WorkflowJobID: 9001, RunAttempt: 1}
	first, err := identity.Key()
	if err != nil {
		t.Fatalf("first key: %v", err)
	}
	second, err := identity.Key()
	if err != nil {
		t.Fatalf("second key: %v", err)
	}
	if first != second {
		t.Fatalf("keys differ: %q != %q", first, second)
	}

	retry := identity
	retry.RunAttempt = 2
	retryKey, err := retry.Key()
	if err != nil {
		t.Fatalf("retry key: %v", err)
	}
	if first == retryKey {
		t.Fatal("a new run attempt reused the prior idempotency key")
	}
}

func TestJobKeyRejectsIncompleteIdentity(t *testing.T) {
	t.Parallel()

	if _, err := (WebhookJobIdentity{}).Key(); err == nil {
		t.Fatal("expected an invalid identity error")
	}
}

func TestScaleSetJobKeyUsesRunnerRequest(t *testing.T) {
	t.Parallel()

	first, err := (ScaleSetJobIdentity{ScaleSetID: 17, JobID: "5c3077ba-3664-5824-b2cf-e22a31b25f43"}).Key()
	if err != nil {
		t.Fatalf("first key: %v", err)
	}
	repeated, err := (ScaleSetJobIdentity{ScaleSetID: 17, JobID: "5c3077ba-3664-5824-b2cf-e22a31b25f43"}).Key()
	if err != nil {
		t.Fatalf("repeated key: %v", err)
	}
	next, err := (ScaleSetJobIdentity{ScaleSetID: 17, JobID: "98c98544-f5b8-4cd0-9ee4-319edb711382"}).Key()
	if err != nil {
		t.Fatalf("next key: %v", err)
	}
	if first != repeated || first == next {
		t.Fatalf("unexpected scale-set keys: first=%q repeated=%q next=%q", first, repeated, next)
	}
}
