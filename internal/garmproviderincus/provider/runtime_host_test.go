package provider

import (
	"errors"
	"testing"
)

func TestRuntimeHostIdentityRequiresExactEquality(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		declared string
		observed string
		want     bool
	}{
		{name: "exact", declared: "gha-services", observed: "gha-services", want: true},
		{name: "legacy alias", declared: "server-gha-services", observed: "gha-services"},
		{name: "other member", declared: "gha-runner-1", observed: "gha-services"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := platformHostMatches(testCase.declared, testCase.observed); got != testCase.want {
				t.Fatalf("platformHostMatches(%q, %q) = %v, want %v", testCase.declared, testCase.observed, got, testCase.want)
			}
		})
	}
}

func TestRuntimeHostnameFailureIsNotAccepted(t *testing.T) {
	original := runtimeHostname
	runtimeHostname = func() (string, error) { return "", errors.New("unavailable") }
	t.Cleanup(func() { runtimeHostname = original })
	if _, err := runtimeHostname(); err == nil {
		t.Fatal("hostname failure was hidden")
	}
}
