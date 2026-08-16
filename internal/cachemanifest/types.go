package cachemanifest

// Manifest pins every release input used by the local cache plane. It contains
// no credentials and does not treat a mutable release alias as an artifact.
type Manifest struct {
	SchemaVersion int         `json:"schema_version" yaml:"schema_version"`
	RustFS        RustFS      `json:"rustfs" yaml:"rustfs"`
	OCIRegistry   OCIRegistry `json:"oci_registry" yaml:"oci_registry"`
}

type Asset struct {
	Name   string `json:"name" yaml:"name"`
	URL    string `json:"url" yaml:"url"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

type RustFS struct {
	Repository                 string `json:"repository" yaml:"repository"`
	Version                    string `json:"version" yaml:"version"`
	SourceCommit               string `json:"source_commit" yaml:"source_commit"`
	Architecture               string `json:"architecture" yaml:"architecture"`
	LibC                       string `json:"libc" yaml:"libc"`
	Archive                    Asset  `json:"archive" yaml:"archive"`
	BinarySHA256               string `json:"binary_sha256" yaml:"binary_sha256"`
	Checksums                  Asset  `json:"checksums" yaml:"checksums"`
	Provenance                 Asset  `json:"provenance" yaml:"provenance"`
	SBOM                       Asset  `json:"sbom" yaml:"sbom"`
	DeploymentStage            string `json:"deployment_stage" yaml:"deployment_stage"`
	ProductionPromotionAllowed bool   `json:"production_promotion_allowed" yaml:"production_promotion_allowed"`
}

type OCIRegistry struct {
	Implementation             string            `json:"implementation" yaml:"implementation"`
	Repository                 string            `json:"repository" yaml:"repository"`
	Version                    string            `json:"version" yaml:"version"`
	SourceCommit               string            `json:"source_commit" yaml:"source_commit"`
	Architecture               string            `json:"architecture" yaml:"architecture"`
	BuildProfile               string            `json:"build_profile" yaml:"build_profile"`
	StorageDriver              string            `json:"storage_driver" yaml:"storage_driver"`
	ExtensionsEnabled          bool              `json:"extensions_enabled" yaml:"extensions_enabled"`
	Binary                     Asset             `json:"binary" yaml:"binary"`
	Checksums                  Asset             `json:"checksums" yaml:"checksums"`
	Schema                     Asset             `json:"schema" yaml:"schema"`
	ReproducibleBuild          ReproducibleBuild `json:"reproducible_build" yaml:"reproducible_build"`
	RuntimeEvidence            RuntimeEvidence   `json:"runtime_evidence" yaml:"runtime_evidence"`
	DeploymentStage            string            `json:"deployment_stage" yaml:"deployment_stage"`
	ProductionPromotionAllowed bool              `json:"production_promotion_allowed" yaml:"production_promotion_allowed"`
}

type RuntimeEvidence struct {
	StorageAuditFile         string `json:"storage_audit_file" yaml:"storage_audit_file"`
	StorageAuditSHA256       string `json:"storage_audit_sha256" yaml:"storage_audit_sha256"`
	AuthorizationAuditFile   string `json:"authorization_audit_file" yaml:"authorization_audit_file"`
	AuthorizationAuditSHA256 string `json:"authorization_audit_sha256" yaml:"authorization_audit_sha256"`
	RebootAuditFile          string `json:"reboot_audit_file" yaml:"reboot_audit_file"`
	RebootAuditSHA256        string `json:"reboot_audit_sha256" yaml:"reboot_audit_sha256"`
}

type ReproducibleBuild struct {
	Toolchain         string `json:"toolchain" yaml:"toolchain"`
	GoExperiment      string `json:"goexperiment" yaml:"goexperiment"`
	CGOEnabled        bool   `json:"cgo_enabled" yaml:"cgo_enabled"`
	GOAMD64           string `json:"goamd64" yaml:"goamd64"`
	BuildMode         string `json:"build_mode" yaml:"build_mode"`
	CommitDescription string `json:"commit_description" yaml:"commit_description"`
	IndependentBuilds int    `json:"independent_builds" yaml:"independent_builds"`
	OutputSHA256      string `json:"output_sha256" yaml:"output_sha256"`
	EvidenceFile      string `json:"evidence_file" yaml:"evidence_file"`
	EvidenceSHA256    string `json:"evidence_sha256" yaml:"evidence_sha256"`
}
