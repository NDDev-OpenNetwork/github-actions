package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestGitHubTokenRejectsAmbiguousOrMissingIdentity(t *testing.T) {
	getenv := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	if token, err := githubToken(getenv(map[string]string{"GH_TOKEN": "one"})); err != nil || token != "one" {
		t.Fatalf("GH_TOKEN result = %q, %v", token, err)
	}
	if token, err := githubToken(getenv(map[string]string{"GITHUB_TOKEN": "two"})); err != nil || token != "two" {
		t.Fatalf("GITHUB_TOKEN result = %q, %v", token, err)
	}
	for _, values := range []map[string]string{
		{},
		{"GH_TOKEN": "one", "GITHUB_TOKEN": "two"},
		{"GH_TOKEN": " one "},
	} {
		if _, err := githubToken(getenv(values)); err == nil {
			t.Fatalf("accepted ambiguous token environment: %#v", values)
		}
	}
}

func TestRunHasBoundedCommandSurface(t *testing.T) {
	originalVersion, originalCommit := version, commit
	version, commit = "v1.2.3", strings.Repeat("a", 40)
	t.Cleanup(func() { version, commit = originalVersion, originalCommit })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"version": "v1.2.3"`) || stderr.Len() != 0 {
		t.Fatalf("version code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"collect", "--run-id", "0"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "requires --run-id") {
		t.Fatalf("invalid collect code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"unknown"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("unknown code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
