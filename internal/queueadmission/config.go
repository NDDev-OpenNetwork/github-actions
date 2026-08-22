package queueadmission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

const SchemaVersion = 5
const maxConfigBytes = 64 * 1024

type ResourceBudget struct {
	CPUUnits  int `json:"cpu_units"`
	MemoryMiB int `json:"memory_mib"`
}

type ScaleSetResources struct {
	CPUUnits             int `json:"cpu_units"`
	MemoryMiB            int `json:"memory_mib"`
	ReservationCPUUnits  int `json:"reservation_cpu_units"`
	ReservationMemoryMiB int `json:"reservation_memory_mib"`
	Priority             int `json:"priority"`
}

type RepositoryPolicy struct {
	Weight      uint64 `json:"weight"`
	MaxInFlight int    `json:"max_in_flight"`
}

type Config struct {
	SchemaVersion             int                          `json:"schema_version"`
	MaxInFlight               int                          `json:"max_in_flight"`
	MaxBackgroundInFlight     int                          `json:"max_background_in_flight"`
	DefaultRepositoryLimit    int                          `json:"default_repository_limit"`
	DefaultWeight             uint64                       `json:"default_weight"`
	QueuedTTLSeconds          int                          `json:"queued_ttl_seconds"`
	AcquiringTTLSeconds       int                          `json:"acquiring_ttl_seconds"`
	AcquiredTTLSeconds        int                          `json:"acquired_ttl_seconds"`
	ExecutionTTLSeconds       int                          `json:"execution_ttl_seconds"`
	PriorityAgingSeconds      int                          `json:"priority_aging_seconds"`
	MaxRepositorySharePercent int                          `json:"max_repository_share_percent"`
	Capacity                  ResourceBudget               `json:"capacity"`
	ScaleSets                 map[string]ScaleSetResources `json:"scale_sets"`
	Repositories              map[string]RepositoryPolicy  `json:"repositories"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read queue admission config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return Config{}, fmt.Errorf("queue admission config exceeds %d bytes", maxConfigBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result Config
	if err := decoder.Decode(&result); err != nil {
		return Config{}, fmt.Errorf("decode queue admission config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("queue admission config has trailing data")
	}
	if err := result.Validate(); err != nil {
		return Config{}, err
	}
	return result, nil
}

func (c Config) Validate() error {
	if c.SchemaVersion != SchemaVersion || c.MaxInFlight < 1 || c.MaxInFlight > 64 ||
		c.MaxBackgroundInFlight < 1 || c.MaxBackgroundInFlight > c.MaxInFlight ||
		c.DefaultRepositoryLimit < 1 || c.DefaultRepositoryLimit > c.MaxInFlight ||
		c.DefaultWeight < 1 || c.DefaultWeight > 100 {
		return fmt.Errorf("queue admission identity or count limits are invalid")
	}
	if c.QueuedTTLSeconds != 600 || c.AcquiringTTLSeconds != 120 || c.AcquiredTTLSeconds != 600 ||
		c.ExecutionTTLSeconds != 86400 || c.PriorityAgingSeconds != 300 ||
		c.MaxRepositorySharePercent != 75 {
		return fmt.Errorf("queue admission TTL/aging policy is invalid")
	}
	if c.Capacity.CPUUnits < 1 || c.Capacity.MemoryMiB < 1024 || len(c.ScaleSets) == 0 || c.Repositories == nil {
		return fmt.Errorf("queue admission resource contract is incomplete")
	}
	for name, resources := range c.ScaleSets {
		if name == "" || resources.CPUUnits < 1 || resources.CPUUnits > c.Capacity.CPUUnits ||
			resources.MemoryMiB < 1 || resources.MemoryMiB > c.Capacity.MemoryMiB ||
			resources.ReservationCPUUnits < 1 || resources.ReservationCPUUnits > resources.CPUUnits ||
			resources.ReservationMemoryMiB < 256 || resources.ReservationMemoryMiB%256 != 0 ||
			resources.ReservationMemoryMiB > resources.MemoryMiB ||
			resources.Priority < 0 || resources.Priority > 2 {
			return fmt.Errorf("queue admission scale-set resources %q are invalid", name)
		}
	}
	for repository, policy := range c.Repositories {
		if repository == "" || policy.Weight < 1 || policy.Weight > 100 ||
			policy.MaxInFlight < 1 || policy.MaxInFlight > c.MaxInFlight {
			return fmt.Errorf("queue admission repository policy %q is invalid", repository)
		}
	}
	return nil
}

func (c Config) ValidateAgainstPlatform(platform config.Config) error {
	wantCPU, wantMemory := platform.Incus.FleetMaxCPUUnits(), platform.Incus.FleetMaxMemoryMiB()
	if c.Capacity.CPUUnits != wantCPU || c.Capacity.MemoryMiB != wantMemory {
		return fmt.Errorf("queue capacity cpu=%d memory_mib=%d differs from platform cpu=%d memory_mib=%d",
			c.Capacity.CPUUnits, c.Capacity.MemoryMiB, wantCPU, wantMemory)
	}
	seen := make(map[string]ScaleSetResources)
	for _, pool := range platform.Pools {
		declared, exists := c.ScaleSets[pool.ScaleSetName]
		if !exists {
			return fmt.Errorf("queue admission omits scale set %q", pool.ScaleSetName)
		}
		want := ScaleSetResources{
			CPUUnits: pool.Resources.VCPU, MemoryMiB: pool.Resources.MemoryMiB,
			ReservationCPUUnits:  pool.EffectiveReservation().CPUUnits,
			ReservationMemoryMiB: pool.EffectiveReservation().MemoryMiB,
			Priority:             declared.Priority,
		}
		if declared != want {
			return fmt.Errorf("queue resources for %q are %#v, want %#v", pool.ScaleSetName, declared, want)
		}
		if previous, duplicate := seen[pool.ScaleSetName]; duplicate && previous != want {
			return fmt.Errorf("platform duplicates scale set %q with different resources", pool.ScaleSetName)
		}
		seen[pool.ScaleSetName] = want
	}
	for name := range c.ScaleSets {
		if _, exists := seen[name]; !exists {
			return fmt.Errorf("queue admission declares unknown scale set %q", name)
		}
	}
	return nil
}
