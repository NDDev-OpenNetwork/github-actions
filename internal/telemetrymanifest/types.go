package telemetrymanifest

// Manifest pins every release input of the telemetry plane. It contains no
// credentials and never treats a mutable release alias as an artifact, so a
// `latest` tag or an unversioned download URL is rejected rather than trusted.
//
// The plane has exactly two components. The collector runs on the CI host,
// which deliberately has no container runtime in its control plane, so it is
// pinned as a release tarball the same way the runner and sccache are. The
// store runs on a separate host and is pinned by image digest, because that is
// the only immutable identity a registry offers.
type Manifest struct {
	SchemaVersion int       `json:"schema_version" yaml:"schema_version"`
	Collector     Collector `json:"collector" yaml:"collector"`
	Store         Store     `json:"store" yaml:"store"`
	Transport     Transport `json:"transport" yaml:"transport"`
}

type Asset struct {
	Name   string `json:"name" yaml:"name"`
	URL    string `json:"url" yaml:"url"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

// Collector pins the OpenTelemetry Collector that runs beside the fleet control
// plane. Upstream publishes a SHA-256, a CycloneDX SBOM and a Sigstore bundle
// for every asset, so all three are pinned rather than only the archive.
type Collector struct {
	Implementation  string `json:"implementation" yaml:"implementation"`
	Repository      string `json:"repository" yaml:"repository"`
	Version         string `json:"version" yaml:"version"`
	Architecture    string `json:"architecture" yaml:"architecture"`
	Archive         Asset  `json:"archive" yaml:"archive"`
	Checksum        Asset  `json:"checksum" yaml:"checksum"`
	SBOM            Asset  `json:"sbom" yaml:"sbom"`
	SigstoreBundle  Asset  `json:"sigstore_bundle" yaml:"sigstore_bundle"`
	BinaryPath      string `json:"binary_path" yaml:"binary_path"`
	DeploymentStage string `json:"deployment_stage" yaml:"deployment_stage"`
}

// Store pins the OpenObserve instance that receives the fleet's telemetry.
//
// It is pinned as a release tarball rather than a container image because it
// now runs on a fleet host, and every other fleet component — the runner,
// sccache, RustFS, Zot, GARM and the collector — is a digest-pinned binary
// under systemd. Adding a container runtime to a host purely to run one
// service would widen its attack surface for no gain.
type Store struct {
	Implementation string   `json:"implementation" yaml:"implementation"`
	Version        string   `json:"version" yaml:"version"`
	Archive        Asset    `json:"archive" yaml:"archive"`
	BinaryPath     string   `json:"binary_path" yaml:"binary_path"`
	Host           string   `json:"host" yaml:"host"`
	Organization   string   `json:"organization" yaml:"organization"`
	ObjectStore    string   `json:"object_store" yaml:"object_store"`
	Bucket         string   `json:"bucket" yaml:"bucket"`
	Streams        []string `json:"streams" yaml:"streams"`
	RetentionDays  int      `json:"retention_days" yaml:"retention_days"`
	// StreamRetentionDays overrides RetentionDays for one stream. The two
	// signals age differently: a log line is worth keeping while someone is
	// still debugging the week it came from, and a metric point is the only
	// evidence of what the fleet's capacity did over a quarter. Storing both
	// for the same 90 days meant the class sizing done from seven days of
	// history had no longer window to check itself against, while nine
	// million log records were kept long past anyone reading them.
	StreamRetentionDays map[string]int `json:"stream_retention_days,omitempty" yaml:"stream_retention_days,omitempty"`
	DeploymentStage     string         `json:"deployment_stage" yaml:"deployment_stage"`
}

// Transport pins how the collector reaches the store. Both hosts sit in one
// private cloud network, so telemetry never leaves it and the listener is
// bound to that address rather than to every interface.
type Transport struct {
	Protocol       string `json:"protocol" yaml:"protocol"`
	SourceAddress  string `json:"source_address" yaml:"source_address"`
	TargetAddress  string `json:"target_address" yaml:"target_address"`
	TargetPort     int    `json:"target_port" yaml:"target_port"`
	QueueDirectory string `json:"queue_directory" yaml:"queue_directory"`
}

// FleetStreams are the streams the fleet owns in the store. Keeping them named
// here means a pipeline cannot quietly write into a stream nobody declared.
func FleetStreams() []string {
	return []string{"fleet_logs", "fleet_metrics", "fleet_traces"}
}
