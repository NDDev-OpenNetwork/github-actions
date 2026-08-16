package deploycontract

import (
	"encoding/json"
	"os"
	"testing"
)

func TestDirectJITImageStageEvidenceIsExactAndNonPromoting(t *testing.T) {
	raw, err := os.ReadFile("../../config/direct-jit-image-stage-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	type smoke struct {
		MachineID              string `json:"machine_id"`
		ForbiddenDevices       string `json:"forbidden_devices"`
		HostRoute              string `json:"host_route"`
		MetadataRoute          string `json:"metadata_route"`
		NestedCPUFlags         string `json:"nested_cpu_flags"`
		PublicEgress           string `json:"public_egress"`
		RegistrationState      string `json:"registration_state"`
		SSHListener            string `json:"ssh_listener"`
		SSHServerPackage       string `json:"ssh_server_package"`
		SSHUnits               string `json:"ssh_units"`
		WarmAgent              string `json:"warm_agent"`
		DockerActionBuild      string `json:"docker_action_build"`
		DockerCgroupDriver     string `json:"docker_cgroup_driver"`
		DockerEngineVersion    string `json:"docker_engine_version"`
		DockerNonrootAccess    string `json:"docker_nonroot_access"`
		DockerServiceNetwork   string `json:"docker_service_network"`
		DockerSocket           string `json:"docker_socket"`
		DockerSocketFilesystem string `json:"docker_socket_filesystem"`
		DockerStorageDriver    string `json:"docker_storage_driver"`
		ImageVariant           string `json:"image_variant"`
	}
	type image struct {
		ImmutableAlias        string `json:"immutable_alias"`
		ImageFingerprint      string `json:"image_fingerprint"`
		ImageCreatedAt        string `json:"image_created_at"`
		ImageSizeBytes        int64  `json:"image_size_bytes"`
		ManifestFingerprint   string `json:"manifest_fingerprint"`
		RecipeFingerprint     string `json:"recipe_fingerprint"`
		SmokeFingerprint      string `json:"smoke_fingerprint"`
		PackageManifestSHA256 string `json:"package_manifest_sha256"`
		RunnerVersion         string `json:"runner_version"`
		SccacheVersion        string `json:"sccache_version"`
		Smoke                 smoke  `json:"smoke"`
	}
	var audit struct {
		SchemaVersion             int    `json:"schema_version"`
		Host                      string `json:"host"`
		ImplementationMergeCommit string `json:"implementation_merge_commit"`
		StageTool                 struct {
			Version      string `json:"version"`
			Commit       string `json:"commit"`
			BinarySHA256 string `json:"binary_sha256"`
		} `json:"stage_tool"`
		StageOnly          bool  `json:"stage_only"`
		PromotionPerformed bool  `json:"promotion_performed"`
		Standard           image `json:"standard"`
		Integration        image `json:"integration"`
		Aliases            struct {
			StandardCurrent     string `json:"standard_current"`
			StandardPrevious    string `json:"standard_previous"`
			IntegrationCurrent  string `json:"integration_current"`
			IntegrationPrevious string `json:"integration_previous"`
			Unchanged           bool   `json:"unchanged"`
		} `json:"active_aliases_after_stage"`
		StagePostconditions struct {
			Instances                 int `json:"builder_and_smoke_instances"`
			Orphans                   int `json:"incus_orphans"`
			RootFreePercent           int `json:"root_free_percent"`
			LegacyListeners           int `json:"legacy_listeners"`
			FailedUnits               int `json:"failed_systemd_units"`
			ExamplePlatformHTTPStatus int `json:"example_platform_http_status"`
			CaptchaHTTPStatus         int `json:"captcha_http_status"`
		} `json:"stage_postconditions"`
		Restored struct {
			WarmInstance              string `json:"warm_instance"`
			WarmImageFingerprint      string `json:"warm_image_fingerprint"`
			WarmReady                 bool   `json:"warm_ready"`
			ProviderLeases            int    `json:"provider_leases"`
			ProviderClaims            int    `json:"provider_claims"`
			QueueActive               int    `json:"queue_active"`
			QueueInFlight             int    `json:"queue_in_flight"`
			DiagnosticSourceBundles   int    `json:"diagnostic_source_bundles"`
			DiagnosticExportedBundles int    `json:"diagnostic_exported_bundles"`
			DiagnosticPendingBundles  int    `json:"diagnostic_pending_bundles"`
			GARMActive                bool   `json:"garm_active"`
			GatewayActive             bool   `json:"gateway_active"`
			ObserverActive            bool   `json:"observer_active"`
			RustFSActive              bool   `json:"rustfs_active"`
			ZotActive                 bool   `json:"zot_active"`
			WarmTimerActive           bool   `json:"warm_timer_active"`
		} `json:"restored_production"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.SchemaVersion != 1 || audit.Host != "server-example-legacy" || !audit.StageOnly || audit.PromotionPerformed ||
		audit.ImplementationMergeCommit != "54ae35a64cb1da9871669cdf27069dae0641accc" ||
		audit.StageTool.Version != "v0.1.0" || audit.StageTool.Commit != audit.ImplementationMergeCommit ||
		audit.StageTool.BinarySHA256 != "510c543727b785bfd359bf2e6d82f6bc3deceab663fa10de6e4c8a6c692825bd" {
		t.Fatalf("stage identity drifted: %#v", audit)
	}
	if audit.Standard.ImmutableAlias != "nddev-ubuntu-24.04-amd64-runner-2.336.0-r20260801-b8" ||
		audit.Standard.ImageFingerprint != "c8feffddd19ba89b7a49362d75fb07cc89ba45f77451234e072f6f6f227a3e9d" ||
		audit.Integration.ImmutableAlias != "nddev-ubuntu-24.04-amd64-docker-runner-2.336.0-r20260801-b4" ||
		audit.Integration.ImageFingerprint != "ddd02f9d62c35d9955cd2cd5057c6926375da25177d916fc0f5d054640ac7bcf" {
		t.Fatalf("staged image identity drifted: standard=%#v integration=%#v", audit.Standard, audit.Integration)
	}
	for name, candidate := range map[string]image{"standard": audit.Standard, "integration": audit.Integration} {
		if candidate.ImageCreatedAt == "" || candidate.ImageSizeBytes <= 0 || candidate.ManifestFingerprint == "" ||
			candidate.RecipeFingerprint == "" || candidate.SmokeFingerprint == "" || candidate.PackageManifestSHA256 == "" ||
			candidate.RunnerVersion != "2.336.0" || candidate.SccacheVersion != "v0.17.0" || candidate.Smoke.MachineID == "" ||
			candidate.Smoke.ForbiddenDevices != "absent" || candidate.Smoke.HostRoute != "blocked" ||
			candidate.Smoke.MetadataRoute != "blocked" || candidate.Smoke.NestedCPUFlags != "absent" ||
			candidate.Smoke.PublicEgress != "ok" || candidate.Smoke.RegistrationState != "absent" ||
			candidate.Smoke.SSHListener != "absent" || candidate.Smoke.SSHServerPackage != "absent" ||
			candidate.Smoke.SSHUnits != "masked" || candidate.Smoke.WarmAgent != "ready-unregistered" {
			t.Fatalf("%s smoke evidence is incomplete: %#v", name, candidate)
		}
	}
	if audit.Integration.Smoke.DockerActionBuild != "ok" || audit.Integration.Smoke.DockerCgroupDriver != "systemd" ||
		audit.Integration.Smoke.DockerEngineVersion != "29.1.3" || audit.Integration.Smoke.DockerNonrootAccess != "ok" ||
		audit.Integration.Smoke.DockerServiceNetwork != "ok" || audit.Integration.Smoke.DockerSocket != "vm-local" ||
		audit.Integration.Smoke.DockerSocketFilesystem != "tmpfs:/run" || audit.Integration.Smoke.DockerStorageDriver != "overlay2" ||
		audit.Integration.Smoke.ImageVariant != "integration" {
		t.Fatalf("integration Docker boundary is incomplete: %#v", audit.Integration.Smoke)
	}
	if !audit.Aliases.Unchanged || audit.Aliases.StandardCurrent != "bed3af7a52d9a0de5502e23a8a37a076aa42f6b15b16ba7db095e0ebf453a2ce" ||
		audit.Aliases.StandardPrevious != "d36fc3f425133fd2a5335e48fd8d2e4f859d6ff54675b84c5523d842b93578c" ||
		audit.Aliases.IntegrationCurrent != "8c5ca5c79ba5b24b58fe653460ce4f3950ed3991f0837b46a834b897b525f960" ||
		audit.Aliases.IntegrationPrevious != "88576e9649dc4d73d490f7042eba87209605f7d42fd5144250c263e20d3555cf" {
		t.Fatalf("active aliases moved during stage: %#v", audit.Aliases)
	}
	post := audit.StagePostconditions
	if post.Instances != 0 || post.Orphans != 0 || post.RootFreePercent < 20 || post.LegacyListeners != 12 ||
		post.FailedUnits != 0 || post.ExamplePlatformHTTPStatus != 200 || post.CaptchaHTTPStatus != 200 {
		t.Fatalf("stage postconditions are incomplete: %#v", post)
	}
	restored := audit.Restored
	if restored.WarmInstance != "warm-standard-87a5a533cea6" || !restored.WarmReady ||
		restored.WarmImageFingerprint != audit.Aliases.StandardCurrent || restored.ProviderLeases != 1 || restored.ProviderClaims != 0 ||
		restored.QueueActive != 0 || restored.QueueInFlight != 0 || restored.DiagnosticSourceBundles != 107 ||
		restored.DiagnosticExportedBundles != restored.DiagnosticSourceBundles || restored.DiagnosticPendingBundles != 0 ||
		!restored.GARMActive || !restored.GatewayActive || !restored.ObserverActive || !restored.RustFSActive ||
		!restored.ZotActive || !restored.WarmTimerActive {
		t.Fatalf("production state was not restored exactly: %#v", restored)
	}
}
