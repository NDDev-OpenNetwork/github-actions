package pressureobserve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectAndRenderCompliance(t *testing.T) {
	root := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/sys/kernel/osrelease", "6.8.0-138-generic\n")
	write("sys/devices/system/cpu/vulnerabilities/spec_rstack_overflow", "Vulnerable: Safe RET, no microcode\n")
	write("var/lib/update-notifier/updates-available", "0 updates can be applied immediately.\n3 additional security updates can be applied with ESM Apps.\n")
	write("var/run/reboot-required", "*** System restart required ***\n")
	now := time.Now().UTC()
	state := CollectCompliance(root, now)
	if !state.Complete || !state.RebootRequired || state.KernelRelease != "6.8.0-138-generic" || state.SRSOStatus != "vulnerable" || state.StandardUpdatesAvailable != 0 || state.ESMSecurityUpdatesAvailable != 3 {
		t.Fatalf("state=%#v", state)
	}
	metrics := RenderCompliance(state)
	for _, wanted := range []string{
		"gha_fleet_host_compliance_observer_up 1\n",
		"gha_fleet_host_reboot_required 1\n",
		"gha_fleet_host_standard_updates_available 0\n",
		"gha_fleet_host_esm_security_updates_available 3\n",
		`gha_fleet_host_kernel_info{release="6.8.0-138-generic"} 1`,
		`gha_fleet_host_srso_status{status="vulnerable"} 1`,
	} {
		if !strings.Contains(metrics, wanted) {
			t.Fatalf("metrics missing %q\n%s", wanted, metrics)
		}
	}
}

func TestComplianceFailsObservableWhenInputsAreMissing(t *testing.T) {
	state := CollectCompliance(t.TempDir(), time.Now().UTC())
	if state.Complete || state.PackageInventoryAgeSeconds != -1 || state.SRSOStatus != "unknown" {
		t.Fatalf("state=%#v", state)
	}
	metrics := RenderCompliance(state)
	if !strings.Contains(metrics, "gha_fleet_host_compliance_observer_up 0\n") {
		t.Fatalf("missing incomplete metric\n%s", metrics)
	}
}
