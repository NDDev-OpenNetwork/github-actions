package imageplan

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/imagemanifest"
)

func loadInputs(t *testing.T) (config.Config, imagemanifest.Manifest) {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "config", "server-gha-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	manifest, err := imagemanifest.Load(filepath.Join("..", "..", "config", "golden-image.yaml"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	return cfg, manifest
}

func TestBuildProducesDeterministicBoundedPlan(t *testing.T) {
	t.Parallel()
	cfg, manifest := loadInputs(t)

	first, err := Build(cfg, manifest, "nddev-linux-standard")
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	second, err := Build(cfg, manifest, "nddev-linux-standard")
	if err != nil {
		t.Fatalf("build second plan: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("plan is not deterministic: %#v != %#v", first, second)
	}
	if !strings.HasPrefix(first.BuilderName, "gha-image-builder-") || first.Project != "gha-fleet" {
		t.Fatalf("unexpected plan: %#v", first)
	}
	if first.InstanceConfig["raw.qemu"] != "-cpu host,-vmx,-svm" || len(first.InstanceConfig) != 1 {
		t.Fatalf("nested virtualization is not disabled: %#v", first.InstanceConfig)
	}
	if first.BuilderDiskGiB != 20 || first.SmokeRootDiskGiB != 30 {
		t.Fatalf("unexpected image/runtime disk policy: %#v", first)
	}
	names := make([]string, 0, len(first.Toolchains))
	for _, toolchain := range first.Toolchains {
		names = append(names, toolchain.Name)
	}
	if !slices.Equal(names, imagemanifest.BakedToolchains()) {
		t.Fatalf("plan toolchains are not the canonical sorted baked set: %v", names)
	}
	if first.CompilerCache.Name != "sccache" || first.CompilerCache.Version != "v0.17.0" ||
		first.CompilerCache.BinarySHA256 != "066c5a84c85044c8f48b3ab571ac114293ea717c3d36985db022af8206e21e63" {
		t.Fatalf("compiler cache artifact did not reach the image plan: %#v", first.CompilerCache)
	}
}

func TestBuildRejectsRunnerDriftAndUnsafeProfile(t *testing.T) {
	t.Parallel()
	cfg, manifest := loadInputs(t)

	manifest.Runner.Version = "v2.335.0"
	manifest.Runner.DownloadURL = "https://github.com/actions/runner/releases/download/v2.335.0/" + manifest.Runner.Archive
	if _, err := Build(cfg, manifest, "nddev-linux-standard"); err == nil || !strings.Contains(err.Error(), "does not match image runner") {
		t.Fatalf("expected runner drift rejection, got %v", err)
	}
	manifest, _ = imagemanifest.Load(filepath.Join("..", "..", "config", "golden-image.yaml"))
	if _, err := Build(cfg, manifest, "nddev-linux-integration"); err == nil || !strings.Contains(err.Error(), "Docker capability") {
		t.Fatalf("expected variant/profile capability rejection, got %v", err)
	}
	manifest, _ = imagemanifest.Load(filepath.Join("..", "..", "config", "golden-image.yaml"))
	for index := range cfg.Pools {
		if cfg.Pools[index].Name == "nddev-linux-standard" {
			cfg.Pools[index].Resources.DiskGiB = manifest.Guest.BuilderDiskGiB
		}
	}
	if _, err := Build(cfg, manifest, "nddev-linux-standard"); err == nil || !strings.Contains(err.Error(), "must be smaller") {
		t.Fatalf("expected oversized builder-disk rejection, got %v", err)
	}
}

func TestBuildProducesPinnedDockerIntegrationPlan(t *testing.T) {
	t.Parallel()
	cfg, err := config.Load(filepath.Join("..", "..", "config", "server-gha-runner-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := imagemanifest.Load(filepath.Join("..", "..", "config", "golden-image-integration.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Build(cfg, manifest, "nddev-linux-integration")
	if err != nil {
		t.Fatalf("build integration plan: %v", err)
	}
	if plan.Variant != "integration" || plan.SmokeRootDiskGiB != 50 || plan.BuilderDiskGiB != 24 {
		t.Fatalf("unexpected integration plan: %#v", plan)
	}
	if plan.DockerActionBaseRef != "nddev/gha-action-base:busybox-1-1.36.1-6ubuntu3.1" {
		t.Fatalf("unexpected local action base: %q", plan.DockerActionBaseRef)
	}
	wanted := "docker.io=29.1.3-0ubuntu3~24.04.2"
	if !slices.Contains(plan.PackageInstallSpecs, wanted) {
		t.Fatalf("integration install specs do not contain %q: %#v", wanted, plan.PackageInstallSpecs)
	}
	if _, err := Build(cfg, manifest, "nddev-linux-standard"); err == nil || !strings.Contains(err.Error(), "Docker capability") {
		t.Fatalf("expected integration manifest on standard profile rejection, got %v", err)
	}
}
