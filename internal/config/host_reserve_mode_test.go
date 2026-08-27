package config

import "testing"

// A dedicated floor must never be reachable without saying so. The fleet no
// longer runs a host that shares its capacity with anything, so this builds
// the retained-workloads case rather than loading it: the property under test
// belongs to the validator, not to whichever host happens to exist.
func TestHostReserveFloorFollowsDeclaredMode(t *testing.T) {
	base, err := Load("../../config/example-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if base.HostReserve.Mode != "dedicated" {
		t.Fatalf("a fleet host must declare dedicated, got %q", base.HostReserve.Mode)
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("the dedicated shape the fleet runs was rejected: %v", err)
	}
	burst := base
	burst.HostReserve.MinimumMemoryMiB = 768
	burst.HostReserve.MinimumPercent = 5
	if err := burst.Validate(); err != nil {
		t.Fatalf("a dedicated five-percent burst envelope was rejected: %v", err)
	}

	retained := base
	retained.HostReserve.Mode = "retained-workloads"
	retained.HostReserve.MinimumCPUUnits = 4
	retained.HostReserve.MinimumMemoryMiB = 16 * 1024
	if err := retained.Validate(); err != nil {
		t.Fatalf("a correctly declared retained-workloads host was rejected: %v", err)
	}

	for _, testCase := range []struct {
		name    string
		mutate  func(*Config)
		message string
	}{
		{"missing mode", func(c *Config) { c.HostReserve.Mode = "" }, "host_reserve.mode"},
		{"unknown mode", func(c *Config) { c.HostReserve.Mode = "shared" }, "host_reserve.mode"},
		{"retained host taking the dedicated memory floor", func(c *Config) {
			c.HostReserve.Mode = "retained-workloads"
			c.HostReserve.MinimumCPUUnits = 4
			c.HostReserve.MinimumMemoryMiB = 2048
		}, "minimum_memory_mib"},
		{"retained host taking the dedicated cpu floor", func(c *Config) {
			c.HostReserve.Mode = "retained-workloads"
			c.HostReserve.MinimumCPUUnits = 2
			c.HostReserve.MinimumMemoryMiB = 16 * 1024
		}, "minimum_cpu_units"},
		{"dedicated host below its own floor", func(c *Config) {
			c.HostReserve.MinimumMemoryMiB = 512
		}, "minimum_memory_mib"},
		{"dedicated percent below burst floor", func(c *Config) {
			c.HostReserve.Mode = "dedicated"
			c.HostReserve.MinimumMemoryMiB = 768
			c.HostReserve.MinimumPercent = 4
		}, "minimum_percent"},
		{"dedicated host below its own cpu floor", func(c *Config) {
			c.HostReserve.MinimumCPUUnits = 1
		}, "minimum_cpu_units"},
		{"fleet cpu ceiling can exceed the host slo", func(c *Config) {
			c.HostReserve.MaximumFleetCPUPercent = 99
		}, "maximum_fleet_cpu_percent"},
		{"fleet cpu ceiling wastes capacity", func(c *Config) {
			c.HostReserve.MaximumFleetCPUPercent = 79
		}, "maximum_fleet_cpu_percent"},
	} {
		mutated := base
		testCase.mutate(&mutated)
		err := mutated.Validate()
		if err == nil || !contains(err.Error(), testCase.message) {
			t.Errorf("%s: expected %q, got %v", testCase.name, testCase.message, err)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
