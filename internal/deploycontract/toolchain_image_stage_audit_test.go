package deploycontract

import (
	"path/filepath"
	"regexp"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/imagemanifest"
)

// TestToolchainImageStageAudit checks the stage-only build of the ADR 0030
// images against the manifests that produced them. Stage-only means the
// immutable alias is built and smoked while the current alias stays where it
// was, so the audit must show a fingerprint for the new alias, an unchanged
// current alias and promoted=false for both variants.
func TestToolchainImageStageAudit(t *testing.T) {
	var audit struct {
		SchemaVersion   int               `json:"schema_version"`
		Server          string            `json:"server"`
		StageOnly       bool              `json:"stage_only"`
		BakedToolchains map[string]string `json:"baked_toolchains"`
		RunnerToolCache string            `json:"runner_tool_cache"`
		Standard        stagedImage       `json:"standard"`
		Integration     stagedImage       `json:"integration"`
		Postconditions  struct {
			ObserverHealthy   bool    `json:"observer_healthy"`
			WarmReady         int     `json:"warm_ready"`
			Claims            int     `json:"claims"`
			QueueActive       int     `json:"queue_active"`
			VisibleInstances  int     `json:"visible_instances"`
			OrphanInstances   int     `json:"orphan_instances"`
			MissingInstances  int     `json:"missing_instances"`
			DiagnosticPending int     `json:"diagnostics_pending"`
			PoolDataPercent   float64 `json:"pool_data_percent"`
			LegacyListeners   int     `json:"legacy_listeners"`
			ExamplePlatform           bool    `json:"example_platform_healthy"`
			Captcha           bool    `json:"captcha_healthy"`
			FailedUnits       int     `json:"failed_systemd_units"`
			Registrations     int     `json:"github_runner_registrations"`
		} `json:"postconditions"`
	}
	readJSON(t, "../../config/toolchain-image-stage-audit.json", &audit)

	if audit.SchemaVersion != 1 || audit.Server != "server-example-legacy" || !audit.StageOnly {
		t.Fatalf("invalid stage audit identity: schema=%d server=%q stage_only=%v",
			audit.SchemaVersion, audit.Server, audit.StageOnly)
	}
	if audit.RunnerToolCache != "/home/runner/actions-runner/_work/_tool" {
		t.Fatalf("runner tool cache %q is not where actions/setup-go looks", audit.RunnerToolCache)
	}

	for variant, staged := range map[string]stagedImage{
		"standard": audit.Standard, "integration": audit.Integration,
	} {
		manifestFile := "golden-image.yaml"
		if variant == "integration" {
			manifestFile = "golden-image-integration.yaml"
		}
		manifest, err := imagemanifest.Load(filepath.Join("../..", "config", manifestFile))
		if err != nil {
			t.Fatalf("load %s: %v", manifestFile, err)
		}
		fingerprint, err := manifest.Fingerprint()
		if err != nil {
			t.Fatalf("fingerprint %s: %v", manifestFile, err)
		}
		if staged.ManifestFingerprint != fingerprint {
			t.Errorf("%s image was built from manifest %s, repository now pins %s",
				variant, staged.ManifestFingerprint, fingerprint)
		}
		if staged.Alias != manifest.Image.Alias || staged.CurrentAlias != manifest.Image.CurrentAlias {
			t.Errorf("%s audit aliases %q/%q do not match the manifest %q/%q",
				variant, staged.Alias, staged.CurrentAlias, manifest.Image.Alias, manifest.Image.CurrentAlias)
		}
		// Stage-only must leave the pools on their existing image.
		if staged.Promoted || staged.CurrentUnchanged == "" || staged.CurrentUnchanged == staged.ImageFingerprint {
			t.Errorf("%s was promoted or its current alias moved onto the new image: %+v", variant, staged)
		}
		for field, value := range map[string]string{
			"image_fingerprint": staged.ImageFingerprint,
			"current_unchanged": staged.CurrentUnchanged,
		} {
			if !regexp.MustCompile(`^[0-9a-f]{12,64}$`).MatchString(value) {
				t.Errorf("%s %s is not an Incus fingerprint: %q", variant, field, value)
			}
		}
		for field, value := range map[string]string{
			"recipe_fingerprint": staged.RecipeFingerprint,
			"smoke_fingerprint":  staged.SmokeFingerprint,
		} {
			if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(value) {
				t.Errorf("%s %s is not a full digest: %q", variant, field, value)
			}
		}
		if staged.RecipeFingerprint == staged.SmokeFingerprint {
			t.Errorf("%s recipe and smoke policy must fingerprint independently", variant)
		}

		// The smoke ran inside the published image, so its report is the proof
		// that the baked toolchains and the security boundary both survived.
		if len(staged.Smoke.Toolchains) != len(imagemanifest.BakedToolchains()) {
			t.Errorf("%s smoke reported %d toolchains, expected %d",
				variant, len(staged.Smoke.Toolchains), len(imagemanifest.BakedToolchains()))
		}
		for _, toolchain := range manifest.Toolchains {
			if staged.Smoke.Toolchains[toolchain.Name] != toolchain.Version {
				t.Errorf("%s smoke saw %s %q, manifest pins %q",
					variant, toolchain.Name, staged.Smoke.Toolchains[toolchain.Name], toolchain.Version)
			}
		}
		if staged.Smoke.RunnerToolCache != audit.RunnerToolCache {
			t.Errorf("%s smoke tool cache %q disagrees with the audit", variant, staged.Smoke.RunnerToolCache)
		}
		if staged.Smoke.RunnerVersion != manifest.Runner.Version[1:] ||
			staged.Smoke.SccacheVersion != manifest.CompilerCache.Version {
			t.Errorf("%s smoke runner/sccache versions drifted: %+v", variant, staged.Smoke)
		}
		if staged.Smoke.RegistrationState != "absent" || staged.Smoke.WarmAgent != "ready-unregistered" ||
			staged.Smoke.ForbiddenDevices != "absent" || staged.Smoke.NestedCPUFlags != "absent" ||
			staged.Smoke.HostRoute != "blocked" || staged.Smoke.MetadataRoute != "blocked" ||
			staged.Smoke.SSHServerPackage != "absent" || staged.Smoke.SSHUnits != "masked" ||
			staged.Smoke.SSHListener != "absent" || staged.Smoke.PublicEgress != "ok" {
			t.Errorf("%s smoke did not preserve the worker security boundary: %+v", variant, staged.Smoke)
		}
		if variant == "integration" && (staged.Smoke.ImageVariant != "integration" ||
			staged.Smoke.DockerSocket != "vm-local" || staged.Smoke.DockerNonrootAccess != "ok" ||
			staged.Smoke.DockerActionBuild != "ok" || staged.Smoke.DockerServiceNetwork != "ok") {
			t.Errorf("integration smoke did not prove the VM-local Docker contract: %+v", staged.Smoke)
		}
	}

	post := audit.Postconditions
	if !post.ObserverHealthy || post.WarmReady != 1 || post.Claims != 0 || post.QueueActive != 0 ||
		post.VisibleInstances != 1 || post.OrphanInstances != 0 || post.MissingInstances != 0 ||
		post.DiagnosticPending != 0 || post.LegacyListeners != 12 || !post.ExamplePlatform || !post.Captcha ||
		post.FailedUnits != 0 || post.Registrations != 0 {
		t.Fatalf("the build window did not converge: %+v", post)
	}
	// Two images plus their transient builder and smoke volumes must not push
	// the loop-backed thin pool toward the point where it cannot be recovered.
	if post.PoolDataPercent <= 0 || post.PoolDataPercent >= 85 {
		t.Fatalf("thin pool left at %.2f percent; collect old images before the next build", post.PoolDataPercent)
	}
}

type stagedImage struct {
	Alias                 string `json:"alias"`
	ImageFingerprint      string `json:"image_fingerprint"`
	ManifestFingerprint   string `json:"manifest_fingerprint"`
	CurrentAlias          string `json:"current_alias"`
	CurrentUnchanged      string `json:"current_alias_target_unchanged"`
	Profile               string `json:"profile"`
	RecipeFingerprint     string `json:"recipe_fingerprint"`
	SmokeFingerprint      string `json:"smoke_fingerprint"`
	PackageManifestSHA256 string `json:"package_manifest_sha256"`
	Promoted              bool   `json:"promoted"`
	Smoke                 struct {
		Toolchains           map[string]string `json:"toolchains"`
		RunnerToolCache      string            `json:"runner_tool_cache"`
		RunnerVersion        string            `json:"runner_version"`
		SccacheVersion       string            `json:"sccache_version"`
		RegistrationState    string            `json:"registration_state"`
		WarmAgent            string            `json:"warm_agent"`
		ForbiddenDevices     string            `json:"forbidden_devices"`
		NestedCPUFlags       string            `json:"nested_cpu_flags"`
		HostRoute            string            `json:"host_route"`
		MetadataRoute        string            `json:"metadata_route"`
		PublicEgress         string            `json:"public_egress"`
		SSHServerPackage     string            `json:"ssh_server_package"`
		SSHUnits             string            `json:"ssh_units"`
		SSHListener          string            `json:"ssh_listener"`
		ImageVariant         string            `json:"image_variant"`
		DockerSocket         string            `json:"docker_socket"`
		DockerNonrootAccess  string            `json:"docker_nonroot_access"`
		DockerActionBuild    string            `json:"docker_action_build"`
		DockerServiceNetwork string            `json:"docker_service_network"`
	} `json:"smoke"`
}
