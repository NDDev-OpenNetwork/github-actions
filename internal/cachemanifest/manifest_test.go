package cachemanifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type zotReproducibilityEvidence struct {
	SchemaVersion                  int    `json:"schema_version"`
	ObservedAt                     string `json:"observed_at"`
	IndependenceScope              string `json:"independence_scope"`
	Repository                     string `json:"repository"`
	Version                        string `json:"version"`
	SourceCommit                   string `json:"source_commit"`
	CommitDescription              string `json:"commit_description"`
	GitHubCommitVerification       string `json:"github_commit_verification"`
	GitHubArtifactAttestationFound bool   `json:"github_artifact_attestation_found"`
	Toolchain                      string `json:"toolchain"`
	Build                          struct {
		CGOEnabled   bool   `json:"cgo_enabled"`
		GoExperiment string `json:"goexperiment"`
		GOAMD64      string `json:"goamd64"`
		BuildMode    string `json:"build_mode"`
		Trimpath     bool   `json:"trimpath"`
	} `json:"build"`
	ReleaseAsset struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	} `json:"release_asset"`
	Runs []struct {
		BuildID             string `json:"build_id"`
		FreshSourceCheckout bool   `json:"fresh_source_checkout"`
		IsolatedModuleCache bool   `json:"isolated_module_cache"`
		IsolatedBuildCache  bool   `json:"isolated_build_cache"`
		ModuleVerification  bool   `json:"module_verification"`
		VCSModified         bool   `json:"vcs_modified"`
		OutputSHA256        string `json:"output_sha256"`
		ReleaseAssetMatch   bool   `json:"release_asset_match"`
	} `json:"runs"`
}

type zotStorageAuditEvidence struct {
	SchemaVersion          int    `json:"schema_version"`
	ObservedAt             string `json:"observed_at"`
	ZotSHA256              string `json:"zot_sha256"`
	AuditScriptSHA256      string `json:"audit_script_sha256"`
	SourceImageFingerprint string `json:"source_image_fingerprint"`
	Execution              struct {
		IncusProject              string `json:"incus_project"`
		VMName                    string `json:"vm_name"`
		VCPUs                     int    `json:"vcpus"`
		Memory                    string `json:"memory"`
		NetworkDevices            int    `json:"network_devices"`
		DisposableVMDestroyed     bool   `json:"disposable_vm_destroyed"`
		TemporaryImageCopyRemoved bool   `json:"temporary_image_copy_removed"`
		LiveCacheTargeted         bool   `json:"live_cache_targeted"`
		RetainedTenantsTargeted   bool   `json:"retained_tenants_targeted"`
	} `json:"execution"`
	StorageBoundary struct {
		Type         string   `json:"type"`
		ImageSizeMiB int      `json:"image_size_mib"`
		MountOptions []string `json:"mount_options"`
	} `json:"storage_boundary"`
	GC struct {
		ServerStoppedForOfflineGC       bool `json:"server_stopped_for_offline_gc"`
		RetainedBlobPreserved           bool `json:"retained_blob_preserved"`
		OrphanBlobRemoved               bool `json:"orphan_blob_removed"`
		RetainedManifestReadableAfterGC bool `json:"retained_manifest_readable_after_gc"`
	} `json:"gc"`
	FullDisk struct {
		WriteRejected             bool `json:"write_rejected"`
		HTTPStatus                int  `json:"http_status"`
		ServiceRemainedActive     bool `json:"service_remained_active"`
		RetainedContentReadable   bool `json:"retained_content_readable"`
		PostReclaimWriteSucceeded bool `json:"post_reclaim_write_succeeded"`
	} `json:"full_disk"`
	FilesystemCheckClean             bool `json:"filesystem_check_clean"`
	FleetObserverHealthyAfterCleanup bool `json:"fleet_observer_healthy_after_cleanup"`
	GHAFleetInstancesAfterCleanup    int  `json:"gha_fleet_instances_after_cleanup"`
}

type zotAuthorizationAuditEvidence struct {
	SchemaVersion int    `json:"schema_version"`
	ObservedAt    string `json:"observed_at"`
	Controller    struct {
		Commit string `json:"commit"`
		SHA256 string `json:"sha256"`
	} `json:"controller"`
	ZotSHA256         string `json:"zot_sha256"`
	ConfigSHA256      string `json:"config_sha256"`
	SmokeScriptSHA256 string `json:"smoke_script_sha256"`
	CredentialState   string `json:"credential_state"`
	VMResult          struct {
		SchemaVersion          int    `json:"schema_version"`
		ObservedAt             string `json:"observed_at"`
		SourceImageFingerprint string `json:"source_image_fingerprint"`
		VM                     struct {
			Project string `json:"project"`
			Name    string `json:"name"`
			VCPUs   int    `json:"vcpus"`
			Memory  string `json:"memory"`
			Network string `json:"network"`
			RawQEMU string `json:"raw_qemu"`
		} `json:"vm"`
		Authz struct {
			TrustedOCICRUD             bool `json:"trusted_oci_crud"`
			UntrustedOCICRUD           bool `json:"untrusted_oci_crud"`
			PromoterOCICRUD            bool `json:"promoter_oci_crud"`
			ReleasePromotedRead        bool `json:"release_promoted_read"`
			ReleasePromotedWriteHTTP   int  `json:"release_promoted_write_http"`
			AllCrossNamespaceReadsHTTP int  `json:"all_cross_namespace_reads_http"`
			AnonymousHTTP              int  `json:"anonymous_http"`
		} `json:"authz"`
		Network struct {
			HostSSHReachable bool `json:"host_ssh_reachable"`
		} `json:"network"`
	} `json:"vm_result"`
	Cleanup struct {
		DisposableVMDestroyed         bool `json:"disposable_vm_destroyed"`
		TemporaryImageRemoved         bool `json:"temporary_image_removed"`
		TemporaryVolumeRemoved        bool `json:"temporary_volume_removed"`
		GHAFleetInstancesAfterCleanup int  `json:"gha_fleet_instances_after_cleanup"`
	} `json:"cleanup"`
	Service struct {
		Active                  bool    `json:"active"`
		AnonymousHTTP           int     `json:"anonymous_http"`
		SystemdSecurityExposure float64 `json:"systemd_security_exposure"`
		JournalSecretMatch      bool    `json:"journal_secret_match"`
	} `json:"service"`
	Diagnostics struct {
		SourceBundles       int `json:"source_bundles"`
		ExportedBundles     int `json:"exported_bundles"`
		PendingBundles      int `json:"pending_bundles"`
		ConsecutiveFailures int `json:"consecutive_failures"`
	} `json:"diagnostics"`
	RetainedServices struct {
		ExamplePlatformHTTP int `json:"example-platform_http"`
		CaptchaHTTP         int `json:"captcha_http"`
	} `json:"retained_services"`
	FaultControls struct {
		ReadinessRollbackExercised     bool `json:"readiness_rollback_exercised"`
		RollbackRestoredBootstrapState bool `json:"rollback_restored_bootstrap_state"`
		LoadCircuitBreakerExercised    bool `json:"load_circuit_breaker_exercised"`
	} `json:"fault_controls"`
}

type zotRebootAuditEvidence struct {
	SchemaVersion    int    `json:"schema_version"`
	ObservedAt       string `json:"observed_at"`
	RepositoryCommit string `json:"repository_commit"`
	Host             string `json:"host"`
	PreviousBoot     struct {
		BootID           string `json:"boot_id"`
		GateAccepted     bool   `json:"gate_accepted"`
		ZotBindAttemptAt string `json:"zot_bind_attempt_at"`
		GHA0CreatedAt    string `json:"gha0_created_at"`
		IncusActiveAt    string `json:"incus_active_at"`
		ZotExitStatus    int    `json:"zot_exit_status"`
	} `json:"previous_boot"`
	CorrectiveContract struct {
		NetworkProbeSHA256    string `json:"network_probe_sha256"`
		ZotUnitSHA256         string `json:"zot_unit_sha256"`
		RustFSUnitSHA256      string `json:"rustfs_unit_sha256"`
		RequiresIncus         bool   `json:"requires_incus"`
		Interface             string `json:"interface"`
		ProbeTimeoutSeconds   int    `json:"probe_timeout_seconds"`
		SystemdTimeoutSeconds int    `json:"systemd_timeout_seconds"`
		RestartPolicy         string `json:"restart_policy"`
	} `json:"corrective_contract"`
	AcceptedBoot struct {
		BootID                    string `json:"boot_id"`
		StartedAt                 string `json:"started_at"`
		SystemState               string `json:"system_state"`
		IncusActiveMonotonicUSec  int64  `json:"incus_active_monotonic_usec"`
		ZotActiveMonotonicUSec    int64  `json:"zot_active_monotonic_usec"`
		RustFSActiveMonotonicUSec int64  `json:"rustfs_active_monotonic_usec"`
		ZotRestarts               int    `json:"zot_restarts"`
		ZotProbeStatus            int    `json:"zot_probe_status"`
		RustFSProbeStatus         int    `json:"rustfs_probe_status"`
		ZotBindRaceDetected       bool   `json:"zot_bind_race_detected"`
		ManualCacheStartOrRestart bool   `json:"manual_cache_start_or_restart"`
	} `json:"accepted_boot"`
	Zot struct {
		BinarySHA256            string  `json:"binary_sha256"`
		ConfigSHA256            string  `json:"config_sha256"`
		CredentialState         string  `json:"credential_state"`
		BcryptCost              int     `json:"bcrypt_cost"`
		AnonymousHTTP           int     `json:"anonymous_http"`
		SystemdSecurityExposure float64 `json:"systemd_security_exposure"`
		JournalSecretMatch      bool    `json:"journal_secret_match"`
	} `json:"zot"`
	PostBootAuthorization struct {
		TrustedOCICRUD                   bool `json:"trusted_oci_crud"`
		TrustedCrossNamespaceWriteHTTP   int  `json:"trusted_cross_namespace_write_http"`
		UntrustedOCICRUD                 bool `json:"untrusted_oci_crud"`
		UntrustedCrossNamespaceWriteHTTP int  `json:"untrusted_cross_namespace_write_http"`
		PromoterOCICRUD                  bool `json:"promoter_oci_crud"`
		PromoterCrossNamespaceWriteHTTP  int  `json:"promoter_cross_namespace_write_http"`
		ReleasePromotedRead              bool `json:"release_promoted_read"`
		ReleasePromotedWriteHTTP         int  `json:"release_promoted_write_http"`
		AnonymousHTTP                    int  `json:"anonymous_http"`
		TemporaryManifestsDeleted        bool `json:"temporary_manifests_deleted"`
		CacheServiceRestarted            bool `json:"cache_service_restarted"`
	} `json:"post_boot_authorization"`
	AuditTool struct {
		ExpectedSmokeScriptSHA256 string `json:"expected_smoke_script_sha256"`
		ObservedAtBootSHA256      string `json:"observed_at_boot_sha256"`
		ReconciledAfterBoot       bool   `json:"reconciled_after_boot"`
		CacheServiceRestarted     bool   `json:"cache_service_restarted"`
	} `json:"audit_tool"`
	Fleet struct {
		ObserverCapturedAt            string `json:"observer_captured_at"`
		ObserverHealthy               bool   `json:"observer_healthy"`
		CollectionErrors              int    `json:"collection_errors"`
		VisibleInstances              int    `json:"visible_instances"`
		OrphanInstances               int    `json:"orphan_instances"`
		MissingInstances              int    `json:"missing_instances"`
		ProviderLeases                int    `json:"provider_leases"`
		DiagnosticSourceBundles       int    `json:"diagnostic_source_bundles"`
		DiagnosticExportedBundles     int    `json:"diagnostic_exported_bundles"`
		DiagnosticPendingBundles      int    `json:"diagnostic_pending_bundles"`
		DiagnosticConsecutiveFailures int    `json:"diagnostic_consecutive_failures"`
		FailedSystemdUnits            int    `json:"failed_systemd_units"`
		RootFreePercent               int    `json:"root_free_percent"`
	} `json:"fleet"`
	RetainedServices struct {
		LegacyRunnerUnits          int `json:"legacy_runner_units"`
		LegacyListeners            int `json:"legacy_listeners"`
		LegacyWorkersAtCapture     int `json:"legacy_workers_at_capture"`
		WorkersSignaledDuringDrain int `json:"workers_signaled_during_drain"`
		JobsCancelledDuringDrain   int `json:"jobs_cancelled_during_drain"`
		ExamplePlatformHTTP        int `json:"example-platform_http"`
		CaptchaHTTP                int `json:"captcha_http"`
		RunningOrHealthyContainers int `json:"running_or_healthy_containers"`
	} `json:"retained_services"`
}

func TestRepositoryManifestIsValidPinnedAndIndependentlyGated(t *testing.T) {
	t.Parallel()

	manifest, err := Load(filepath.Join("..", "..", "config", "cache-artifacts.yaml"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if manifest.RustFS.Version != "1.0.0-rc.1" || manifest.RustFS.ProductionPromotionAllowed {
		t.Fatalf("unexpected RustFS gate: %#v", manifest.RustFS)
	}
	if manifest.RustFS.DeploymentStage != "canary-only" {
		t.Fatalf("unexpected RustFS stage: %q", manifest.RustFS.DeploymentStage)
	}
	if manifest.OCIRegistry.Version != "v2.1.20" || manifest.OCIRegistry.BuildProfile != "minimal" || manifest.OCIRegistry.ExtensionsEnabled {
		t.Fatalf("unexpected OCI contract: %#v", manifest.OCIRegistry)
	}
	if manifest.OCIRegistry.DeploymentStage != "production" || !manifest.OCIRegistry.ProductionPromotionAllowed {
		t.Fatalf("unexpected OCI promotion gate: %#v", manifest.OCIRegistry)
	}
	if manifest.OCIRegistry.ReproducibleBuild.Toolchain != "go1.26.5" ||
		manifest.OCIRegistry.ReproducibleBuild.IndependentBuilds != 2 ||
		manifest.OCIRegistry.ReproducibleBuild.OutputSHA256 != manifest.OCIRegistry.Binary.SHA256 {
		t.Fatalf("unexpected OCI reproducibility contract: %#v", manifest.OCIRegistry.ReproducibleBuild)
	}
	fingerprint, err := manifest.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fingerprint, "sha256:") || len(fingerprint) != len("sha256:")+64 {
		t.Fatalf("unexpected fingerprint %q", fingerprint)
	}
}

func TestRepositoryZotReproducibilityEvidenceMatchesManifest(t *testing.T) {
	t.Parallel()

	configDirectory := filepath.Join("..", "..", "config")
	manifest, err := Load(filepath.Join(configDirectory, "cache-artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	build := manifest.OCIRegistry.ReproducibleBuild
	evidenceData, err := os.ReadFile(filepath.Join(configDirectory, build.EvidenceFile))
	if err != nil {
		t.Fatal(err)
	}
	evidenceDigest := sha256.Sum256(evidenceData)
	if got := hex.EncodeToString(evidenceDigest[:]); got != build.EvidenceSHA256 {
		t.Fatalf("evidence digest mismatch: got %s, want %s", got, build.EvidenceSHA256)
	}

	decoder := json.NewDecoder(bytes.NewReader(evidenceData))
	decoder.DisallowUnknownFields()
	var evidence zotReproducibilityEvidence
	if err := decoder.Decode(&evidence); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("evidence must contain one JSON document, got %v", err)
	}
	if _, err := time.Parse(time.DateOnly, evidence.ObservedAt); err != nil {
		t.Fatalf("invalid observation date: %v", err)
	}
	if evidence.SchemaVersion != 1 ||
		evidence.IndependenceScope != "separate-source-checkout-module-cache-and-build-cache-on-same-host" ||
		evidence.Repository != manifest.OCIRegistry.Repository ||
		evidence.Version != manifest.OCIRegistry.Version ||
		evidence.SourceCommit != manifest.OCIRegistry.SourceCommit ||
		evidence.CommitDescription != build.CommitDescription ||
		evidence.GitHubCommitVerification != "verified" || evidence.GitHubArtifactAttestationFound ||
		evidence.Toolchain != build.Toolchain || evidence.ReleaseAsset.Name != manifest.OCIRegistry.Binary.Name ||
		evidence.ReleaseAsset.SHA256 != manifest.OCIRegistry.Binary.SHA256 {
		t.Fatalf("evidence identity does not match manifest: %#v", evidence)
	}
	if evidence.Build.CGOEnabled != build.CGOEnabled || evidence.Build.GoExperiment != build.GoExperiment ||
		evidence.Build.GOAMD64 != build.GOAMD64 || evidence.Build.BuildMode != build.BuildMode || !evidence.Build.Trimpath {
		t.Fatalf("evidence build contract does not match manifest: %#v", evidence.Build)
	}
	if len(evidence.Runs) != build.IndependentBuilds {
		t.Fatalf("evidence has %d runs, want %d", len(evidence.Runs), build.IndependentBuilds)
	}
	seenBuildIDs := make(map[string]struct{}, len(evidence.Runs))
	for _, run := range evidence.Runs {
		if _, exists := seenBuildIDs[run.BuildID]; run.BuildID == "" || exists {
			t.Fatalf("evidence build IDs must be non-empty and unique: %q", run.BuildID)
		}
		seenBuildIDs[run.BuildID] = struct{}{}
		if !run.FreshSourceCheckout || !run.IsolatedModuleCache || !run.IsolatedBuildCache ||
			!run.ModuleVerification || run.VCSModified || !run.ReleaseAssetMatch || run.OutputSHA256 != build.OutputSHA256 {
			t.Fatalf("invalid reproducibility run: %#v", run)
		}
	}
}

func TestRepositoryZotStorageAuditEvidenceMatchesManifestAndScript(t *testing.T) {
	t.Parallel()

	configDirectory := filepath.Join("..", "..", "config")
	manifest, err := Load(filepath.Join(configDirectory, "cache-artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(configDirectory, manifest.OCIRegistry.RuntimeEvidence.StorageAuditFile)
	evidenceData, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	evidenceDigest := sha256.Sum256(evidenceData)
	if got := hex.EncodeToString(evidenceDigest[:]); got != manifest.OCIRegistry.RuntimeEvidence.StorageAuditSHA256 {
		t.Fatalf("storage evidence digest mismatch: got %s", got)
	}
	decoder := json.NewDecoder(bytes.NewReader(evidenceData))
	decoder.DisallowUnknownFields()
	var evidence zotStorageAuditEvidence
	if err := decoder.Decode(&evidence); err != nil {
		t.Fatalf("decode storage evidence: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("storage evidence must contain one JSON document, got %v", err)
	}
	if _, err := time.Parse(time.DateOnly, evidence.ObservedAt); err != nil {
		t.Fatalf("invalid storage evidence date: %v", err)
	}
	scriptData, err := os.ReadFile(filepath.Join("..", "..", "scripts", "zot-storage-audit.sh"))
	if err != nil {
		t.Fatal(err)
	}
	scriptDigest := sha256.Sum256(scriptData)
	if hex.EncodeToString(scriptDigest[:]) != evidence.AuditScriptSHA256 {
		t.Fatal("storage evidence does not bind the reviewed audit script")
	}
	if evidence.SchemaVersion != 1 || evidence.ZotSHA256 != manifest.OCIRegistry.Binary.SHA256 ||
		!shaPattern.MatchString(evidence.SourceImageFingerprint) ||
		evidence.Execution.IncusProject != "default" || evidence.Execution.VMName != "nddev-zot-storage-audit" ||
		evidence.Execution.VCPUs != 2 || evidence.Execution.Memory != "4GiB" || evidence.Execution.NetworkDevices != 0 ||
		!evidence.Execution.DisposableVMDestroyed || !evidence.Execution.TemporaryImageCopyRemoved ||
		evidence.Execution.LiveCacheTargeted || evidence.Execution.RetainedTenantsTargeted {
		t.Fatalf("unsafe or mismatched storage evidence identity: %#v", evidence)
	}
	if evidence.StorageBoundary.Type != "dedicated-loopback-ext4" || evidence.StorageBoundary.ImageSizeMiB != 256 ||
		!slices.Equal(evidence.StorageBoundary.MountOptions, []string{"noatime", "nodev", "noexec", "nosuid"}) {
		t.Fatalf("unexpected storage boundary: %#v", evidence.StorageBoundary)
	}
	if !evidence.GC.ServerStoppedForOfflineGC || !evidence.GC.RetainedBlobPreserved ||
		!evidence.GC.OrphanBlobRemoved || !evidence.GC.RetainedManifestReadableAfterGC ||
		!evidence.FullDisk.WriteRejected || evidence.FullDisk.HTTPStatus < 400 || evidence.FullDisk.HTTPStatus > 599 ||
		!evidence.FullDisk.ServiceRemainedActive || !evidence.FullDisk.RetainedContentReadable ||
		!evidence.FullDisk.PostReclaimWriteSucceeded || !evidence.FilesystemCheckClean ||
		!evidence.FleetObserverHealthyAfterCleanup || evidence.GHAFleetInstancesAfterCleanup != 0 {
		t.Fatalf("storage evidence did not pass every gate: %#v", evidence)
	}
}

func TestRepositoryZotAuthorizationAuditEvidenceMatchesLiveContracts(t *testing.T) {
	t.Parallel()

	configDirectory := filepath.Join("..", "..", "config")
	manifest, err := Load(filepath.Join(configDirectory, "cache-artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEvidence := manifest.OCIRegistry.RuntimeEvidence
	evidenceData, err := os.ReadFile(filepath.Join(configDirectory, runtimeEvidence.AuthorizationAuditFile))
	if err != nil {
		t.Fatal(err)
	}
	evidenceDigest := sha256.Sum256(evidenceData)
	if got := hex.EncodeToString(evidenceDigest[:]); got != runtimeEvidence.AuthorizationAuditSHA256 {
		t.Fatalf("authorization evidence digest mismatch: got %s", got)
	}
	decoder := json.NewDecoder(bytes.NewReader(evidenceData))
	decoder.DisallowUnknownFields()
	var evidence zotAuthorizationAuditEvidence
	if err := decoder.Decode(&evidence); err != nil {
		t.Fatalf("decode authorization evidence: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("authorization evidence must contain one JSON document, got %v", err)
	}
	if _, err := time.Parse(time.RFC3339, evidence.ObservedAt); err != nil {
		t.Fatalf("invalid authorization evidence timestamp: %v", err)
	}
	if evidence.ObservedAt != evidence.VMResult.ObservedAt || evidence.SchemaVersion != 1 ||
		evidence.Controller.Commit != "7cfdc3d273334c512e0ef0cebb51e25f5d354a3e" ||
		!commitPattern.MatchString(evidence.Controller.Commit) || !shaPattern.MatchString(evidence.Controller.SHA256) ||
		evidence.ZotSHA256 != manifest.OCIRegistry.Binary.SHA256 || evidence.CredentialState != "managed" {
		t.Fatalf("authorization evidence identity mismatch: %#v", evidence)
	}
	for path, expected := range map[string]string{
		filepath.Join("..", "..", "deploy", "fleet-host", "zot.json"): evidence.ConfigSHA256,
		filepath.Join("..", "..", "scripts", "zot-smoke.sh"):          evidence.SmokeScriptSHA256,
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents)
		if got := hex.EncodeToString(digest[:]); got != expected {
			t.Fatalf("authorization evidence does not bind %s: got %s", path, got)
		}
	}
	vm := evidence.VMResult
	if vm.SchemaVersion != 1 || vm.SourceImageFingerprint != "2c30f163eaf4ba6df14148359c3030a1aeca5627e1677637fc98fef9f3cc0b18" ||
		vm.VM.Project != "default" || vm.VM.Name != "nddev-zot-authz-test" || vm.VM.VCPUs != 1 ||
		vm.VM.Memory != "1536MiB" || vm.VM.Network != "gha0" || vm.VM.RawQEMU != "-cpu host,-vmx,-svm" ||
		!vm.Authz.TrustedOCICRUD || !vm.Authz.UntrustedOCICRUD || !vm.Authz.PromoterOCICRUD ||
		!vm.Authz.ReleasePromotedRead || vm.Authz.ReleasePromotedWriteHTTP != 403 ||
		vm.Authz.AllCrossNamespaceReadsHTTP != 403 || vm.Authz.AnonymousHTTP != 401 || vm.Network.HostSSHReachable {
		t.Fatalf("authorization VM gate did not pass: %#v", vm)
	}
	if !evidence.Cleanup.DisposableVMDestroyed || !evidence.Cleanup.TemporaryImageRemoved ||
		!evidence.Cleanup.TemporaryVolumeRemoved || evidence.Cleanup.GHAFleetInstancesAfterCleanup != 0 ||
		!evidence.Service.Active || evidence.Service.AnonymousHTTP != 401 ||
		evidence.Service.SystemdSecurityExposure != 2.7 || evidence.Service.JournalSecretMatch ||
		evidence.Diagnostics.SourceBundles != 23 || evidence.Diagnostics.ExportedBundles != 23 ||
		evidence.Diagnostics.PendingBundles != 0 || evidence.Diagnostics.ConsecutiveFailures != 0 ||
		evidence.RetainedServices.ExamplePlatformHTTP != 200 || evidence.RetainedServices.CaptchaHTTP != 200 ||
		!evidence.FaultControls.ReadinessRollbackExercised ||
		!evidence.FaultControls.RollbackRestoredBootstrapState ||
		!evidence.FaultControls.LoadCircuitBreakerExercised {
		t.Fatalf("authorization cleanup or retained-service gate did not pass: %#v", evidence)
	}
}

func TestRepositoryZotRebootAuditEvidenceMatchesProductionContract(t *testing.T) {
	t.Parallel()

	configDirectory := filepath.Join("..", "..", "config")
	manifest, err := Load(filepath.Join(configDirectory, "cache-artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEvidence := manifest.OCIRegistry.RuntimeEvidence
	evidenceData, err := os.ReadFile(filepath.Join(configDirectory, runtimeEvidence.RebootAuditFile))
	if err != nil {
		t.Fatal(err)
	}
	evidenceDigest := sha256.Sum256(evidenceData)
	if got := hex.EncodeToString(evidenceDigest[:]); got != runtimeEvidence.RebootAuditSHA256 {
		t.Fatalf("reboot evidence digest mismatch: got %s", got)
	}
	decoder := json.NewDecoder(bytes.NewReader(evidenceData))
	decoder.DisallowUnknownFields()
	var evidence zotRebootAuditEvidence
	if err := decoder.Decode(&evidence); err != nil {
		t.Fatalf("decode reboot evidence: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("reboot evidence must contain one JSON document, got %v", err)
	}
	for name, value := range map[string]string{
		"observed_at":                       evidence.ObservedAt,
		"accepted_boot.started_at":          evidence.AcceptedBoot.StartedAt,
		"fleet.observer_captured_at":        evidence.Fleet.ObserverCapturedAt,
		"previous_boot.zot_bind_attempt_at": evidence.PreviousBoot.ZotBindAttemptAt,
		"previous_boot.gha0_created_at":     evidence.PreviousBoot.GHA0CreatedAt,
		"previous_boot.incus_active_at":     evidence.PreviousBoot.IncusActiveAt,
	} {
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			t.Fatalf("invalid %s timestamp: %v", name, err)
		}
	}
	if evidence.SchemaVersion != 1 || // The record was gathered on the host that has since left the fleet.
		// It says so, and it must keep saying so.
		evidence.Host != "server-example-legacy" ||
		evidence.RepositoryCommit != "547f3cab399fb9bc107f107a6bb1d1f387a64f99" ||
		!commitPattern.MatchString(evidence.RepositoryCommit) ||
		evidence.PreviousBoot.BootID != "d00fa803-a30a-4ff6-a2e9-c0bf55ef3a14" ||
		evidence.PreviousBoot.GateAccepted || evidence.PreviousBoot.ZotExitStatus != 0 {
		t.Fatalf("reboot evidence identity or rejected-gate record mismatch: %#v", evidence)
	}
	bindAt, _ := time.Parse(time.RFC3339Nano, evidence.PreviousBoot.ZotBindAttemptAt)
	bridgeAt, _ := time.Parse(time.RFC3339Nano, evidence.PreviousBoot.GHA0CreatedAt)
	incusAt, _ := time.Parse(time.RFC3339Nano, evidence.PreviousBoot.IncusActiveAt)
	if !bindAt.Before(bridgeAt) || !bridgeAt.Before(incusAt) {
		t.Fatal("rejected boot does not prove that Zot raced the Incus bridge")
	}
	contract := evidence.CorrectiveContract
	for path, expected := range map[string]string{
		filepath.Join("..", "..", "scripts", "cache-network-ready.sh"):          contract.NetworkProbeSHA256,
		filepath.Join("..", "..", "deploy", "fleet-host", "gha-zot.service"):    contract.ZotUnitSHA256,
		filepath.Join("..", "..", "deploy", "fleet-host", "gha-rustfs.service"): contract.RustFSUnitSHA256,
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(contents)
		if got := hex.EncodeToString(digest[:]); got != expected {
			t.Fatalf("reboot evidence does not bind %s: got %s", path, got)
		}
	}
	if !contract.RequiresIncus || contract.Interface != "gha0" || contract.ProbeTimeoutSeconds != 120 ||
		contract.SystemdTimeoutSeconds != 150 || contract.RestartPolicy != "always" {
		t.Fatalf("unsafe corrective contract: %#v", contract)
	}
	boot := evidence.AcceptedBoot
	if boot.BootID != "3a3ed9f4-6a6b-4732-9d27-d22afce1442a" || boot.BootID == evidence.PreviousBoot.BootID ||
		boot.SystemState != "running" || boot.IncusActiveMonotonicUSec <= 0 ||
		boot.ZotActiveMonotonicUSec <= boot.IncusActiveMonotonicUSec ||
		boot.RustFSActiveMonotonicUSec <= boot.IncusActiveMonotonicUSec || boot.ZotRestarts != 0 ||
		boot.ZotProbeStatus != 0 || boot.RustFSProbeStatus != 0 || boot.ZotBindRaceDetected ||
		boot.ManualCacheStartOrRestart {
		t.Fatalf("accepted boot did not pass automatic recovery: %#v", boot)
	}
	configData, err := os.ReadFile(filepath.Join("..", "..", "deploy", "fleet-host", "zot.json"))
	if err != nil {
		t.Fatal(err)
	}
	configDigest := sha256.Sum256(configData)
	if evidence.Zot.BinarySHA256 != manifest.OCIRegistry.Binary.SHA256 ||
		evidence.Zot.ConfigSHA256 != hex.EncodeToString(configDigest[:]) || evidence.Zot.CredentialState != "managed" ||
		evidence.Zot.BcryptCost != 12 || evidence.Zot.AnonymousHTTP != 401 ||
		evidence.Zot.SystemdSecurityExposure != 2.7 || evidence.Zot.JournalSecretMatch {
		t.Fatalf("post-reboot Zot state mismatch: %#v", evidence.Zot)
	}
	authz := evidence.PostBootAuthorization
	if !authz.TrustedOCICRUD || authz.TrustedCrossNamespaceWriteHTTP != 403 ||
		!authz.UntrustedOCICRUD || authz.UntrustedCrossNamespaceWriteHTTP != 403 ||
		!authz.PromoterOCICRUD || authz.PromoterCrossNamespaceWriteHTTP != 403 ||
		!authz.ReleasePromotedRead || authz.ReleasePromotedWriteHTTP != 403 || authz.AnonymousHTTP != 401 ||
		!authz.TemporaryManifestsDeleted || authz.CacheServiceRestarted {
		t.Fatalf("post-reboot authorization gate failed: %#v", authz)
	}
	smokeData, err := os.ReadFile(filepath.Join("..", "..", "scripts", "zot-smoke.sh"))
	if err != nil {
		t.Fatal(err)
	}
	smokeDigest := sha256.Sum256(smokeData)
	if evidence.AuditTool.ExpectedSmokeScriptSHA256 != hex.EncodeToString(smokeDigest[:]) ||
		evidence.AuditTool.ObservedAtBootSHA256 != "6c518d1d622a540a062773f94690e61e0a9779ada7ecc4728d8dbf0f12021fd9" ||
		!evidence.AuditTool.ReconciledAfterBoot || evidence.AuditTool.CacheServiceRestarted {
		t.Fatalf("audit-tool drift was not explicitly reconciled: %#v", evidence.AuditTool)
	}
	fleet := evidence.Fleet
	if !fleet.ObserverHealthy || fleet.CollectionErrors != 0 || fleet.VisibleInstances != 0 ||
		fleet.OrphanInstances != 0 || fleet.MissingInstances != 0 || fleet.ProviderLeases != 0 ||
		fleet.DiagnosticSourceBundles != 23 || fleet.DiagnosticExportedBundles != 23 ||
		fleet.DiagnosticPendingBundles != 0 || fleet.DiagnosticConsecutiveFailures != 0 ||
		fleet.FailedSystemdUnits != 0 || fleet.RootFreePercent < 20 {
		t.Fatalf("fleet did not converge after reboot: %#v", fleet)
	}
	retained := evidence.RetainedServices
	if retained.LegacyRunnerUnits != 12 || retained.LegacyListeners != 12 || retained.LegacyWorkersAtCapture < 0 ||
		retained.WorkersSignaledDuringDrain != 0 || retained.JobsCancelledDuringDrain != 0 ||
		retained.ExamplePlatformHTTP != 200 || retained.CaptchaHTTP != 200 || retained.RunningOrHealthyContainers != 5 {
		t.Fatalf("retained estate did not recover safely: %#v", retained)
	}
	if manifest.OCIRegistry.DeploymentStage != "production" || !manifest.OCIRegistry.ProductionPromotionAllowed ||
		manifest.RustFS.DeploymentStage != "canary-only" || manifest.RustFS.ProductionPromotionAllowed {
		t.Fatal("Zot and RustFS promotion gates are not independent")
	}
}

func TestDecodeRejectsUnknownAndMultipleDocuments(t *testing.T) {
	t.Parallel()

	if _, err := Decode(strings.NewReader("schema_version: 1\nunknown: true\n")); err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
	if _, err := Decode(strings.NewReader("schema_version: 1\n---\nschema_version: 1\n")); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("expected multiple-document error, got %v", err)
	}
}

func TestValidationRejectsMutableOrPromotedInputs(t *testing.T) {
	t.Parallel()

	base, err := Load(filepath.Join("..", "..", "config", "cache-artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{"latest RustFS asset", func(m *Manifest) { m.RustFS.Archive.Name = "rustfs-linux-x86_64-gnu-latest.zip" }, "mutable latest"},
		{"RustFS mirror", func(m *Manifest) { m.RustFS.Archive.URL = "https://example.com/rustfs.zip" }, "rustfs.archive.url"},
		{"wrong RustFS release asset", func(m *Manifest) { m.RustFS.Archive.Name = "rustfs-linux-aarch64-gnu-v1.0.0-rc.1.zip" }, "rustfs.archive.name"},
		{"unverified source identity", func(m *Manifest) { m.OCIRegistry.SourceCommit = "main" }, "source_commit"},
		{"Zot URL userinfo", func(m *Manifest) {
			m.OCIRegistry.Binary.URL = "https://attacker@github.com/project-zot/zot/releases/download/v2.1.20/zot-linux-amd64-minimal"
		}, "oci_registry.binary.url"},
		{"extended registry", func(m *Manifest) { m.OCIRegistry.BuildProfile = "extended" }, "minimal build"},
		{"remote registry storage", func(m *Manifest) { m.OCIRegistry.StorageDriver = "s3" }, "must be filesystem"},
		{"enabled registry extensions", func(m *Manifest) { m.OCIRegistry.ExtensionsEnabled = true }, "must be false"},
		{"wrong reproducible toolchain", func(m *Manifest) { m.OCIRegistry.ReproducibleBuild.Toolchain = "latest" }, "exact Go version"},
		{"one reproducible build", func(m *Manifest) { m.OCIRegistry.ReproducibleBuild.IndependentBuilds = 1 }, "at least two clean builds"},
		{"mismatched reproduced binary", func(m *Manifest) { m.OCIRegistry.ReproducibleBuild.OutputSHA256 = strings.Repeat("a", 64) }, "pinned release binary"},
		{"mutable evidence path", func(m *Manifest) { m.OCIRegistry.ReproducibleBuild.EvidenceFile = "latest.json" }, "must be zot-v2.1.20-reproducibility.json"},
		{"invalid evidence digest", func(m *Manifest) { m.OCIRegistry.ReproducibleBuild.EvidenceSHA256 = "latest" }, "lowercase SHA-256"},
		{"mutable storage evidence", func(m *Manifest) { m.OCIRegistry.RuntimeEvidence.StorageAuditFile = "latest.json" }, "must be zot-v2.1.20-storage-audit.json"},
		{"invalid storage evidence digest", func(m *Manifest) { m.OCIRegistry.RuntimeEvidence.StorageAuditSHA256 = "latest" }, "lowercase SHA-256"},
		{"mutable authorization evidence", func(m *Manifest) { m.OCIRegistry.RuntimeEvidence.AuthorizationAuditFile = "latest.json" }, "must be zot-v2.1.20-authz-audit.json"},
		{"invalid authorization evidence digest", func(m *Manifest) { m.OCIRegistry.RuntimeEvidence.AuthorizationAuditSHA256 = "latest" }, "lowercase SHA-256"},
		{"mutable reboot evidence", func(m *Manifest) { m.OCIRegistry.RuntimeEvidence.RebootAuditFile = "latest.json" }, "must be zot-v2.1.20-reboot-audit.json"},
		{"invalid reboot evidence digest", func(m *Manifest) { m.OCIRegistry.RuntimeEvidence.RebootAuditSHA256 = "latest" }, "lowercase SHA-256"},
		{"premature RustFS promotion", func(m *Manifest) { m.RustFS.ProductionPromotionAllowed = true }, "production-blocked"},
		{"reverted Zot stage", func(m *Manifest) { m.OCIRegistry.DeploymentStage = "canary" }, "must be production"},
		{"disabled Zot promotion", func(m *Manifest) { m.OCIRegistry.ProductionPromotionAllowed = false }, "must be production"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifest := base
			test.mutate(&manifest)
			err := manifest.Validate()
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q validation failure, got %v", test.want, err)
			}
		})
	}
}
