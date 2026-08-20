package imagebuild

import (
	"encoding/base64"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/imagemanifest"
	"github.com/NDDev-OpenNetwork/github-actions/internal/imageplan"
)

func testImagePlan(t *testing.T) imageplan.Plan {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := imagemanifest.Load(filepath.Join("..", "..", "config", "golden-image-container.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := imageplan.Build(cfg, manifest, "nddev-linux-standard")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func testIntegrationImagePlan(t *testing.T) imageplan.Plan {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := imagemanifest.Load(filepath.Join("..", "..", "config", "golden-image-container-integration.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := imageplan.Build(cfg, manifest, "nddev-linux-integration")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func testContainerImagePlan(t *testing.T) imageplan.Plan {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := imagemanifest.Load(filepath.Join("..", "..", "config", "golden-image-container.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := imageplan.Build(cfg, manifest, "nddev-linux-container-canary")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestContainerBuilderUsesRootfsAndNeverRequestsVM(t *testing.T) {
	plan := testContainerImagePlan(t)
	orchestrator := &Orchestrator{}
	args := orchestrator.instanceInitArgs(plan, strings.Repeat("a", 64), "container-builder", 12)
	if slices.Contains(args, "--vm") {
		t.Fatalf("container builder requested VM mode: %v", args)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{
		"--config security.privileged=false",
		"--config security.nesting=false",
		"--config security.syscalls.intercept.mknod=false",
		"--config security.syscalls.intercept.setxattr=false",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("container builder args missing %q: %v", required, args)
		}
	}
	artifacts := Artifacts{
		Directory: "/var/tmp/gha-fleet-image-test", Checksums: "/var/tmp/gha-fleet-image-test/SHA256SUMS",
		Signature: "/var/tmp/gha-fleet-image-test/SHA256SUMS.gpg", Metadata: "/var/tmp/gha-fleet-image-test/" + plan.Source.MetadataFile,
		Rootfs: "/var/tmp/gha-fleet-image-test/" + plan.Source.RootfsFile, Runner: "/var/tmp/gha-fleet-image-test/" + plan.Runner.Archive,
		CompilerCache: "/var/tmp/gha-fleet-image-test/" + plan.CompilerCache.Archive, Toolchains: map[string]string{}, VerifiedBy: plan.Source.SignerFingerprint,
	}
	for _, toolchain := range plan.Toolchains {
		artifacts.Toolchains[toolchain.Name] = artifacts.Directory + "/" + toolchain.Archive
	}
	if err := validateArtifactPaths(plan, artifacts); err != nil {
		t.Fatal(err)
	}
}

func TestRecipeFingerprintIsDeterministic(t *testing.T) {
	t.Parallel()
	plan := testImagePlan(t)
	first, err := RecipeFingerprint(plan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RecipeFingerprint(plan)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, "sha256:") || len(first) != 71 {
		t.Fatalf("unexpected recipe fingerprints %q %q", first, second)
	}
	if first != "sha256:d073d0120ae8d4c7c3920c3ba008c4af89c31acc906d4b0c24fef9a55252b295" {
		t.Fatalf("deployed standard recipe fingerprint drifted: %q", first)
	}
	smoke, err := SmokeFingerprint(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(smoke, "sha256:") || len(smoke) != 71 || smoke == first {
		t.Fatalf("unexpected smoke fingerprint %q for recipe %q", smoke, first)
	}
	plan.SmokeRootDiskGiB++
	unchangedRecipe, err := RecipeFingerprint(plan)
	if err != nil {
		t.Fatal(err)
	}
	changedSmoke, err := SmokeFingerprint(plan)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedRecipe != first || changedSmoke == smoke {
		t.Fatalf("runtime policy must change only smoke fingerprint")
	}
	integration := testIntegrationImagePlan(t)
	integrationRecipe, err := RecipeFingerprint(integration)
	if err != nil {
		t.Fatal(err)
	}
	integrationSmoke, err := SmokeFingerprint(integration)
	if err != nil {
		t.Fatal(err)
	}
	if integrationRecipe == first || integrationSmoke == smoke || integrationRecipe == integrationSmoke {
		t.Fatal("integration recipe and smoke policy need independent fingerprints")
	}
}

func TestVerifyTargetImageRequiresExplicitVariantForEveryWorkerClass(t *testing.T) {
	t.Parallel()

	for _, plan := range []imageplan.Plan{testImagePlan(t), testIntegrationImagePlan(t)} {
		recipe, err := RecipeFingerprint(plan)
		if err != nil {
			t.Fatal(err)
		}
		artifactSHA256 := plan.Source.DiskSHA256
		if plan.Image.EffectiveType() == "container" {
			artifactSHA256 = plan.Source.RootfsSHA256
		}
		properties := map[string]string{
			"user.nddev.manifest_fingerprint":    plan.ManifestFingerprint,
			"user.nddev.recipe_sha256":           strings.TrimPrefix(recipe, "sha256:"),
			"user.nddev.runner.version":          plan.Runner.Version,
			"user.nddev.runner.sha256":           plan.Runner.SHA256,
			"user.nddev.sccache.version":         plan.CompilerCache.Version,
			"user.nddev.sccache.archive_sha256":  plan.CompilerCache.ArchiveSHA256,
			"user.nddev.sccache.binary_sha256":   plan.CompilerCache.BinarySHA256,
			"user.nddev.source.release":          plan.Source.ReleaseID,
			"user.nddev.source.artifact_sha256":  artifactSHA256,
			"user.nddev.package_manifest_sha256": strings.Repeat("a", 64),
			"user.nddev.image.variant":           plan.Variant,
			"user.nddev.docker-action-base":      plan.DockerActionBaseRef,
		}
		maps.Copy(properties, toolchainProperties(plan))
		image := imageState{
			Architecture: plan.Image.Architecture,
			Type:         plan.Image.EffectiveType(),
			Properties:   properties,
		}
		if err := verifyTargetImage(image, plan, recipe); err != nil {
			t.Fatalf("%s image with explicit variant was rejected: %v", plan.Variant, err)
		}
		delete(image.Properties, "user.nddev.image.variant")
		if err := verifyTargetImage(image, plan, recipe); err == nil || !strings.Contains(err.Error(), "user.nddev.image.variant") {
			t.Fatalf("%s image without explicit variant was accepted: %v", plan.Variant, err)
		}
	}
}

func TestStageOnlyApplyOptionDefaultsToPromotionSafety(t *testing.T) {
	t.Parallel()

	if (ApplyOptions{}).StageOnly {
		t.Fatal("zero-value apply options unexpectedly suppress promotion")
	}
	if !(ApplyOptions{StageOnly: true}).StageOnly {
		t.Fatal("explicit stage-only option was lost")
	}
}

func TestEmbeddedScriptsPreserveSecurityBoundary(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"assets/provision.sh",
		"assets/docker-provision.sh",
		"assets/docker-seal.sh",
		"assets/sanitize.sh",
		"assets/smoke.sh",
		"assets/smoke-integration.sh",
	} {
		content, err := scripts.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "config.sh --") || strings.Contains(string(content), "/var/run/docker.sock:") {
			t.Fatalf("%s embeds registration or socket passthrough", name)
		}
		if strings.Contains(name, "docker-") || name == "assets/smoke-integration.sh" {
			if !strings.Contains(string(content), "failed at line") || !strings.Contains(string(content), "${LINENO:-0}") {
				t.Fatalf("%s lacks actionable ERR diagnostics", name)
			}
		}
	}
	sanitize, _ := scripts.ReadFile("assets/sanitize.sh")
	for _, invariant := range []string{"/etc/machine-id", ".credentials_rsaparams", "/var/run/docker.sock", "/dev/kvm", "sanitize failed at line"} {
		if !strings.Contains(string(sanitize), invariant) {
			t.Fatalf("sanitize script misses %s", invariant)
		}
	}
	for _, accountInvariant := range []string{"install -o ubuntu -g mail", "userdel --remove ubuntu", "getent passwd ubuntu"} {
		if !strings.Contains(string(sanitize), accountInvariant) {
			t.Fatalf("sanitize script misses account-removal invariant %s", accountInvariant)
		}
	}
	provision, _ := scripts.ReadFile("assets/provision.sh")
	for _, sshInvariant := range []string{
		"apt-get purge -y openssh-server",
		"for unit in ssh.service ssh.socket sshd.service sshd.socket",
		`systemctl stop "${unit}"`,
		`systemctl disable "${unit}"`,
		`systemctl mask "${unit}"`,
	} {
		if !strings.Contains(string(provision), sshInvariant) {
			t.Fatalf("provision script misses SSH-removal invariant %s", sshInvariant)
		}
	}
	provisionText := string(provision)
	for _, warmupInvariant := range []string{
		"/home/runner/actions-runner/bin/Runner.Listener warmup",
		"runuser --user runner",
		"registration state appeared while preparing the warm filesystem",
		"ready-unregistered-v1",
	} {
		if !strings.Contains(provisionText, warmupInvariant) {
			t.Fatalf("provision script misses official-runner warmup invariant %s", warmupInvariant)
		}
	}
	warmupIndex := strings.Index(provisionText, "/home/runner/actions-runner/bin/Runner.Listener warmup")
	readyIndex := strings.Index(provisionText, `printf "ready-unregistered-v1\n"`)
	if warmupIndex < 0 || readyIndex <= warmupIndex || strings.Count(provisionText[warmupIndex:readyIndex], ".credentials_rsaparams") != 1 {
		t.Fatal("warm readiness must be published only after official warmup and a second registration-state check")
	}
	installerIndex := strings.Index(provisionText, `"${version_root}/bin/installdependencies.sh"`)
	finalStopIndex := strings.Index(provisionText, `systemctl stop "${unit}"`)
	purgeIndex := strings.Index(provisionText, "apt-get purge -y openssh-server")
	if installerIndex < 0 || purgeIndex <= installerIndex || finalStopIndex <= purgeIndex {
		t.Fatal("SSH purge and final stop must run after every package installer")
	}
	if strings.Contains(provisionText, "systemctl disable --now ssh.service ssh.socket sshd.service sshd.socket") {
		t.Fatal("grouped SSH disable can abort before stopping an active unit whose file was purged")
	}
	for _, cacheInvariant := range []string{
		"GHA_SCCACHE_ARCHIVE_SHA256", "GHA_SCCACHE_BINARY_SHA256",
		"sccache archive has an unexpected entry count", "--no-same-owner", "--no-same-permissions",
		"/usr/local/bin/sccache", `sccache --version`,
	} {
		if !strings.Contains(provisionText, cacheInvariant) {
			t.Fatalf("provision script misses compiler-cache invariant %s", cacheInvariant)
		}
	}
	for _, name := range []string{"assets/sanitize.sh", "assets/smoke.sh"} {
		content, _ := scripts.ReadFile(name)
		for _, sshInvariant := range []string{"dpkg -s openssh-server", "ssh.socket", "sport = :22"} {
			if !strings.Contains(string(content), sshInvariant) {
				t.Fatalf("%s misses SSH runtime invariant %s", name, sshInvariant)
			}
		}
	}
	for _, name := range []string{"assets/smoke.sh", "assets/smoke-integration.sh"} {
		content, _ := scripts.ReadFile(name)
		for _, cacheInvariant := range []string{"GHA_SCCACHE_VERSION", "GHA_SCCACHE_BINARY_SHA256", "sccache --version", "/usr/local/bin/sccache", "sha256sum --check --strict --status"} {
			if !strings.Contains(string(content), cacheInvariant) {
				t.Fatalf("%s misses compiler-cache runtime invariant %s", name, cacheInvariant)
			}
		}
	}
	dockerProvision, _ := scripts.ReadFile("assets/docker-provision.sh")
	for _, invariant := range []string{"overlay2", "overlayfs", "GHA_DOCKER_STORAGE_DRIVER", "native.cgroupdriver=systemd", "docker buildx version", "docker compose version", "docker import", "--network none"} {
		if !strings.Contains(string(dockerProvision), invariant) {
			t.Fatalf("Docker provision script misses %s", invariant)
		}
	}
	dockerSeal, _ := scripts.ReadFile("assets/docker-seal.sh")
	for _, invariant := range []string{"exactly one OCI image", "docker.socket", "ss -H -xl", "Docker Unix listener remained", "rm -f -- /run/docker.sock", "test ! -e /var/run/docker.sock"} {
		if !strings.Contains(string(dockerSeal), invariant) {
			t.Fatalf("Docker seal script misses %s", invariant)
		}
	}
	dockerSealText := string(dockerSeal)
	serviceStop := strings.Index(dockerSealText, "systemctl stop docker.service")
	socketStop := strings.Index(dockerSealText, "systemctl stop docker.socket")
	containerdStop := strings.Index(dockerSealText, "systemctl stop containerd.service")
	listenerProof := strings.Index(dockerSealText, "ss -H -xl")
	staleRemoval := strings.Index(dockerSealText, "rm -f -- /run/docker.sock")
	absenceProof := strings.Index(dockerSealText, "test ! -e /run/docker.sock")
	if serviceStop < 0 || socketStop <= serviceStop || containerdStop <= socketStop || listenerProof <= containerdStop || staleRemoval <= listenerProof || absenceProof <= staleRemoval {
		t.Fatal("Docker seal must stop daemon/socket/containerd, reject a listener, remove only a stale socket, then prove absence")
	}
	integrationSmoke, _ := scripts.ReadFile("assets/smoke-integration.sh")
	for _, invariant := range []string{"docker_nonroot_access", "docker_action_build", "docker_service_network", "socket_mount_target", `== "/run"`, `socket_mount_source}" == "tmpfs`, `socket_mount_fstype}" == "tmpfs`, "docker_socket_filesystem"} {
		if !strings.Contains(string(integrationSmoke), invariant) {
			t.Fatalf("Docker smoke script misses %s", invariant)
		}
	}
}

func TestIntegrationErrorTrapsSurviveNounsetAtTopLevel(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		marker string
	}{
		{"assets/docker-provision.sh", `if [[ "$(id -u)"`},
		{"assets/docker-seal.sh", `if [[ "$(id -u)"`},
		{"assets/smoke-integration.sh", `: "${GHA_RUNNER_VERSION:?}"`},
	} {
		content, err := scripts.ReadFile(test.name)
		if err != nil {
			t.Fatal(err)
		}
		prefix, _, found := strings.Cut(string(content), test.marker)
		if !found {
			t.Fatalf("%s test marker is absent", test.name)
		}
		command := exec.Command("bash", "-ceu", prefix+"\nfalse\n")
		output, err := command.CombinedOutput()
		if err == nil {
			t.Fatalf("%s synthetic failure unexpectedly succeeded", test.name)
		}
		if !strings.Contains(string(output), "failed at line") || strings.Contains(string(output), "unbound variable") {
			t.Fatalf("%s ERR trap output is not actionable: %s", test.name, output)
		}
	}
}

func TestIntegrationSmokeCleanupDoesNotReenterErrorTrap(t *testing.T) {
	t.Parallel()

	content, err := scripts.ReadFile("assets/smoke-integration.sh")
	if err != nil {
		t.Fatal(err)
	}
	prefix, _, found := strings.Cut(string(content), "cloud-init status")
	if !found {
		t.Fatal("integration smoke cleanup test marker is absent")
	}
	command := exec.Command("bash", "-ceu", prefix+"\nexit 0\n")
	command.Env = append(os.Environ(),
		"GHA_RUNNER_VERSION=v2.336.0",
		"GHA_PUBLIC_HOST_ADDRESS=198.51.100.1",
		"GHA_EXPECTED_ROOT_DISK_GIB=70",
		"GHA_DOCKER_ACTION_BASE_REF=example.invalid/action-base:test",
		"GHA_DOCKER_STORAGE_DRIVER=overlay2",
		"GHA_INSTANCE_TYPE=virtual-machine",
		"GHA_SCCACHE_VERSION=v0.17.0",
		"GHA_SCCACHE_BINARY_SHA256="+strings.Repeat("a", 64),
		"GHA_TOOLCHAINS_B64="+base64.StdEncoding.EncodeToString([]byte("[]")),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("successful script exit was converted into a cleanup failure: %v: %s", err, output)
	}
}

func TestDockerStorageDriverMatchesIsolationBackend(t *testing.T) {
	t.Parallel()
	vm := testImagePlan(t)
	vm.Image.Type = "virtual-machine"
	if got := dockerStorageDriver(vm); got != "overlay2" {
		t.Fatalf("VM Docker storage driver = %q", got)
	}
	container := vm
	container.Image.Type = "container"
	if got := dockerStorageDriver(container); got != "overlayfs" {
		t.Fatalf("container Docker storage driver = %q", got)
	}
}

func TestInstanceInitArgsPinContainerIsolation(t *testing.T) {
	t.Parallel()

	plan := testImagePlan(t)
	args := (&Orchestrator{}).instanceInitArgs(plan, strings.Repeat("a", 64), "probe", plan.BuilderDiskGiB)
	for _, want := range []string{"--config security.privileged=false", "--config security.nesting=false"} {
		if !strings.Contains(strings.Join(args, " "), want) {
			t.Fatalf("instance init args %q do not contain %q", args, want)
		}
	}
	if !strings.Contains(strings.Join(args, " "), "--device root,size=12GiB") {
		t.Fatalf("builder init args do not contain bounded root disk: %q", args)
	}
	smokeArgs := (&Orchestrator{}).instanceInitArgs(plan, strings.Repeat("b", 64), "smoke", 0)
	if strings.Contains(strings.Join(smokeArgs, " "), "--device") {
		t.Fatalf("smoke must exercise the production profile disk: %q", smokeArgs)
	}
}

func TestValidateArtifactPathsRejectsSubstitution(t *testing.T) {
	t.Parallel()
	plan := testImagePlan(t)
	directory := "/var/tmp/gha-fleet-image-test"
	artifacts := Artifacts{
		Directory:     directory,
		Checksums:     filepath.Join(directory, plan.Source.ChecksumsFile),
		Signature:     filepath.Join(directory, plan.Source.SignatureFile),
		Metadata:      filepath.Join(directory, plan.Source.MetadataFile),
		Disk:          filepath.Join(directory, plan.Source.DiskFile),
		Rootfs:        filepath.Join(directory, plan.Source.RootfsFile),
		Runner:        filepath.Join(directory, plan.Runner.Archive),
		CompilerCache: filepath.Join(directory, plan.CompilerCache.Archive),
		Toolchains:    make(map[string]string, len(plan.Toolchains)),
		VerifiedBy:    plan.Source.SignerFingerprint,
	}
	for _, toolchain := range plan.Toolchains {
		artifacts.Toolchains[toolchain.Name] = filepath.Join(directory, toolchain.Archive)
	}
	if err := validateArtifactPaths(plan, artifacts); err != nil {
		t.Fatalf("valid artifact set rejected: %v", err)
	}
	artifacts.Runner = "/tmp/substitute.tar.gz"
	if err := validateArtifactPaths(plan, artifacts); err == nil {
		t.Fatal("expected substituted artifact rejection")
	}
	artifacts.Runner = filepath.Join(directory, plan.Runner.Archive)
	artifacts.Toolchains["rust"] = "/tmp/substitute-rust.tar.xz"
	if err := validateArtifactPaths(plan, artifacts); err == nil {
		t.Fatal("expected substituted toolchain artifact rejection")
	}
	artifacts.Toolchains["rust"] = filepath.Join(directory, "rust-1.97.1-x86_64-unknown-linux-gnu.tar.xz")
	delete(artifacts.Toolchains, "bun")
	if err := validateArtifactPaths(plan, artifacts); err == nil {
		t.Fatal("expected missing toolchain artifact rejection")
	}
}

// In a cluster the build claims one member: it verifies that member is empty
// and then pins its own VMs there. Without the pin the placement scriptlet
// would choose, and it would rightly choose whichever member has the most free
// memory -- which is not necessarily the one just verified free.
func TestClusteredImageBuildPinsItsInstancesToTheVerifiedMember(t *testing.T) {
	plan := testImagePlan(t)

	standalone := (&Orchestrator{}).instanceInitArgs(plan, strings.Repeat("a", 64), "probe", plan.BuilderDiskGiB)
	if strings.Contains(strings.Join(standalone, " "), "--target") {
		t.Fatalf("standalone build pinned a cluster member: %q", standalone)
	}

	clustered := (&Orchestrator{clusterMember: "gha-runner-4"}).instanceInitArgs(plan, strings.Repeat("a", 64), "probe", plan.BuilderDiskGiB)
	if !strings.Contains(strings.Join(clustered, " "), "--target gha-runner-4") {
		t.Fatalf("clustered build did not pin its member: %q", clustered)
	}
}
