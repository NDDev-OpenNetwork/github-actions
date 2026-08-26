package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanIsReadOnlyAndClassifiesVanishedCIJob(t *testing.T) {
	directory := t.TempDir()
	state, lock, token := filepath.Join(directory, "state.json"), filepath.Join(directory, "state.lock"), filepath.Join(directory, "token")
	if err := os.WriteFile(token, []byte("secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.json")
	jobPath := filepath.Join(directory, "job.json")
	configuration := `{"state_file":"` + state + `","lock_file":"` + lock + `","token_file":"` + token + `","github_endpoint":"https://api.github.com","policy":{"schema_version":1,"missing_runner_grace_seconds":120,"scale_sets":{"example-ci":"force-cancel-full-rerun"}}}`
	job := `{"repository":"example-org/example-repo","scale_set":"example-ci","run_id":42,"job_id":84,"runner_id":21,"runner_name":"example-runner","job_status":"in_progress","started_at":"2026-08-26T13:00:00Z","runner_present":false,"run_status":"in_progress","run_attempt":1}`
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobPath, []byte(job), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--config", configPath, "--job", jobPath, "plan"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"action":"force-cancel"`)) {
		t.Fatalf("plan=%s", stdout.String())
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("plan mutated recovery state")
	}
}

func TestResolveTokenCommandReceivesOnlyRepositoryIdentity(t *testing.T) {
	directory := t.TempDir()
	command := filepath.Join(directory, "token")
	script := "#!/bin/sh\n[ \"$GHA_VANISHED_REPOSITORY\" = 'example-org/example-repo' ]\nprintf '%s\\n' 'installation-token'\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	token, err := resolveToken(context.Background(), config{TokenCommand: []string{command}}, "example-org/example-repo")
	if err != nil {
		t.Fatal(err)
	}
	if token != "installation-token" {
		t.Fatalf("token=%q", token)
	}
}
