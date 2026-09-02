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

func TestValidateTenantRegistryCanonicalConfig(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"validate-tenant-registry", "--config", "../../config/tenant-registry.yaml"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), `"default_tenant": "example"`) {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
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
		"--tenant-config", "../../config/tenant-registry.yaml",
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

// The rollout validates the provider toml with the gha-fleet it just built,
// on the host that will read it, before installing it: a rollback list
// longer than N-1 is refused with the validator's own words, and the command
// takes no positional arguments.
func TestValidateProviderConfigRefusesTwoPreviousIdentities(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "provider-incus.toml")
	if err := os.WriteFile(path, []byte(`current_provider_identity = { version = "v0.1.5-nddev.116", commit = "c8f5f8633ea3012fdf1cf12ef0c2195e23cc0c20" }

[[previous_provider_identities]]
version = "v0.1.5-nddev.115"
commit = "1208ef1a627309c3ed67663a86493e3cdc6dc30d"

[[previous_provider_identities]]
version = "v0.1.5-nddev.113"
commit = "a4cd4345bf5eeef92264260ede79ddad8e8c41ec"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"validate-provider-config", "--config", path}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code %d, stdout %s stderr %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "error validating config") || stdout.Len() != 0 {
		t.Fatalf("a refused config must be named on stderr only: stdout %q stderr %q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"validate-provider-config", "--config", path, "extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("positional argument accepted: exit %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"validate-provider-config", "--config", filepath.Join(t.TempDir(), "missing.toml")}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "error decoding config") {
		t.Fatalf("a missing config must fail as a decode error: exit %d stderr %q", code, stderr.String())
	}
}
