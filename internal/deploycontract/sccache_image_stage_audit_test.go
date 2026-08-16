package deploycontract

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSccacheImageStageRuntimeEvidence(t *testing.T) {
	raw, err := os.ReadFile("../../config/sccache-image-stage-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	type smoke struct {
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
		SmokeFixMergeCommit       string `json:"smoke_fix_merge_commit"`
		StageOnly                 bool   `json:"stage_only"`
		PromotionPerformed        bool   `json:"promotion_performed"`
		FirstStandardAttempt      struct {
			ImagePublished     bool   `json:"image_published"`
			SmokeAccepted      bool   `json:"smoke_accepted"`
			FailureReason      string `json:"failure_reason"`
			ActiveAliasesMoved bool   `json:"active_aliases_moved"`
			SmokeCleaned       bool   `json:"smoke_instance_cleaned"`
		} `json:"first_standard_attempt"`
		Standard    image `json:"standard"`
		Integration image `json:"integration"`
		Compiler    struct {
			ArchiveSHA256      string `json:"archive_sha256"`
			BinarySHA256       string `json:"binary_sha256"`
			CredentialsInImage bool   `json:"credentials_in_images"`
		} `json:"compiler_cache"`
		Postconditions struct {
			Instances                 int `json:"builder_and_smoke_instances"`
			Leases                    int `json:"journal_leases"`
			Claims                    int `json:"journal_claims"`
			Orphans                   int `json:"incus_orphans"`
			RootFreePercent           int `json:"root_free_percent"`
			LegacyListeners           int `json:"legacy_listeners"`
			FailedUnits               int `json:"failed_systemd_units"`
			ExamplePlatformHTTPStatus int `json:"example_platform_http_status"`
			CaptchaHTTPStatus         int `json:"captcha_http_status"`
			DiagnosticBundles         int `json:"diagnostic_bundles"`
		} `json:"postconditions"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.SchemaVersion != 1 || audit.Host != "server-example-legacy" || !audit.StageOnly || audit.PromotionPerformed ||
		audit.ImplementationMergeCommit != "95945e401a2cd0f5a1c6bfd4196a4ec8ada5489b" ||
		audit.SmokeFixMergeCommit != "465bf9f3355a3e5a9921f5ab60abe2a3ebefa507" {
		t.Fatalf("stage identity is not exact: %#v", audit)
	}
	if !audit.FirstStandardAttempt.ImagePublished || audit.FirstStandardAttempt.SmokeAccepted ||
		audit.FirstStandardAttempt.ActiveAliasesMoved || !audit.FirstStandardAttempt.SmokeCleaned ||
		audit.FirstStandardAttempt.FailureReason == "" {
		t.Fatalf("fail-closed first attempt is not recorded: %#v", audit.FirstStandardAttempt)
	}
	if audit.Standard.ImmutableAlias != "nddev-ubuntu-24.04-amd64-runner-2.336.0-r20260801-b7" ||
		audit.Standard.ImageFingerprint != "bed3af7a52d9a0de5502e23a8a37a076aa42f6b15b16ba7db095e0ebf453a2ce" ||
		audit.Standard.ImageSizeBytes <= 0 || audit.Standard.RunnerVersion != "2.336.0" || audit.Standard.SccacheVersion != "v0.17.0" {
		t.Fatalf("standard image evidence drifted: %#v", audit.Standard)
	}
	if audit.Integration.ImmutableAlias != "nddev-ubuntu-24.04-amd64-docker-runner-2.336.0-r20260801-b3" ||
		audit.Integration.ImageFingerprint != "8c5ca5c79ba5b24b58fe653460ce4f3950ed3991f0837b46a834b897b525f960" ||
		audit.Integration.ImageSizeBytes <= 0 || audit.Integration.RunnerVersion != "2.336.0" || audit.Integration.SccacheVersion != "v0.17.0" {
		t.Fatalf("integration image evidence drifted: %#v", audit.Integration)
	}
	for name, image := range map[string]image{"standard": audit.Standard, "integration": audit.Integration} {
		if image.ManifestFingerprint == "" || image.RecipeFingerprint == "" || image.SmokeFingerprint == "" || image.PackageManifestSHA256 == "" ||
			image.Smoke.ForbiddenDevices != "absent" || image.Smoke.HostRoute != "blocked" || image.Smoke.MetadataRoute != "blocked" ||
			image.Smoke.NestedCPUFlags != "absent" || image.Smoke.PublicEgress != "ok" || image.Smoke.RegistrationState != "absent" ||
			image.Smoke.SSHListener != "absent" || image.Smoke.SSHServerPackage != "absent" || image.Smoke.SSHUnits != "masked" ||
			image.Smoke.WarmAgent != "ready-unregistered" {
			t.Fatalf("%s isolation evidence is incomplete: %#v", name, image)
		}
	}
	if audit.Integration.Smoke.DockerActionBuild != "ok" || audit.Integration.Smoke.DockerCgroupDriver != "systemd" ||
		audit.Integration.Smoke.DockerEngineVersion != "29.1.3" || audit.Integration.Smoke.DockerNonrootAccess != "ok" ||
		audit.Integration.Smoke.DockerServiceNetwork != "ok" || audit.Integration.Smoke.DockerSocket != "vm-local" ||
		audit.Integration.Smoke.DockerSocketFilesystem != "tmpfs:/run" || audit.Integration.Smoke.DockerStorageDriver != "overlay2" ||
		audit.Integration.Smoke.ImageVariant != "integration" {
		t.Fatalf("integration Docker boundary is incomplete: %#v", audit.Integration.Smoke)
	}
	if audit.Compiler.ArchiveSHA256 != "67c4a96dd237c1f518f6b36083f270f9976d516f1e57fce891755ea782e50006" ||
		audit.Compiler.BinarySHA256 != "066c5a84c85044c8f48b3ab571ac114293ea717c3d36985db022af8206e21e63" || audit.Compiler.CredentialsInImage {
		t.Fatalf("compiler-cache provenance or secrecy drifted: %#v", audit.Compiler)
	}
	post := audit.Postconditions
	if post.Instances != 0 || post.Leases != 0 || post.Claims != 0 || post.Orphans != 0 || post.RootFreePercent < 20 ||
		post.LegacyListeners != 12 || post.FailedUnits != 0 || post.ExamplePlatformHTTPStatus != 200 || post.CaptchaHTTPStatus != 200 || post.DiagnosticBundles < 64 {
		t.Fatalf("stage postconditions are incomplete: %#v", post)
	}
}
