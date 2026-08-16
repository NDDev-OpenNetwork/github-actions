package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Every operator command reaches the cache credential loader, which derives the
// expected owner of /etc/garm/cache from the running process. Under sudo that
// loader demands root:root while the deployment ships root:garm, so a command
// run the wrong way fails for a reason unrelated to what it was asked to do.
// warm-drain refused root from the start; probe and warm-pool did not, which is
// what made the compatibility probe fail on both serving hosts.
func TestOperatorCommandsRefuseRootBeforeReadingConfiguration(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{"warm-drain", []string{
			"warm-drain", "--config", "/does/not/exist",
			"--controller-id", "controller", "--instance", "warm-standard-0001",
		}},
		{"warm-pool", []string{
			"warm-pool", "--config", "/does/not/exist", "--controller-id", "controller",
		}},
		{"probe", []string{"probe", "--config", "/does/not/exist"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			originalEffectiveUID := effectiveUID
			t.Cleanup(func() { effectiveUID = originalEffectiveUID })
			effectiveUID = func() int { return 0 }

			var stdout, stderr bytes.Buffer
			if exitCode := run(testCase.args, &stdout, &stderr); exitCode != 1 {
				t.Fatalf("run %s exit code = %d, want 1", testCase.name, exitCode)
			}
			if !strings.Contains(stderr.String(), testCase.name+" refuses effective UID 0") {
				t.Fatalf("stderr = %q, want root-refusal diagnostic for %s", stderr.String(), testCase.name)
			}
			if stdout.Len() != 0 {
				t.Fatalf("%s wrote %q to stdout before refusing root", testCase.name, stdout.String())
			}
		})
	}
}

// The refusal must precede configuration and Incus access, so a non-root run
// against a missing configuration file fails for the configuration rather than
// for the identity. Without this the guard could be satisfied by any error.
func TestOperatorCommandsReachConfigurationWhenNotRoot(t *testing.T) {
	originalEffectiveUID := effectiveUID
	t.Cleanup(func() { effectiveUID = originalEffectiveUID })
	effectiveUID = func() int { return 1000 }

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"probe", "--config", "/does/not/exist"}, &stdout, &stderr); exitCode != 1 {
		t.Fatalf("run probe exit code = %d, want 1", exitCode)
	}
	if strings.Contains(stderr.String(), "refuses effective UID 0") {
		t.Fatalf("stderr = %q, want a configuration diagnostic rather than the root refusal", stderr.String())
	}
}

func TestVersionCommandReportsBuildAndSDKProvenance(t *testing.T) {
	originalVersion, originalCommit := version, commit
	t.Cleanup(func() {
		version, commit = originalVersion, originalCommit
	})
	version, commit = "v0.1.5-nddev.test", "0123456789abcdef"

	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"version"}, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("run version exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	var output map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode version output: %v", err)
	}
	if output["version"] != version || output["commit"] != commit || output["incus_sdk_version"] != "v7.3.0" {
		t.Fatalf("unexpected version output: %#v", output)
	}
}

func TestUnknownCommandFailsWithoutStartingExternalProtocol(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"unknown"}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("run unknown exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q, want unknown-command diagnostic", stderr.String())
	}
}

func TestProbeRejectsPositionalArgumentsBeforeReadingConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"probe", "unexpected"}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("run probe exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "accepts no positional arguments") {
		t.Fatalf("stderr = %q, want positional-argument diagnostic", stderr.String())
	}
}

func TestWarmPoolRequiresExplicitControllerIdentityBeforeReadingConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exitCode := run([]string{"warm-pool", "--apply"}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("run warm-pool exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "requires --controller-id") {
		t.Fatalf("stderr = %q, want controller identity diagnostic", stderr.String())
	}
}

func TestWarmDrainRequiresExactIdentity(t *testing.T) {
	for _, args := range [][]string{
		{"warm-drain"},
		{"warm-drain", "--controller-id", "controller"},
		{"warm-drain", "--instance", "warm-standard-0001"},
	} {
		var stdout, stderr bytes.Buffer
		if exitCode := run(args, &stdout, &stderr); exitCode != 2 {
			t.Fatalf("run(%v) exit code = %d, want 2", args, exitCode)
		}
		if !strings.Contains(stderr.String(), "requires --controller-id and --instance") {
			t.Fatalf("stderr = %q, want exact identity diagnostic", stderr.String())
		}
	}
}
