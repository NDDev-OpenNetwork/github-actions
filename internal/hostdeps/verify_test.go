package hostdeps

import (
	"strings"
	"testing"
)

func TestParsePackageReport(t *testing.T) {
	t.Parallel()

	versions, err := parsePackageReport([]byte("qemu-system-modules-spice\tii \t1:8.2.2+ds-0ubuntu1.18\n"))
	if err != nil {
		t.Fatalf("parse package report: %v", err)
	}
	if versions["qemu-system-modules-spice"] != "1:8.2.2+ds-0ubuntu1.18" {
		t.Fatalf("unexpected versions: %#v", versions)
	}
}

func TestParsePackageReportRejectsNonInstalledPackage(t *testing.T) {
	t.Parallel()

	_, err := parsePackageReport([]byte("qemu-system-modules-spice\tunknown ok not-installed\t\n"))
	if err == nil || !strings.Contains(err.Error(), "not-installed") {
		t.Fatalf("expected package-status failure, got %v", err)
	}
}

func TestParsePackageReportRejectsMalformedLine(t *testing.T) {
	t.Parallel()

	_, err := parsePackageReport([]byte("qemu-system-modules-spice installed\n"))
	if err == nil || !strings.Contains(err.Error(), "invalid dpkg package report") {
		t.Fatalf("expected malformed-report failure, got %v", err)
	}
}

// Provisioning a new host found two packages this check did not name. Each was
// absent for the same reason -- Ubuntu splits them out and
// --no-install-recommends skips them -- and each failed later and less clearly
// than it would have here.
func TestIncusVMHostPackagesCoverWhatAFullVMNeeds(t *testing.T) {
	t.Parallel()
	required := map[string]string{
		"qemu-system-modules-spice": "the VM driver is unusable without it",
		"ovmf":                      "QEMU fails its feature checks and the server never advertises the driver",
		"dnsmasq-base":              "the managed bridge cannot be created",
	}
	declared := make(map[string]struct{}, len(incusVMHostPackages))
	for _, name := range incusVMHostPackages {
		declared[name] = struct{}{}
	}
	for name, why := range required {
		if _, ok := declared[name]; !ok {
			t.Errorf("%s is not verified before the Incus API is reached: %s", name, why)
		}
	}
	for index := 1; index < len(incusVMHostPackages); index++ {
		if incusVMHostPackages[index-1] >= incusVMHostPackages[index] {
			t.Fatalf("package list is not sorted at %q; keep it ordered so a duplicate is visible",
				incusVMHostPackages[index])
		}
	}
}
