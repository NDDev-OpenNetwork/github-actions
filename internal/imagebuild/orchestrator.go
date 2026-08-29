package imagebuild

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/imageplan"
)

//go:embed assets/*.sh
var scripts embed.FS

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Result struct {
	Applied               bool           `json:"applied"`
	Built                 bool           `json:"built"`
	Promoted              bool           `json:"promoted"`
	ManifestFingerprint   string         `json:"manifest_fingerprint"`
	RecipeFingerprint     string         `json:"recipe_fingerprint"`
	SmokeFingerprint      string         `json:"smoke_fingerprint"`
	SourceFingerprint     string         `json:"source_fingerprint"`
	ImageFingerprint      string         `json:"image_fingerprint"`
	ImmutableAlias        string         `json:"immutable_alias"`
	CurrentAlias          string         `json:"current_alias"`
	PreviousFingerprint   string         `json:"previous_fingerprint,omitempty"`
	PackageManifestSHA256 string         `json:"package_manifest_sha256"`
	Smoke                 map[string]any `json:"smoke"`
}

type Orchestrator struct {
	Runner CommandRunner

	// PreserveFailedBuilder keeps the builder instance when a build fails,
	// instead of deleting it.
	//
	// The default is to delete, because a failed build must not strand a
	// member. But three integration-image publishes have failed with
	// `websocket: close 1006` and `readdirent ... bad file descriptor`
	// (NDDev-OpenNetwork/github-actions#265), and each attempt deleted the one
	// artifact that could explain them: the rootfs at the path the error names.
	// Two sessions have now reasoned about that failure from the message alone.
	//
	// Nothing is preserved silently -- the caller is told the instance name and
	// the member it is on, because a preserved builder holds disk and a
	// capacity lease until someone removes it.
	PreserveFailedBuilder bool

	// clusterMember is this server's name when it is part of a cluster, and
	// empty when it is standalone. Resolved once, before the first mutation.
	clusterMember string
}

// localClusterMember reports which cluster member this command is running on,
// or an empty string on a standalone server. It caches, because every caller
// below needs the same answer and it cannot change mid-build.
func (o *Orchestrator) localClusterMember(ctx context.Context) (string, error) {
	if o.clusterMember != "" {
		return o.clusterMember, nil
	}
	server, err := queryJSON[struct {
		Environment struct {
			ServerName      string `json:"server_name"`
			ServerClustered bool   `json:"server_clustered"`
		} `json:"environment"`
	}](ctx, o.Runner, "/1.0")
	if err != nil {
		return "", fmt.Errorf("inspect Incus server: %w", err)
	}
	if !server.Environment.ServerClustered {
		return "", nil
	}
	if server.Environment.ServerName == "" {
		return "", fmt.Errorf("clustered Incus did not report its member name")
	}
	o.clusterMember = server.Environment.ServerName
	return o.clusterMember, nil
}

type ApplyOptions struct {
	// StageOnly builds or reuses the immutable alias and completes its smoke
	// test without changing current/previous aliases.
	StageOnly bool
}

type aliasState struct {
	Name        string `json:"name"`
	Target      string `json:"target"`
	Description string `json:"description"`
}

type imageState struct {
	Fingerprint  string            `json:"fingerprint"`
	Architecture string            `json:"architecture"`
	Type         string            `json:"type"`
	Properties   map[string]string `json:"properties"`
}

type serverState struct {
	Auth        string `json:"auth"`
	Environment struct {
		Server        string `json:"server"`
		ServerVersion string `json:"server_version"`
	} `json:"environment"`
}

type instanceState struct {
	Name string `json:"name"`
	// Location is the cluster member running this instance, empty on a
	// standalone server.
	Location string `json:"location"`
}

func RecipeFingerprint(plan imageplan.Plan) (string, error) {
	provision, err := scripts.ReadFile("assets/provision.sh")
	if err != nil {
		return "", err
	}
	sanitize, err := scripts.ReadFile("assets/sanitize.sh")
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	instanceKeys := make([]string, 0, len(plan.InstanceConfig))
	for key := range plan.InstanceConfig {
		instanceKeys = append(instanceKeys, key)
	}
	sort.Strings(instanceKeys)
	for _, key := range instanceKeys {
		_, _ = hash.Write([]byte(key))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(plan.InstanceConfig[key]))
		_, _ = hash.Write([]byte{0})
	}
	parts := [][]byte{[]byte(plan.ManifestFingerprint), provision}
	if imageType(plan) == "container" {
		containerProvision, err := scripts.ReadFile("assets/container-provision.sh")
		if err != nil {
			return "", err
		}
		parts = append(parts, containerProvision)
	}
	if plan.Variant == "integration" {
		dockerProvision, err := scripts.ReadFile("assets/docker-provision.sh")
		if err != nil {
			return "", err
		}
		dockerSeal, err := scripts.ReadFile("assets/docker-seal.sh")
		if err != nil {
			return "", err
		}
		parts = append(parts, dockerProvision, dockerSeal)
	}
	parts = append(parts, sanitize)
	for _, part := range parts {
		_, _ = hash.Write(part)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

// SmokeFingerprint versions the promotion policy independently from the
// byte-producing build recipe. Tightening a runtime assertion must revalidate
// an existing immutable image, but it must not force a byte-identical rebuild.
func SmokeFingerprint(plan imageplan.Plan) (string, error) {
	smokeName := "assets/smoke.sh"
	if plan.Variant == "integration" {
		smokeName = "assets/smoke-integration.sh"
	}
	smoke, err := scripts.ReadFile(smokeName)
	if err != nil {
		return "", err
	}
	recipe, err := RecipeFingerprint(plan)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, part := range []string{
		recipe,
		plan.Profile,
		plan.PublicHostAddress,
		fmt.Sprintf("%d", plan.SmokeRootDiskGiB),
		string(smoke),
	} {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func (o *Orchestrator) Apply(ctx context.Context, plan imageplan.Plan, artifacts Artifacts) (Result, error) {
	return o.ApplyWithOptions(ctx, plan, artifacts, ApplyOptions{})
}

func (o *Orchestrator) ApplyWithOptions(ctx context.Context, plan imageplan.Plan, artifacts Artifacts, options ApplyOptions) (Result, error) {
	if o.Runner == nil {
		return Result{}, fmt.Errorf("incus runner is required")
	}
	if err := validateArtifactPaths(plan, artifacts); err != nil {
		return Result{}, err
	}
	recipe, err := RecipeFingerprint(plan)
	if err != nil {
		return Result{}, fmt.Errorf("fingerprint image recipe: %w", err)
	}
	smokeFingerprint, err := SmokeFingerprint(plan)
	if err != nil {
		return Result{}, fmt.Errorf("fingerprint image smoke policy: %w", err)
	}
	if err := o.verifyFoundation(ctx, plan); err != nil {
		return Result{}, err
	}

	aliases, err := o.aliases(ctx, plan.Project)
	if err != nil {
		return Result{}, err
	}
	sourceFingerprint, err := o.ensureSourceImage(ctx, plan, artifacts, aliases)
	if err != nil {
		return Result{}, err
	}
	aliases, err = o.aliases(ctx, plan.Project)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Applied:             true,
		Built:               false,
		ManifestFingerprint: plan.ManifestFingerprint,
		RecipeFingerprint:   recipe,
		SmokeFingerprint:    smokeFingerprint,
		SourceFingerprint:   sourceFingerprint,
		ImmutableAlias:      plan.Image.Alias,
		CurrentAlias:        plan.Image.CurrentAlias,
		Smoke:               map[string]any{},
	}
	targetFingerprint := aliases[plan.Image.Alias].Target
	if targetFingerprint != "" {
		image, err := o.image(ctx, plan.Project, targetFingerprint)
		if err != nil {
			return Result{}, fmt.Errorf("inspect existing target image: %w", err)
		}
		if err := verifyTargetImage(image, plan, recipe); err != nil {
			return Result{}, err
		}
		result.PackageManifestSHA256 = image.Properties["user.nddev.package_manifest_sha256"]
	} else {
		targetFingerprint, result.PackageManifestSHA256, err = o.buildTarget(ctx, plan, artifacts, sourceFingerprint, recipe)
		if err != nil {
			return Result{}, err
		}
		result.Built = true
	}
	result.ImageFingerprint = targetFingerprint

	smoke, err := o.smoke(ctx, plan, targetFingerprint, artifacts)
	if err != nil {
		return Result{}, err
	}
	result.Smoke = smoke
	if options.StageOnly {
		return result, nil
	}
	previous, err := o.promote(ctx, plan, targetFingerprint)
	if err != nil {
		return Result{}, err
	}
	result.PreviousFingerprint = previous
	result.Promoted = true
	return result, nil
}

// guestToolchain is the exact per-toolchain contract handed to provision.sh.
// It carries no credential, only a guest archive path, its pinned digest and
// the version every installed executable must report.
type guestToolchain struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Archive       string `json:"archive"`
	ArchiveSHA256 string `json:"archive_sha256"`
}

func encodeToolchains(toolchains []guestToolchain) (string, error) {
	encoded, err := json.Marshal(toolchains)
	if err != nil {
		return "", fmt.Errorf("encode toolchain provisioning contract: %w", err)
	}
	return base64.StdEncoding.EncodeToString(encoded), nil
}

func validateArtifactPaths(plan imageplan.Plan, artifacts Artifacts) error {
	wanted := map[string]string{
		"checksums":      plan.Source.ChecksumsFile,
		"signature":      plan.Source.SignatureFile,
		"metadata":       plan.Source.MetadataFile,
		"runner":         plan.Runner.Archive,
		"compiler-cache": plan.CompilerCache.Archive,
		"go-cache-seed":  plan.GoCacheSeed.Archive,
	}
	actual := map[string]string{
		"checksums":      artifacts.Checksums,
		"signature":      artifacts.Signature,
		"metadata":       artifacts.Metadata,
		"runner":         artifacts.Runner,
		"compiler-cache": artifacts.CompilerCache,
		"go-cache-seed":  artifacts.GoCacheSeed,
	}
	if imageType(plan) == "container" {
		wanted["rootfs"] = plan.Source.RootfsFile
		actual["rootfs"] = artifacts.Rootfs
	} else {
		wanted["disk"] = plan.Source.DiskFile
		actual["disk"] = artifacts.Disk
	}
	if plan.BrowserSmoke != nil {
		wanted["browser-smoke"] = plan.BrowserSmoke.Archive
		actual["browser-smoke"] = artifacts.BrowserSmoke
	} else if artifacts.BrowserSmoke != "" {
		return fmt.Errorf("unexpected browser smoke artifact was fetched")
	}
	for _, toolchain := range plan.Toolchains {
		path, ok := artifacts.Toolchains[toolchain.Name]
		if !ok {
			return fmt.Errorf("%s toolchain artifact was not fetched", toolchain.Name)
		}
		key := "toolchain-" + toolchain.Name
		wanted[key] = toolchain.Archive
		actual[key] = path
	}
	for _, binary := range plan.PathBinaries {
		path, ok := artifacts.PathBinaries[binary.Name]
		if !ok || filepath.Base(path) != binary.Archive {
			return fmt.Errorf("fetched artifact for %s does not match its pinned archive", binary.Name)
		}
	}
	if len(artifacts.PathBinaries) != len(plan.PathBinaries) {
		return fmt.Errorf("fetched %d path binary artifacts, expected %d", len(artifacts.PathBinaries), len(plan.PathBinaries))
	}
	if len(artifacts.Toolchains) != len(plan.Toolchains) {
		return fmt.Errorf("fetched %d toolchain artifacts, expected %d", len(artifacts.Toolchains), len(plan.Toolchains))
	}
	for name, path := range actual {
		if filepath.Dir(path) != artifacts.Directory || filepath.Base(path) != wanted[name] {
			return fmt.Errorf("%s artifact path %q is outside the verified artifact set", name, path)
		}
	}
	if artifacts.VerifiedBy != plan.Source.SignerFingerprint {
		return fmt.Errorf("artifact signer %q does not match pinned %q", artifacts.VerifiedBy, plan.Source.SignerFingerprint)
	}
	return nil
}

func (o *Orchestrator) verifyFoundation(ctx context.Context, plan imageplan.Plan) error {
	server, err := queryJSON[serverState](ctx, o.Runner, "/1.0")
	if err != nil {
		return fmt.Errorf("inspect Incus server: %w", err)
	}
	if server.Auth != "trusted" || server.Environment.Server != "incus" || server.Environment.ServerVersion != strings.TrimPrefix(plan.IncusVersion, "v") {
		return fmt.Errorf("incus identity/version does not match trusted %s baseline", plan.IncusVersion)
	}
	projectPath := "/1.0/projects/" + url.PathEscape(plan.Project)
	if _, err := queryJSON[map[string]any](ctx, o.Runner, projectPath); err != nil {
		return fmt.Errorf("inspect image project: %w", err)
	}
	profilePath := "/1.0/profiles/" + url.PathEscape(plan.Profile) + "?project=" + url.QueryEscape(plan.Project)
	if _, err := queryJSON[map[string]any](ctx, o.Runner, profilePath); err != nil {
		return fmt.Errorf("inspect image profile: %w", err)
	}
	instancesPath := "/1.0/instances?project=" + url.QueryEscape(plan.Project) + "&recursion=1"
	instances, err := queryJSON[[]instanceState](ctx, o.Runner, instancesPath)
	if err != nil {
		return fmt.Errorf("list image-project instances: %w", err)
	}
	// The build needs a machine to itself: it creates a builder and a smoke VM
	// and holds the host reserve while it does. On a standalone server that is
	// the whole project, and requiring the project empty says so exactly.
	//
	// In a cluster it is one member. Requiring every member empty would mean
	// stopping the entire fleet to rebuild one image -- and on a fleet that is
	// continuously serving, never being able to rebuild it at all, which is how
	// nddev-linux-integration stayed imageless while ai_stp queued against it.
	// So the build claims this member, and only this member has to be free.
	member, err := o.localClusterMember(ctx)
	if err != nil {
		return err
	}
	blocking := make([]string, 0, len(instances))
	for _, instance := range instances {
		if member != "" && instance.Location != member {
			continue
		}
		blocking = append(blocking, instance.Name)
	}
	if len(blocking) != 0 {
		if member != "" {
			return fmt.Errorf("image build requires cluster member %q to be empty; active instances there: %s",
				member, strings.Join(blocking, ","))
		}
		return fmt.Errorf("image build requires an empty project; active instances: %s", strings.Join(blocking, ","))
	}
	return nil
}

func (o Orchestrator) aliases(ctx context.Context, project string) (map[string]aliasState, error) {
	path := "/1.0/images/aliases?project=" + url.QueryEscape(project) + "&recursion=1"
	states, err := queryJSON[[]aliasState](ctx, o.Runner, path)
	if err != nil {
		return nil, fmt.Errorf("list image aliases: %w", err)
	}
	result := make(map[string]aliasState, len(states))
	for _, state := range states {
		result[state.Name] = state
	}
	return result, nil
}

func (o Orchestrator) image(ctx context.Context, project, fingerprint string) (imageState, error) {
	if !shaPattern.MatchString(fingerprint) {
		return imageState{}, fmt.Errorf("invalid Incus image fingerprint %q", fingerprint)
	}
	path := "/1.0/images/" + fingerprint + "?project=" + url.QueryEscape(project)
	return queryJSON[imageState](ctx, o.Runner, path)
}

func (o Orchestrator) ensureSourceImage(ctx context.Context, plan imageplan.Plan, artifacts Artifacts, aliases map[string]aliasState) (string, error) {
	if existing := aliases[plan.Image.SourceAlias].Target; existing != "" {
		image, err := o.image(ctx, plan.Project, existing)
		if err != nil {
			return "", fmt.Errorf("inspect source image: %w", err)
		}
		if image.Type != imageType(plan) || image.Architecture != plan.Image.Architecture || image.Properties["user.nddev.source.metadata_sha256"] != plan.Source.MetadataSHA256 {
			return "", fmt.Errorf("source alias %q does not match the pinned Canonical artifacts", plan.Image.SourceAlias)
		}
		artifactSHA256 := image.Properties["user.nddev.source.artifact_sha256"]
		// Images imported before the container source contract used the
		// VM-specific property name. Accept it only for VM sources and only
		// when it equals the currently pinned disk digest; new imports always
		// write the type-neutral property above.
		if artifactSHA256 == "" && imageType(plan) == "virtual-machine" {
			artifactSHA256 = image.Properties["user.nddev.source.disk_sha256"]
		}
		if artifactSHA256 != sourceArtifactSHA256(plan) {
			return "", fmt.Errorf("source alias %q does not match the pinned Canonical source artifact", plan.Image.SourceAlias)
		}
		return existing, nil
	}
	sourceArtifact := artifacts.Disk
	if imageType(plan) == "container" {
		sourceArtifact = artifacts.Rootfs
	}
	args := o.incus(plan.Project, "image", "import", artifacts.Metadata, sourceArtifact,
		"--alias", plan.Image.SourceAlias,
		"description=Canonical Ubuntu 24.04 cloud image "+plan.Source.ReleaseID,
		"os=Ubuntu", "release=24.04", "variant=cloud",
		"user.nddev.managed=true",
		"user.nddev.source.release="+plan.Source.ReleaseID,
		"user.nddev.source.metadata_sha256="+plan.Source.MetadataSHA256)
	args = append(args, "user.nddev.source.artifact_sha256="+sourceArtifactSHA256(plan))
	if _, err := o.Runner.Run(ctx, args...); err != nil {
		return "", fmt.Errorf("import pinned Canonical source image: %w", err)
	}
	refreshed, err := o.aliases(ctx, plan.Project)
	if err != nil {
		return "", err
	}
	fingerprint := refreshed[plan.Image.SourceAlias].Target
	if !shaPattern.MatchString(fingerprint) {
		return "", fmt.Errorf("source image alias did not resolve to a full fingerprint")
	}
	return fingerprint, nil
}

func (o Orchestrator) buildTarget(ctx context.Context, plan imageplan.Plan, artifacts Artifacts, sourceFingerprint, recipe string) (fingerprint, packageSHA string, err error) {
	created := false
	defer func() {
		if !created {
			return
		}
		if o.PreserveFailedBuilder {
			// Say where it is and that it costs something, so preserving is a
			// decision with a visible price rather than a leak.
			member, _ := o.localClusterMember(context.Background())
			if member == "" {
				member = "this server"
			}
			err = fmt.Errorf(
				"%w (builder %q preserved on %s for inspection; it holds disk and a capacity lease until `incus delete --project %s %s --force`)",
				err, plan.BuilderName, member, plan.Project, plan.BuilderName,
			)
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = o.Runner.Run(cleanupCtx, o.incus(plan.Project, "delete", plan.BuilderName, "--force")...)
	}()

	if _, err = o.Runner.Run(ctx, o.instanceInitArgs(plan, sourceFingerprint, plan.BuilderName, plan.BuilderDiskGiB)...); err != nil {
		return "", "", fmt.Errorf("initialize image builder: %w", err)
	}
	created = true
	if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "start", plan.BuilderName)...); err != nil {
		return "", "", fmt.Errorf("start image builder: %w", err)
	}
	if err = o.waitAgent(ctx, plan.Project, plan.BuilderName); err != nil {
		return "", "", err
	}
	if imageType(plan) == "container" {
		if _, err = o.runGuest(ctx, plan.Project, plan.BuilderName, nil, `set -Eeuo pipefail
for attempt in $(seq 1 30); do
  if systemctl show --property=Version >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
systemctl show --property=Version >/dev/null
install -d -m 0755 /etc/systemd/network
cat >/etc/systemd/network/10-eth0.network <<'EOF'
[Match]
Name=eth0

[Network]
DHCP=ipv4
LinkLocalAddressing=ipv6
IPv6AcceptRA=no
EOF
systemctl restart systemd-networkd.service systemd-resolved.service
for attempt in $(seq 1 30); do
  if ip -4 address show dev eth0 | grep -q 'inet ' && getent hosts archive.ubuntu.com >/dev/null; then
    exit 0
  fi
  sleep 1
done
echo 'container network did not acquire IPv4 and DNS' >&2
exit 1`); err != nil {
			return "", "", fmt.Errorf("prepare container builder network: %w", err)
		}
	} else if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "exec", plan.BuilderName, "--", "cloud-init", "status", "--wait")...); err != nil {
		return "", "", fmt.Errorf("wait for builder cloud-init: %w", err)
	}
	destination := plan.BuilderName + "/var/tmp/" + plan.Runner.Archive
	if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "file", "push", artifacts.Runner, destination, "--mode", "0600")...); err != nil {
		return "", "", fmt.Errorf("push verified runner archive: %w", err)
	}
	compilerCacheDestination := plan.BuilderName + "/var/tmp/" + plan.CompilerCache.Archive
	if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "file", "push", artifacts.CompilerCache, compilerCacheDestination, "--mode", "0600")...); err != nil {
		return "", "", fmt.Errorf("push verified compiler cache archive: %w", err)
	}
	goCacheSeedDestination := plan.BuilderName + "/var/tmp/" + plan.GoCacheSeed.Archive
	if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "file", "push", artifacts.GoCacheSeed, goCacheSeedDestination, "--mode", "0600")...); err != nil {
		return "", "", fmt.Errorf("push verified Go cache seed archive: %w", err)
	}
	toolchainRequests := make([]guestToolchain, 0, len(plan.Toolchains))
	for _, toolchain := range plan.Toolchains {
		destination := plan.BuilderName + "/var/tmp/" + toolchain.Archive
		if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "file", "push", artifacts.Toolchains[toolchain.Name], destination, "--mode", "0600")...); err != nil {
			return "", "", fmt.Errorf("push verified %s toolchain archive: %w", toolchain.Name, err)
		}
		toolchainRequests = append(toolchainRequests, guestToolchain{
			Name:          toolchain.Name,
			Version:       toolchain.Version,
			Archive:       "/var/tmp/" + toolchain.Archive,
			ArchiveSHA256: toolchain.ArchiveSHA256,
		})
	}
	encodedToolchains, err := encodeToolchains(toolchainRequests)
	if err != nil {
		return "", "", err
	}
	var pathBinaryLines strings.Builder
	for _, binary := range plan.PathBinaries {
		destination := plan.BuilderName + "/var/tmp/" + binary.Archive
		if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "file", "push", artifacts.PathBinaries[binary.Name], destination, "--mode", "0600")...); err != nil {
			return "", "", fmt.Errorf("push verified %s archive: %w", binary.Name, err)
		}
		fmt.Fprintf(&pathBinaryLines, "%s\t%s\t%s\t%s\t%s\n",
			binary.Name, binary.Archive, binary.ArchiveSHA256, binary.BinaryPath, binary.BinarySHA256)
	}
	encodedPathBinaries := base64.StdEncoding.EncodeToString([]byte(pathBinaryLines.String()))
	provision, _ := scripts.ReadFile("assets/provision.sh")
	recipeSHA := strings.TrimPrefix(recipe, "sha256:")
	guestArchive := "/var/tmp/" + plan.Runner.Archive
	guestCompilerCacheArchive := "/var/tmp/" + plan.CompilerCache.Archive
	guestGoCacheSeedArchive := "/var/tmp/" + plan.GoCacheSeed.Archive
	environment := map[string]string{
		"GHA_RUNNER_VERSION":         plan.Runner.Version,
		"GHA_RUNNER_SHA256":          plan.Runner.SHA256,
		"GHA_RUNNER_ARCHIVE":         guestArchive,
		"GHA_PACKAGES":               strings.Join(plan.PackageInstallSpecs, " "),
		"GHA_MANIFEST_FINGERPRINT":   plan.ManifestFingerprint,
		"GHA_RECIPE_FINGERPRINT":     recipe,
		"GHA_SOURCE_RELEASE_ID":      plan.Source.ReleaseID,
		"GHA_SOURCE_ARTIFACT_SHA256": sourceArtifactSHA256(plan),
		"GHA_SCCACHE_VERSION":        plan.CompilerCache.Version,
		"GHA_SCCACHE_ARCHIVE":        guestCompilerCacheArchive,
		"GHA_SCCACHE_ARCHIVE_SHA256": plan.CompilerCache.ArchiveSHA256,
		"GHA_SCCACHE_BINARY_PATH":    plan.CompilerCache.BinaryPath,
		"GHA_SCCACHE_BINARY_SHA256":  plan.CompilerCache.BinarySHA256,
		"GHA_TOOLCHAINS_B64":         encodedToolchains,
		"GHA_GO_CACHE_SEED_ARCHIVE":  guestGoCacheSeedArchive,
		"GHA_GO_CACHE_SEED_SHA256":   plan.GoCacheSeed.ArchiveSHA256,
		"GHA_GO_CACHE_SEED_COMMIT":   plan.GoCacheSeed.Commit,
		"GHA_GO_CACHE_SEED_PACKAGES": strings.Join(plan.GoCacheSeed.Packages, " "),
		"GHA_PATH_BINARIES_B64":      encodedPathBinaries,
	}
	if _, err = o.runGuest(ctx, plan.Project, plan.BuilderName, environment, string(provision)); err != nil {
		return "", "", fmt.Errorf("provision image builder: %w", err)
	}
	if imageType(plan) == "container" {
		containerProvision, _ := scripts.ReadFile("assets/container-provision.sh")
		if _, err = o.runGuest(ctx, plan.Project, plan.BuilderName, nil, string(containerProvision)); err != nil {
			return "", "", fmt.Errorf("provision container bootstrap: %w", err)
		}
	}
	if plan.Variant == "integration" {
		dockerProvision, _ := scripts.ReadFile("assets/docker-provision.sh")
		if _, err = o.runGuest(ctx, plan.Project, plan.BuilderName, map[string]string{
			"GHA_DOCKER_ACTION_BASE_REF": plan.DockerActionBaseRef,
			"GHA_DOCKER_STORAGE_DRIVER":  dockerStorageDriver(plan),
			"GHA_BROWSER":                plan.Browser,
		}, string(dockerProvision)); err != nil {
			return "", "", fmt.Errorf("provision Docker integration image: %w", err)
		}
	}
	output, err := o.Runner.Run(ctx, o.incus(plan.Project, "exec", plan.BuilderName, "--", "jq", "-er", ".package_manifest_sha256", "/etc/nddev/image-build.json")...)
	if err != nil {
		return "", "", fmt.Errorf("read package manifest digest: %w", err)
	}
	packageSHA = strings.TrimSpace(string(output))
	if !shaPattern.MatchString(packageSHA) {
		return "", "", fmt.Errorf("guest returned invalid package manifest digest %q", packageSHA)
	}
	sanitize, _ := scripts.ReadFile("assets/sanitize.sh")
	if plan.Variant == "integration" {
		dockerSeal, _ := scripts.ReadFile("assets/docker-seal.sh")
		if _, err = o.runGuest(ctx, plan.Project, plan.BuilderName, map[string]string{
			"GHA_DOCKER_ACTION_BASE_REF": plan.DockerActionBaseRef,
		}, string(dockerSeal)); err != nil {
			return "", "", fmt.Errorf("seal Docker integration image: %w", err)
		}
	}
	if _, err = o.runGuest(ctx, plan.Project, plan.BuilderName, map[string]string{"GHA_INSTANCE_TYPE": imageType(plan)}, string(sanitize)); err != nil {
		return "", "", fmt.Errorf("sanitize image builder: %w", err)
	}
	if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "stop", plan.BuilderName, "--timeout", "60")...); err != nil {
		return "", "", fmt.Errorf("stop sanitized image builder: %w", err)
	}
	properties := []string{
		"description=NDDev Ubuntu 24.04 actions runner " + plan.Runner.Version,
		"os=Ubuntu", "release=24.04", "variant=cloud",
		"user.nddev.managed=true",
		"user.nddev.manifest_fingerprint=" + plan.ManifestFingerprint,
		"user.nddev.recipe_sha256=" + recipeSHA,
		"user.nddev.runner.version=" + plan.Runner.Version,
		"user.nddev.runner.sha256=" + plan.Runner.SHA256,
		"user.nddev.sccache.version=" + plan.CompilerCache.Version,
		"user.nddev.sccache.archive_sha256=" + plan.CompilerCache.ArchiveSHA256,
		"user.nddev.sccache.binary_sha256=" + plan.CompilerCache.BinarySHA256,
		"user.nddev.source.release=" + plan.Source.ReleaseID,
		"user.nddev.source.artifact_sha256=" + sourceArtifactSHA256(plan),
		"user.nddev.package_manifest_sha256=" + packageSHA,
		"user.nddev.image.variant=" + plan.Variant,
	}
	if plan.Variant == "integration" {
		properties = append(properties,
			"user.nddev.docker-action-base="+plan.DockerActionBaseRef,
		)
	}
	if plan.Browser != "" {
		properties = append(properties,
			"user.nddev.browser="+plan.Browser,
			"user.nddev.browser-smoke.version="+plan.BrowserSmoke.Version,
			"user.nddev.browser-smoke.archive_sha256="+plan.BrowserSmoke.ArchiveSHA256,
		)
	}
	for key, value := range toolchainProperties(plan) {
		properties = append(properties, key+"="+value)
	}
	// Map iteration is unordered; keep the published argument vector byte-stable.
	sort.Strings(properties)
	publishArgs := o.incus(plan.Project, "publish", plan.BuilderName, "--alias", plan.Image.Alias)
	publishArgs = append(publishArgs, properties...)
	if _, err = o.Runner.Run(ctx, publishArgs...); err != nil {
		return "", "", fmt.Errorf("publish immutable worker image: %w", err)
	}
	aliases, err := o.aliases(ctx, plan.Project)
	if err != nil {
		return "", "", err
	}
	fingerprint = aliases[plan.Image.Alias].Target
	if !shaPattern.MatchString(fingerprint) {
		return "", "", fmt.Errorf("published image alias did not resolve to a full fingerprint")
	}
	if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "delete", plan.BuilderName)...); err != nil {
		return "", "", fmt.Errorf("delete sealed image builder: %w", err)
	}
	created = false
	return fingerprint, packageSHA, nil
}

// toolchainProperties exposes every baked toolchain on the published image so a
// deployed worker can be audited without booting it or trusting a build log.
func toolchainProperties(plan imageplan.Plan) map[string]string {
	properties := make(map[string]string, 2*len(plan.Toolchains))
	for _, toolchain := range plan.Toolchains {
		prefix := "user.nddev.toolchain." + toolchain.Name
		properties[prefix+".version"] = toolchain.Version
		properties[prefix+".archive_sha256"] = toolchain.ArchiveSHA256
	}
	return properties
}

func verifyTargetImage(image imageState, plan imageplan.Plan, recipe string) error {
	recipeSHA := strings.TrimPrefix(recipe, "sha256:")
	wanted := map[string]string{
		"user.nddev.manifest_fingerprint":   plan.ManifestFingerprint,
		"user.nddev.recipe_sha256":          recipeSHA,
		"user.nddev.runner.version":         plan.Runner.Version,
		"user.nddev.runner.sha256":          plan.Runner.SHA256,
		"user.nddev.sccache.version":        plan.CompilerCache.Version,
		"user.nddev.sccache.archive_sha256": plan.CompilerCache.ArchiveSHA256,
		"user.nddev.sccache.binary_sha256":  plan.CompilerCache.BinarySHA256,
		"user.nddev.source.release":         plan.Source.ReleaseID,
		"user.nddev.source.artifact_sha256": sourceArtifactSHA256(plan),
		"user.nddev.image.variant":          plan.Variant,
	}
	if plan.Variant == "integration" {
		wanted["user.nddev.docker-action-base"] = plan.DockerActionBaseRef
	}
	if plan.Browser != "" {
		wanted["user.nddev.browser"] = plan.Browser
		wanted["user.nddev.browser-smoke.version"] = plan.BrowserSmoke.Version
		wanted["user.nddev.browser-smoke.archive_sha256"] = plan.BrowserSmoke.ArchiveSHA256
	}
	for key, value := range toolchainProperties(plan) {
		wanted[key] = value
	}
	if image.Type != imageType(plan) || image.Architecture != plan.Image.Architecture {
		return fmt.Errorf("immutable target alias points to the wrong image type or architecture")
	}
	for key, value := range wanted {
		if image.Properties[key] != value {
			return fmt.Errorf("immutable target alias property %q is %q, expected %q", key, image.Properties[key], value)
		}
	}
	if !shaPattern.MatchString(image.Properties["user.nddev.package_manifest_sha256"]) {
		return fmt.Errorf("immutable target image has no valid package manifest digest")
	}
	return nil
}

func (o Orchestrator) smoke(ctx context.Context, plan imageplan.Plan, fingerprint string, artifacts Artifacts) (result map[string]any, err error) {
	created := false
	defer func() {
		if created {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_, _ = o.Runner.Run(cleanupCtx, o.incus(plan.Project, "delete", plan.SmokeName, "--force")...)
		}
	}()
	if _, err = o.Runner.Run(ctx, o.instanceInitArgs(plan, fingerprint, plan.SmokeName, 0)...); err != nil {
		return nil, fmt.Errorf("initialize image smoke instance: %w", err)
	}
	created = true
	if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "start", plan.SmokeName)...); err != nil {
		return nil, fmt.Errorf("start image smoke instance: %w", err)
	}
	if err = o.waitAgent(ctx, plan.Project, plan.SmokeName); err != nil {
		return nil, err
	}
	if plan.Browser != "" {
		destination := plan.SmokeName + "/var/tmp/" + plan.BrowserSmoke.Archive
		if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "file", "push", artifacts.BrowserSmoke, destination, "--mode", "0600")...); err != nil {
			return nil, fmt.Errorf("push verified browser smoke archive: %w", err)
		}
	}
	smokeName := "assets/smoke.sh"
	if plan.Variant == "integration" {
		smokeName = "assets/smoke-integration.sh"
	}
	smoke, _ := scripts.ReadFile(smokeName)
	smokeToolchains := make([]guestToolchain, 0, len(plan.Toolchains))
	for _, toolchain := range plan.Toolchains {
		smokeToolchains = append(smokeToolchains, guestToolchain{
			Name:          toolchain.Name,
			Version:       toolchain.Version,
			ArchiveSHA256: toolchain.ArchiveSHA256,
		})
	}
	encodedToolchains, err := encodeToolchains(smokeToolchains)
	if err != nil {
		return nil, err
	}
	smokeEnvironment := map[string]string{
		"GHA_INSTANCE_TYPE":          imageType(plan),
		"GHA_RUNNER_VERSION":         plan.Runner.Version,
		"GHA_SCCACHE_VERSION":        plan.CompilerCache.Version,
		"GHA_SCCACHE_BINARY_SHA256":  plan.CompilerCache.BinarySHA256,
		"GHA_TOOLCHAINS_B64":         encodedToolchains,
		"GHA_GO_CACHE_SEED_COMMIT":   plan.GoCacheSeed.Commit,
		"GHA_GO_CACHE_SEED_SHA256":   plan.GoCacheSeed.ArchiveSHA256,
		"GHA_PUBLIC_HOST_ADDRESS":    plan.PublicHostAddress,
		"GHA_EXPECTED_ROOT_DISK_GIB": fmt.Sprintf("%d", plan.SmokeRootDiskGiB),
		"GHA_DOCKER_ACTION_BASE_REF": plan.DockerActionBaseRef,
		"GHA_DOCKER_STORAGE_DRIVER":  dockerStorageDriver(plan),
		"GHA_BROWSER":                plan.Browser,
		"GHA_PROVIDES":               strings.Join(plan.Provides, " "),
	}
	if plan.Browser != "" {
		smokeEnvironment["GHA_BROWSER_SMOKE_VERSION"] = plan.BrowserSmoke.Version
		smokeEnvironment["GHA_BROWSER_SMOKE_ARCHIVE"] = "/var/tmp/" + plan.BrowserSmoke.Archive
		smokeEnvironment["GHA_BROWSER_SMOKE_SHA256"] = plan.BrowserSmoke.ArchiveSHA256
		smokeEnvironment["GHA_BROWSER_SMOKE_BINARY"] = plan.BrowserSmoke.BinaryPath
	}
	output, err := o.runGuest(ctx, plan.Project, plan.SmokeName, smokeEnvironment, string(smoke))
	if err != nil {
		return nil, fmt.Errorf("execute image smoke test: %w", err)
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("decode smoke evidence: %w", err)
	}
	if _, err = o.Runner.Run(ctx, o.incus(plan.Project, "delete", plan.SmokeName, "--force")...); err != nil {
		return nil, fmt.Errorf("delete image smoke VM: %w", err)
	}
	created = false
	return result, nil
}

func (o Orchestrator) promote(ctx context.Context, plan imageplan.Plan, target string) (string, error) {
	aliases, err := o.aliases(ctx, plan.Project)
	if err != nil {
		return "", err
	}
	current := aliases[plan.Image.CurrentAlias].Target
	previous := aliases[plan.Image.PreviousAlias].Target
	if current == "" && previous != "" {
		return "", fmt.Errorf("previous alias exists while current alias is absent; refusing ambiguous promotion")
	}
	if current == target {
		return previous, nil
	}
	if current != "" {
		if err := o.setAlias(ctx, plan.Project, plan.Image.PreviousAlias, current, "Previous verified NDDev worker image", aliases[plan.Image.PreviousAlias]); err != nil {
			return "", fmt.Errorf("retain previous image alias: %w", err)
		}
		previous = current
	}
	if err := o.setAlias(ctx, plan.Project, plan.Image.CurrentAlias, target, "Current verified NDDev worker image", aliases[plan.Image.CurrentAlias]); err != nil {
		return "", fmt.Errorf("promote current image alias: %w", err)
	}
	verified, err := o.aliases(ctx, plan.Project)
	if err != nil {
		return "", fmt.Errorf("verify promoted aliases: %w", err)
	}
	if verified[plan.Image.CurrentAlias].Target != target {
		return "", fmt.Errorf("current alias did not converge to promoted fingerprint")
	}
	if previous != "" && verified[plan.Image.PreviousAlias].Target != previous {
		return "", fmt.Errorf("previous alias did not retain rollback fingerprint")
	}
	return previous, nil
}

func (o Orchestrator) setAlias(ctx context.Context, project, name, target, description string, current aliasState) error {
	payload := map[string]string{"target": target, "description": description}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if current.Target == "" {
		create := map[string]string{"name": name, "target": target, "description": description}
		data, _ = json.Marshal(create)
		path := "/1.0/images/aliases?project=" + url.QueryEscape(project)
		_, err = o.Runner.Run(ctx, "--force-local", "query", "--request", "POST", "--wait", "--data", string(data), path)
		return err
	}
	if current.Target == target && current.Description == description {
		return nil
	}
	path := "/1.0/images/aliases/" + url.PathEscape(name) + "?project=" + url.QueryEscape(project)
	_, err = o.Runner.Run(ctx, "--force-local", "query", "--request", "PUT", "--wait", "--data", string(data), path)
	return err
}

func (o Orchestrator) waitAgent(ctx context.Context, project, instance string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	for {
		if _, err := o.Runner.Run(ctx, o.incus(project, "exec", instance, "--", "true")...); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("incus agent for %q did not become ready within 3 minutes", instance)
		case <-ticker.C:
		}
	}
}

func (o Orchestrator) runGuest(ctx context.Context, project, instance string, environment map[string]string, script string) ([]byte, error) {
	args := o.incus(project, "exec", instance)
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := environment[key]
		args = append(args, "--env", key+"="+value)
	}
	args = append(args, "--", "bash", "-ceu", script)
	return o.Runner.Run(ctx, args...)
}

func (o Orchestrator) incus(project string, args ...string) []string {
	base := []string{"--force-local", "--project", project}
	return append(base, args...)
}

func (o *Orchestrator) instanceInitArgs(plan imageplan.Plan, fingerprint, name string, rootDiskGiB int) []string {
	args := o.incus(plan.Project, "init", fingerprint, name)
	if imageType(plan) == "virtual-machine" {
		args = append(args, "--vm")
	}
	args = append(args, "--profile", plan.Profile)
	// Pin the build to the member it just verified is empty. Without this the
	// placement scriptlet would choose, and it would rightly choose whichever
	// member has the most free memory -- which is not necessarily this one.
	if o.clusterMember != "" {
		args = append(args, "--target", o.clusterMember)
	}
	if rootDiskGiB > 0 {
		args = append(args, "--device", fmt.Sprintf("root,size=%dGiB", rootDiskGiB))
	}
	keys := make([]string, 0, len(plan.InstanceConfig))
	for key := range plan.InstanceConfig {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--config", key+"="+plan.InstanceConfig[key])
	}
	return args
}

func sourceArtifactSHA256(plan imageplan.Plan) string {
	if imageType(plan) == "container" {
		return plan.Source.RootfsSHA256
	}
	return plan.Source.DiskSHA256
}

func imageType(plan imageplan.Plan) string {
	return plan.Image.EffectiveType()
}

func dockerStorageDriver(plan imageplan.Plan) string {
	if imageType(plan) == "container" {
		return "overlayfs"
	}
	return "overlay2"
}

func queryJSON[T any](ctx context.Context, runner CommandRunner, path string) (T, error) {
	var result T
	data, err := runner.Run(ctx, "--force-local", "query", path)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("decode %s response: %w", path, err)
	}
	return result, nil
}
