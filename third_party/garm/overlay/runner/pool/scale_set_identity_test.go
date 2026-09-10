package pool

import (
	"testing"

	"github.com/google/go-github/v84/github"
)

func TestParseCheckRunURLRequiresHTTPSOriginAndRepoPath(t *testing.T) {
	t.Parallel()
	got, ok := parseCheckRunURL("https://api.github.com/repos/test-owner/test-repo/check-runs/12")
	if !ok || got.host != "api.github.com" || got.owner != "test-owner" || got.repo != "test-repo" || got.id != 12 {
		t.Fatalf("got %#v ok=%t", got, ok)
	}
	got, ok = parseCheckRunURL("https://ghe.example/api/v3/repos/test-owner/test-repo/check-runs/12")
	if !ok || got.host != "ghe.example" || got.id != 12 {
		t.Fatalf("GHES path %#v ok=%t", got, ok)
	}
	got, ok = parseCheckRunURL("https://api.github.com/repos/test-owner/test-repo/check-runs/12?per_page=100")
	if !ok || got.id != 12 {
		t.Fatalf("query string %#v ok=%t", got, ok)
	}
	for _, raw := range []string{
		"",
		"http://api.github.com/repos/test-owner/test-repo/check-runs/12",
		"https://user:pass@api.github.com/repos/test-owner/test-repo/check-runs/12",
		"https://evil.example/check-runs/12",
		"https://api.github.com/check-runs/12",
		"https://api.github.com/repos/test-owner/test-repo/check-runs/12/extra",
		"https://api.github.com/repos/../test-repo/check-runs/12",
		"https://api.github.com/repos/test-owner/test-repo/check-runs/0",
		"https://api.github.com/repos/test-owner/test-repo/actions/runs/12",
	} {
		if _, ok := parseCheckRunURL(raw); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestWorkflowJobBindsCheckRunRequiresMatchingOrigin(t *testing.T) {
	t.Parallel()
	check := &github.CheckRun{
		ID:  github.Ptr(int64(12)),
		URL: github.Ptr("https://api.github.com/repos/test-owner/test-repo/check-runs/12"),
	}
	job := &github.WorkflowJob{
		CheckRunURL: github.Ptr("https://api.github.com/repos/test-owner/test-repo/check-runs/12"),
	}
	if !workflowJobBindsCheckRun(job, check) {
		t.Fatal("matching origin and path must bind")
	}
	job.CheckRunURL = github.Ptr("https://evil.example/repos/test-owner/test-repo/check-runs/12")
	if workflowJobBindsCheckRun(job, check) {
		t.Fatal("a foreign origin must not bind")
	}
	job.CheckRunURL = github.Ptr("https://api.github.com/repos/other-owner/test-repo/check-runs/12")
	if workflowJobBindsCheckRun(job, check) {
		t.Fatal("a different repository path must not bind")
	}
	empty := &github.CheckRun{ID: github.Ptr(int64(12))}
	job.CheckRunURL = github.Ptr("https://api.github.com/repos/test-owner/test-repo/check-runs/12")
	if workflowJobBindsCheckRun(job, empty) {
		t.Fatal("a missing check URL must not prove origin")
	}
}
