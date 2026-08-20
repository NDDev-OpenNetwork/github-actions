package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
)

func TestValidateCommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"validate",
		"--config", filepath.Join("..", "..", "config", "example-runner-1.yaml"),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"valid": true`) || !strings.Contains(stdout.String(), `"backends": 1`) || !strings.Contains(stdout.String(), `"pools": 10`) {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestValidateCacheCommand(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"validate-cache",
		"--manifest", filepath.Join("..", "..", "config", "cache-artifacts.yaml"),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	for _, wanted := range []string{
		`"valid": true`,
		`"rustfs": "1.0.0-rc.1"`,
		`"oci_registry": "zot@v2.1.20"`,
		`"rustfs_deployment_stage": "canary-only"`,
		`"rustfs_production_promotion_allowed": false`,
		`"oci_registry_deployment_stage": "production"`,
		`"oci_registry_production_promotion_allowed": true`,
		`"production_promotion_allowed": false`,
	} {
		if !strings.Contains(stdout.String(), wanted) {
			t.Fatalf("cache output does not contain %s: %s", wanted, stdout.String())
		}
	}
}

func TestUnknownCommandFailsWithUsage(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{"unknown"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
}

func TestReconcileZotCredentialsRejectsPositionalArguments(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{"reconcile-zot-credentials", "unexpected"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "accepts no positional arguments") {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
}

func TestReconcileRustFSCacheRejectsPositionalArguments(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{"reconcile-rustfs-cache", "unexpected"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "accepts no positional arguments") {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
}

func TestValidateRustFSCacheCanonicalConfig(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"validate-rustfs-cache", "--config", "../../config/rustfs-cache-identities.yaml",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"bucket": "github-actions-cache"`) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}
}

func TestBootstrapGitHubAppRequiresOutputDirectory(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{"bootstrap-github-app"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "requires --output-dir") {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
}

func TestBootstrapGitHubAppRejectsNonLoopbackListener(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"bootstrap-github-app",
		"--listen", "0.0.0.0:0",
		"--output-dir", filepath.Join(t.TempDir(), "credentials"),
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "must use 127.0.0.1") {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
}

func TestReconcileGARMRejectsPositionalArguments(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{"reconcile-garm", "unexpected"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "accepts no positional arguments") {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
}

func TestReconcileIncusDefaultsToReadOnlyPlan(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"reconcile-incus",
		"--config", filepath.Join("..", "..", "config", "example-runner-1.yaml"),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	for _, wanted := range []string{`"applied": false`, `"version": "v6.0.6"`, `"name": "nddev-linux-standard"`} {
		if !strings.Contains(stdout.String(), wanted) {
			t.Fatalf("plan output does not contain %s: %s", wanted, stdout.String())
		}
	}
}

// A pool whose declared network policy no bridge implements must be refused
// through the command an operator actually runs, not only inside the planner.
// No shipped configuration carries such a policy any more -- release is
// implemented as the pool's own NIC ACL and fast stopped asking for
// github-cache-only -- so the case is built here rather than manufactured in a
// host configuration nobody deploys.
func TestReconcileIncusRejectsUnsafePoolPolicy(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	fast := strings.Index(string(source), "- name: nddev-linux-fast")
	if fast < 0 {
		t.Fatal("the fast pool is no longer declared in the reference host configuration")
	}
	head, tail := string(source)[:fast], string(source)[fast:]
	tail = strings.Replace(tail, "network_policy: public-internet", "network_policy: github-cache-only", 1)
	unsafe := filepath.Join(t.TempDir(), "unsafe.yaml")
	if err := os.WriteFile(unsafe, []byte(head+tail), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"reconcile-incus",
		"--config", unsafe,
		"--pool", "nddev-linux-fast",
	}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "cannot use the public-egress pilot bridge") {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
}

func TestReconcileImageDefaultsToReadOnlyPlan(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"reconcile-image",
		"--config", filepath.Join("..", "..", "config", "example-runner-1.yaml"),
		"--manifest", filepath.Join("..", "..", "config", "golden-image-container.yaml"),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	for _, wanted := range []string{`"applied": false`, `"smoke_fingerprint": "sha256:`, `"project": "gha-fleet"`, `"version": "v2.336.0"`} {
		if !strings.Contains(stdout.String(), wanted) {
			t.Fatalf("image plan output does not contain %s: %s", wanted, stdout.String())
		}
	}
}

func TestReconcileImageStageOnlyRequiresApply(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := run([]string{"reconcile-image", "--stage-only"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--stage-only requires --apply") {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
}

func TestWriteImagePreflightRejectionIncludesPhase(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	code := writeImagePreflightRejection(&stderr, "pre-mutation", hostprobe.Decision{PilotReady: false})
	if code != 3 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	for _, wanted := range []string{`"error": "image-build preflight rejected"`, `"phase": "pre-mutation"`, `"pilot_ready": false`} {
		if !strings.Contains(stderr.String(), wanted) {
			t.Fatalf("rejection output does not contain %s: %s", wanted, stderr.String())
		}
	}
}
