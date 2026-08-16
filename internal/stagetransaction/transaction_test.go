package stagetransaction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func plan(t *testing.T, scripts ...string) Plan {
	t.Helper()
	root := t.TempDir()
	stages := make([]Stage, 0, len(scripts))
	for index, body := range scripts {
		path := filepath.Join(root, "script-"+string(rune('a'+index)))
		if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		stages = append(stages, Stage{ID: "stage-" + string(rune('a'+index)), Command: path, Dir: root, Env: []string{"PATH=/nonexistent"}})
	}
	evidence := filepath.Join(root, "evidence")
	if err := os.Mkdir(evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	return Plan{EvidenceRoot: evidence, OutputLimit: 1024, Stages: stages,
		StageTimeout: 30 * time.Second, GracePeriod: time.Second}
}

func TestTransactionStopsAfterFirstFailureAndRecordsOrderedReceipt(t *testing.T) {
	p := plan(t, "printf 'first\\n'", "printf 'failure\\n' >&2; exit 23", "printf later > later-ran")
	if err := Run(context.Background(), p); err == nil {
		t.Fatal("failure accepted")
	}
	receipt, err := os.ReadFile(filepath.Join(p.EvidenceRoot, "stages.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(receipt)
	for _, required := range []string{`"stage_id":"stage-a"`, `"event":"started"`, `"event":"finished"`, `"stage_id":"stage-b"`, `"exit_status":23`} {
		if !strings.Contains(text, required) {
			t.Fatalf("receipt missing %s: %s", required, text)
		}
	}
	if strings.Contains(text, "stage-c") {
		t.Fatal("later stage ran after failure")
	}
}

func TestTransactionCapturesPrelaunchOverflowSignalAndCleanup(t *testing.T) {
	t.Run("prelaunch", func(t *testing.T) {
		p := plan(t, "true")
		p.Stages[0].Command += ".missing"
		if err := Run(context.Background(), p); err == nil {
			t.Fatal("missing command accepted")
		}
	})
	t.Run("overflow", func(t *testing.T) {
		p := plan(t, "printf 12345")
		p.OutputLimit = 4
		if err := Run(context.Background(), p); err == nil || !strings.Contains(err.Error(), "overflow") {
			t.Fatalf("overflow=%v", err)
		}
	})
	t.Run("signal", func(t *testing.T) {
		p := plan(t, "kill -TERM $$")
		if err := Run(context.Background(), p); err == nil {
			t.Fatal("signal accepted")
		}
		raw, _ := os.ReadFile(filepath.Join(p.EvidenceRoot, "stages.jsonl"))
		if !strings.Contains(string(raw), `"signal":"terminated"`) {
			t.Fatalf("signal receipt=%s", raw)
		}
	})
	t.Run("cleanup", func(t *testing.T) {
		p := plan(t, "exit 7")
		p.Cleanup = func() error { return errors.New("cleanup-root") }
		err := Run(context.Background(), p)
		if err == nil || !strings.Contains(err.Error(), "cleanup-root") || !strings.Contains(err.Error(), "exit status 7") {
			t.Fatalf("joined error=%v", err)
		}
	})
}

func TestNegativeStageSeparatesRawOutcomeFromVerifierVerdict(t *testing.T) {
	t.Run("expected rejection", func(t *testing.T) {
		p := plan(t, "printf 'typed rejection\\n' >&2; exit 23")
		p.Stages[0].Expect = &Expectation{ExitStatus: 23, Stderr: "typed rejection\n", Absent: []string{filepath.Join(filepath.Dir(p.EvidenceRoot), "side-effect")}}
		if err := Run(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(filepath.Join(p.EvidenceRoot, "stages.jsonl"))
		if !strings.Contains(string(raw), `"raw_error":"exit status 23"`) || !strings.Contains(string(raw), `"verifier_ok":true`) {
			t.Fatalf("receipt=%s", raw)
		}
	})
	for name, body := range map[string]string{
		"wrong rejection":    "printf 'other\\n' >&2; exit 23",
		"zero exit":          "printf 'typed rejection\\n' >&2; exit 0",
		"missing diagnostic": "exit 23",
		"extra diagnostic":   "printf 'typed rejection\\nextra\\n' >&2; exit 23",
	} {
		t.Run(name, func(t *testing.T) {
			p := plan(t, body, "printf later")
			p.Stages[0].Expect = &Expectation{ExitStatus: 23, Stderr: "typed rejection\n"}
			if err := Run(context.Background(), p); err == nil {
				t.Fatal("verifier mismatch accepted")
			}
			raw, _ := os.ReadFile(filepath.Join(p.EvidenceRoot, "stages.jsonl"))
			if strings.Contains(string(raw), "stage-b") || !strings.Contains(string(raw), `"verifier_ok":false`) {
				t.Fatalf("receipt=%s", raw)
			}
		})
	}
}
