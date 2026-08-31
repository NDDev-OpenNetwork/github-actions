package cachemanifest

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

var (
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	filePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
	shaPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	versionPattern = regexp.MustCompile(
		`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`,
	)
)

type Issue struct {
	Path    string
	Message string
}

type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		parts = append(parts, issue.Path+": "+issue.Message)
	}
	return "invalid cache artifact manifest: " + strings.Join(parts, "; ")
}

func (m Manifest) Validate() error {
	issues := make([]Issue, 0)
	add := func(field, message string) { issues = append(issues, Issue{field, message}) }
	if m.SchemaVersion != 1 {
		add("schema_version", "must be 1")
	}
	validateRustFS(add, m.RustFS)
	validateOCI(add, m.OCIRegistry)
	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path == issues[j].Path {
			return issues[i].Message < issues[j].Message
		}
		return issues[i].Path < issues[j].Path
	})
	return &ValidationError{Issues: issues}
}

func validateRustFS(add func(string, string), component RustFS) {
	const repository = "rustfs/rustfs"
	if component.Repository != repository {
		add("rustfs.repository", "must be "+repository)
	}
	validateVersion(add, "rustfs.version", component.Version)
	validateCommit(add, "rustfs.source_commit", component.SourceCommit)
	if component.Architecture != "x86_64" || component.LibC != "gnu" {
		add("rustfs", "must select the x86_64 GNU/Linux build")
	}
	validateReleaseAsset(add, "rustfs.archive", repository, component.Version, component.Archive)
	validateReleaseAsset(add, "rustfs.checksums", repository, component.Version, component.Checksums)
	validateReleaseAsset(add, "rustfs.provenance", repository, component.Version, component.Provenance)
	validateReleaseAsset(add, "rustfs.sbom", repository, component.Version, component.SBOM)
	validateSHA(add, "rustfs.binary_sha256", component.BinarySHA256)
	expectedArchive := "rustfs-linux-x86_64-gnu-v" + component.Version + ".zip"
	if component.Archive.Name != expectedArchive {
		add("rustfs.archive.name", "must be "+expectedArchive)
	}
	if component.Checksums.Name != "SHA256SUMS" {
		add("rustfs.checksums.name", "must be SHA256SUMS")
	}
	if component.Provenance.Name != "rustfs-"+component.Version+".provenance.json" {
		add("rustfs.provenance.name", "must match the exact release provenance asset")
	}
	if component.SBOM.Name != "rustfs-"+component.Version+".sbom.cdx.json" {
		add("rustfs.sbom.name", "must match the exact release SBOM asset")
	}
	if strings.Contains(component.Archive.Name, "latest") {
		add("rustfs.archive.name", "must not use a mutable latest asset")
	}
	if component.DeploymentStage != "canary-only" || component.ProductionPromotionAllowed {
		add("rustfs.deployment_stage", "pre-release RustFS must remain canary-only and production-blocked")
	}
}

func validateOCI(add func(string, string), component OCIRegistry) {
	const repository = "project-zot/zot"
	if component.Implementation != "zot" || component.Repository != repository {
		add("oci_registry", "must select project-zot/zot")
	}
	validateVersion(add, "oci_registry.version", component.Version)
	validateCommit(add, "oci_registry.source_commit", component.SourceCommit)
	if component.Architecture != "amd64" || component.BuildProfile != "minimal" {
		add("oci_registry", "must select the amd64 minimal build")
	}
	if component.StorageDriver != "filesystem" {
		add("oci_registry.storage_driver", "must be filesystem")
	}
	// The registry was pinned to the minimal build while it only served the
	// authenticated cache buckets. The dockerd registry mirror needs the sync
	// extension, which ships in the full asset, so extensions are now part of
	// the contract rather than forbidden by it.
	if !component.ExtensionsEnabled {
		add("oci_registry.extensions_enabled", "must be true: the docker mirror needs the sync extension")
	}
	validateReleaseAsset(add, "oci_registry.binary", repository, component.Version, component.Binary)
	validateReleaseAsset(add, "oci_registry.checksums", repository, component.Version, component.Checksums)
	validateReleaseAsset(add, "oci_registry.schema", repository, component.Version, component.Schema)
	if component.Binary.Name != "zot-linux-amd64" {
		add("oci_registry.binary.name", "must be the full linux/amd64 asset that carries the sync extension")
	}
	if component.Checksums.Name != "checksums.sha256.txt" {
		add("oci_registry.checksums.name", "must be checksums.sha256.txt")
	}
	if component.Schema.Name != "zot-schema.json" {
		add("oci_registry.schema.name", "must be zot-schema.json")
	}
	validateZotReproducibleBuild(add, component)
	validateZotRuntimeEvidence(add, component)
	if component.DeploymentStage != "production" || !component.ProductionPromotionAllowed {
		add("oci_registry.deployment_stage", "must be production and promotion-allowed after the bound reboot gate")
	}
}

func validateZotRuntimeEvidence(add func(string, string), component OCIRegistry) {
	expectedStorageFile := "zot-" + component.Version + "-storage-audit.json"
	if component.RuntimeEvidence.StorageAuditFile != expectedStorageFile {
		add("oci_registry.runtime_evidence.storage_audit_file", "must be "+expectedStorageFile)
	}
	validateSHA(add, "oci_registry.runtime_evidence.storage_audit_sha256", component.RuntimeEvidence.StorageAuditSHA256)
	expectedAuthorizationFile := "zot-" + component.Version + "-authz-audit.json"
	if component.RuntimeEvidence.AuthorizationAuditFile != expectedAuthorizationFile {
		add("oci_registry.runtime_evidence.authorization_audit_file", "must be "+expectedAuthorizationFile)
	}
	validateSHA(add, "oci_registry.runtime_evidence.authorization_audit_sha256", component.RuntimeEvidence.AuthorizationAuditSHA256)
	expectedRebootFile := "zot-" + component.Version + "-reboot-audit.json"
	if component.RuntimeEvidence.RebootAuditFile != expectedRebootFile {
		add("oci_registry.runtime_evidence.reboot_audit_file", "must be "+expectedRebootFile)
	}
	validateSHA(add, "oci_registry.runtime_evidence.reboot_audit_sha256", component.RuntimeEvidence.RebootAuditSHA256)
}

func validateZotReproducibleBuild(add func(string, string), component OCIRegistry) {
	build := component.ReproducibleBuild
	if !versionPattern.MatchString(strings.TrimPrefix(build.Toolchain, "go")) || !strings.HasPrefix(build.Toolchain, "go") {
		add("oci_registry.reproducible_build.toolchain", "must be an exact Go version")
	}
	if build.GoExperiment != "jsonv2" || build.CGOEnabled || build.GOAMD64 != "v1" || build.BuildMode != "pie" {
		add("oci_registry.reproducible_build", "must match the upstream minimal Linux build contract")
	}
	expectedDescription := ""
	if commitPattern.MatchString(component.SourceCommit) {
		expectedDescription = component.Version + "-0-g" + component.SourceCommit[:7]
	}
	if expectedDescription == "" || build.CommitDescription != expectedDescription {
		add("oci_registry.reproducible_build.commit_description", "must bind the exact version and source commit")
	}
	if build.IndependentBuilds < 2 {
		add("oci_registry.reproducible_build.independent_builds", "must record at least two clean builds")
	}
	validateSHA(add, "oci_registry.reproducible_build.output_sha256", build.OutputSHA256)
	if build.OutputSHA256 != component.Binary.SHA256 {
		add("oci_registry.reproducible_build.output_sha256", "must reproduce the pinned release binary")
	}
	expectedEvidenceFile := "zot-" + component.Version + "-reproducibility.json"
	if build.EvidenceFile != expectedEvidenceFile {
		add("oci_registry.reproducible_build.evidence_file", "must be "+expectedEvidenceFile)
	}
	validateSHA(add, "oci_registry.reproducible_build.evidence_sha256", build.EvidenceSHA256)
}

func validateVersion(add func(string, string), field, value string) {
	if !versionPattern.MatchString(value) || strings.Contains(value, "latest") {
		add(field, "must be an exact semantic version")
	}
}

func validateCommit(add func(string, string), field, value string) {
	if !commitPattern.MatchString(value) {
		add(field, "must be a lowercase 40-character commit ID")
	}
}

func validateReleaseAsset(add func(string, string), field, repository, version string, asset Asset) {
	if !filePattern.MatchString(asset.Name) || path.Base(asset.Name) != asset.Name {
		add(field+".name", "must be a plain filename")
	}
	validateSHA(add, field+".sha256", asset.SHA256)
	wantedPath := "/" + repository + "/releases/download/" + version + "/" + asset.Name
	if asset.URL != "https://github.com"+wantedPath {
		add(field+".url", fmt.Sprintf("must exactly match https://github.com%s", wantedPath))
	}
}

func validateSHA(add func(string, string), field, value string) {
	if !shaPattern.MatchString(value) {
		add(field, "must be a lowercase SHA-256 digest")
	}
}
