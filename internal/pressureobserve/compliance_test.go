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
	write("proc/meminfo", "MemTotal: 16000000 kB\nSUnreclaim:     1066110 kB\nSReclaimable: 200000 kB\n")
	write("sys/fs/cgroup/memory.stat", "anon 1\nslab_reclaimable 766643200\nslab_unreclaimable 46323712\nslab 812966912\n")
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
		// The drifted counter and the attributed truth ship side by side:
		// the 2026-09-01 member showed ~1 GiB against 44 MiB.
		"gha_fleet_host_slab_unreclaimable_counter_bytes 1091696640\n",
		"gha_fleet_host_slab_unreclaimable_attributed_bytes 46323712\n",
	} {
		if !strings.Contains(metrics, wanted) {
			t.Fatalf("metrics missing %q\n%s", wanted, metrics)
		}
	}
}

func TestComplianceFailsObservableWhenInputsAreMissing(t *testing.T) {
	state := CollectCompliance(t.TempDir(), time.Now().UTC())
	if state.SlabUnreclaimableCounterBytes != -1 || state.SlabUnreclaimableAttributedBytes != -1 {
		t.Fatalf("absent slab inputs must read -1, got %v / %v",
			state.SlabUnreclaimableCounterBytes, state.SlabUnreclaimableAttributedBytes)
	}
	if state.Complete || state.PackageInventoryAgeSeconds != -1 || state.SRSOStatus != "unknown" {
		t.Fatalf("state=%#v", state)
	}
	metrics := RenderCompliance(state)
	if !strings.Contains(metrics, "gha_fleet_host_compliance_observer_up 0\n") {
		t.Fatalf("missing incomplete metric\n%s", metrics)
	}
}
