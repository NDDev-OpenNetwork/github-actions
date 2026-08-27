package pressureobserve

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	standardUpdatesPattern = regexp.MustCompile(`(?m)([0-9]+) updates? can be applied immediately\.`)
	esmUpdatesPattern      = regexp.MustCompile(`(?m)([0-9]+) additional security updates? can be applied with ESM Apps\.`)
)

type Compliance struct {
	Complete                    bool
	RebootRequired              bool
	KernelRelease               string
	SRSOStatus                  string
	StandardUpdatesAvailable    int
	ESMSecurityUpdatesAvailable int
	PackageInventoryAgeSeconds  float64
}

func CollectCompliance(root string, now time.Time) Compliance {
	if root == "" {
		root = "/"
	}
	result := Compliance{PackageInventoryAgeSeconds: -1, SRSOStatus: "unknown"}
	result.RebootRequired = regularFileExists(filepath.Join(root, "var", "run", "reboot-required"))

	kernel, kernelErr := os.ReadFile(filepath.Join(root, "proc", "sys", "kernel", "osrelease"))
	if kernelErr == nil {
		result.KernelRelease = strings.TrimSpace(string(kernel))
	}
	srso, srsoErr := os.ReadFile(filepath.Join(root, "sys", "devices", "system", "cpu", "vulnerabilities", "spec_rstack_overflow"))
	if srsoErr == nil {
		result.SRSOStatus = classifySRSO(string(srso))
	}

	updatesPath := filepath.Join(root, "var", "lib", "update-notifier", "updates-available")
	updates, updatesErr := os.ReadFile(updatesPath)
	if updatesErr == nil {
		result.StandardUpdatesAvailable = firstCount(standardUpdatesPattern, string(updates))
		result.ESMSecurityUpdatesAvailable = firstCount(esmUpdatesPattern, string(updates))
		if info, err := os.Stat(updatesPath); err == nil && !info.ModTime().After(now) {
			result.PackageInventoryAgeSeconds = now.Sub(info.ModTime()).Seconds()
		}
	}
	result.Complete = kernelErr == nil && result.KernelRelease != "" && srsoErr == nil && updatesErr == nil && result.PackageInventoryAgeSeconds >= 0
	return result
}

func RenderCompliance(state Compliance) string {
	var output strings.Builder
	gauge := func(name, help string, value float64) {
		fmt.Fprintf(&output, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", name, help, name, name, strconv.FormatFloat(value, 'f', -1, 64))
	}
	gauge("gha_fleet_host_compliance_observer_up", "Whether kernel and package compliance state was read completely.", boolFloat(state.Complete))
	gauge("gha_fleet_host_reboot_required", "Whether the host requires a reboot to finish package maintenance.", boolFloat(state.RebootRequired))
	gauge("gha_fleet_host_standard_updates_available", "Standard Ubuntu package updates currently available.", float64(state.StandardUpdatesAvailable))
	gauge("gha_fleet_host_esm_security_updates_available", "Additional security updates available only through Ubuntu ESM Apps.", float64(state.ESMSecurityUpdatesAvailable))
	gauge("gha_fleet_host_package_inventory_age_seconds", "Age of the update-notifier package inventory, or -1 when unavailable.", state.PackageInventoryAgeSeconds)
	fmt.Fprintf(&output, "# HELP gha_fleet_host_kernel_info Running host kernel identity.\n# TYPE gha_fleet_host_kernel_info gauge\ngha_fleet_host_kernel_info{release=%q} 1\n", escapeLabel(state.KernelRelease))
	fmt.Fprintf(&output, "# HELP gha_fleet_host_srso_status Speculative return stack overflow status reported by the running kernel.\n# TYPE gha_fleet_host_srso_status gauge\ngha_fleet_host_srso_status{status=%q} 1\n", escapeLabel(state.SRSOStatus))
	return output.String()
}

func classifySRSO(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(normalized, "not affected"):
		return "not_affected"
	case strings.Contains(normalized, "vulnerable"):
		return "vulnerable"
	case strings.Contains(normalized, "mitigation"):
		return "mitigated"
	default:
		return "unknown"
	}
}

func firstCount(pattern *regexp.Regexp, value string) int {
	match := pattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return 0
	}
	count, _ := strconv.Atoi(match[1])
	return count
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
