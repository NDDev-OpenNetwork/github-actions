package hostprobe

// Snapshot is a read-only, secret-free observation of one Linux runner host.
// It is intentionally small enough to persist alongside deployment audit
// records and stable enough to compare before and after maintenance.
type Snapshot struct {
	SchemaVersion   int             `json:"schema_version"`
	CapturedAt      string          `json:"captured_at"`
	Hostname        string          `json:"hostname"`
	OperatingSystem OperatingSystem `json:"operating_system"`
	Virtualization  string          `json:"virtualization"`
	CPU             CPU             `json:"cpu"`
	Memory          Memory          `json:"memory"`
	RootFilesystem  Filesystem      `json:"root_filesystem"`
	KVM             KVM             `json:"kvm"`
	Maintenance     Maintenance     `json:"maintenance"`
	Software        Software        `json:"software"`
	LegacyRunners   LegacyRunners   `json:"legacy_runners"`
}

type OperatingSystem struct {
	ID           string `json:"id"`
	VersionID    string `json:"version_id"`
	PrettyName   string `json:"pretty_name"`
	Kernel       string `json:"kernel"`
	Architecture string `json:"architecture"`
}

type CPU struct {
	Logical        int     `json:"logical"`
	PhysicalCores  int     `json:"physical_cores"`
	Sockets        int     `json:"sockets"`
	ThreadsPerCore int     `json:"threads_per_core"`
	NUMANodes      int     `json:"numa_nodes"`
	Load1          float64 `json:"load_1"`
	Load5          float64 `json:"load_5"`
	Load15         float64 `json:"load_15"`
}

type Memory struct {
	TotalMiB     int `json:"total_mib"`
	AvailableMiB int `json:"available_mib"`
	SwapTotalMiB int `json:"swap_total_mib"`
	SwapFreeMiB  int `json:"swap_free_mib"`
}

type Filesystem struct {
	Source            string `json:"source"`
	Type              string `json:"type"`
	TotalMiB          uint64 `json:"total_mib"`
	AvailableMiB      uint64 `json:"available_mib"`
	FreePercent       int    `json:"free_percent"`
	FreeInodesPercent int    `json:"free_inodes_percent"`
	Rotational        bool   `json:"rotational"`
	RotationalKnown   bool   `json:"rotational_known"`
}

type KVM struct {
	Present    bool `json:"present"`
	Accessible bool `json:"accessible"`
	Nested     bool `json:"nested"`
}

type Maintenance struct {
	RebootRequired bool   `json:"reboot_required"`
	SystemState    string `json:"system_state"`
	// FailedUnits names the failed systemd services behind a degraded
	// SystemState. The aggregate alone cannot distinguish a failed fleet
	// dependency from an unrelated unit, and admission must not close the
	// whole host for the second kind.
	FailedUnits []string `json:"failed_units,omitempty"`
}

type Software struct {
	Incus  SoftwareVersion `json:"incus"`
	Docker SoftwareVersion `json:"docker"`
}

type SoftwareVersion struct {
	Present bool   `json:"present"`
	Version string `json:"version,omitempty"`
}

type LegacyRunners struct {
	Listeners int `json:"listeners"`
	Workers   int `json:"workers"`
}
