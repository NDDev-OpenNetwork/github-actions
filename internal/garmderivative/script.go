package garmderivative

import (
	"fmt"
	"strings"
)

// DefaultScriptPath is the build script whose inputs this package renders.
const DefaultScriptPath = "scripts/build-garm-nddev.sh"

const (
	regionBegin = "# BEGIN GENERATED REGION -- do not edit"
	regionEnd   = "# END GENERATED REGION"
)

// Disposition records what one manifest field is for. Exactly one of the two is
// set: a field is either an input the build reads, in which case ShellName is
// the assignment it becomes, or it is not a build input, in which case Reason
// says what does consume it.
//
// The point is that there is no third state. A field with neither is a field
// nobody decided about, and that is precisely what patches[4].sha256 was: the
// contract test listed the first four digests and stopped, so the fifth could
// hold any value in the script and still pass.
type Disposition struct {
	ShellName string
	Reason    string
}

// Rendered reports whether the field becomes an assignment in the generated
// region.
func (d Disposition) Rendered() bool { return d.ShellName != "" }

// FieldDispositions maps every leaf field of Manifest to what consumes it.
// TestEveryManifestFieldIsDispositioned walks the struct and requires this map
// to name each field exactly once, so adding a field to the manifest without
// deciding what reads it fails before it can be merged.
func FieldDispositions() map[string]Disposition {
	notBuild := func(reason string) Disposition { return Disposition{Reason: reason} }
	renders := func(name string) Disposition { return Disposition{ShellName: name} }

	dispositions := map[string]Disposition{
		"schema_version": notBuild("governs how this file is decoded, not what is built from it"),
		"artifact":       notBuild("names which manifest this is; the build script is already specific to it"),

		"derivative_version": renders("derivative_version"),

		"upstream.repository":           renders("upstream_repository"),
		"upstream.commit":               renders("upstream_commit"),
		"upstream.release":              notBuild("names the upstream release the pinned commit belongs to; docs/upstream-baseline.md consumes it"),
		"upstream.release_asset_sha256": notBuild("digest of the upstream release asset, which the build does not download -- it fetches the commit; docs/upstream-baseline.md consumes it"),

		"patches[].path":    renders("patch_paths"),
		"patches[].sha256":  renders("patch_sha256s"),
		"patches[].purpose": notBuild("reviewed prose stating why the patch exists; internal/deploycontract holds it against the patch content"),

		"overlays[].path":    renders("overlay_paths"),
		"overlays[].sha256":  renders("overlay_sha256s"),
		"overlays[].purpose": notBuild("reviewed prose stating why the overlay exists; internal/deploycontract holds it against the overlay content"),

		"build.container_image":               renders("build_image"),
		"build.go_version":                    renders("build_go_version"),
		"build.cgo_enabled":                   renders("build_cgo_enabled"),
		"build.target_os":                     renders("build_target_os"),
		"build.target_arch":                   renders("build_target_arch"),
		"build.network_during_test_and_build": renders("build_network"),
		"build.module_mode":                   renders("build_module_mode"),
		"build.tags":                          renders("build_tags"),
		"build.reproducible_rebuilds":         renders("build_reproducible_rebuilds"),
		"build.maximum_required_glibc":        renders("build_maximum_required_glibc"),
		"build.binary_sha256":                 renders("expected_binary_sha256"),
	}
	for _, field := range runtimeContractFields {
		dispositions["runtime_contract."+field] = notBuild(
			"a behavioural promise, not a build input; internal/deploycontract holds it against the patch and overlay content")
	}
	return dispositions
}

// runtimeContractFields is the yaml name of every RuntimeContract member. It is
// listed rather than reflected so that the dispositions map and the struct are
// two statements that have to agree, which is what the test checks.
var runtimeContractFields = []string{
	"event_driven_scale_set_wake",
	"event_driven_instance_wake",
	"startup_states_protected_from_scale_down",
	"periodic_reconciliation_seconds",
	"durable_queue_intent_before_acquire",
	"job_assigned_provisional_capacity",
	"job_available_identity_binding_before_acquire",
	"startup_queue_promotion",
	"deferred_available_message_retained",
	"global_max_in_flight",
	"weighted_repository_fairness",
	"per_repository_limit",
	"priority_aging_seconds",
	"failed_scale_set_registration_cleanup",
	"direct_jit_provider_handoff",
	"direct_jit_phase_telemetry",
	"durable_provider_retry",
	"provider_retry_maximum_attempts",
	"provider_retry_backoff_cap_seconds",
	"capacity_retry_backoff_cap_seconds",
	"capacity_retry_wake_after_delete",
	"authoritative_job_reconciliation",
	"authoritative_queue_intent_reconciliation",
	"job_reconciliation_interval_seconds",
	"job_reconciliation_batch_size",
	"official_actions_runner_unchanged",
}

// RenderRegion produces the generated block of the build script: every manifest
// field that is a build input, as a shell assignment, and nothing else.
func (m Manifest) RenderRegion() (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	cgo := "0"
	if m.Build.CGOEnabled {
		cgo = "1"
	}

	var out strings.Builder
	out.WriteString(regionBegin + "\n")
	out.WriteString("# generator: gha-fleet render-garm-build\n")
	out.WriteString("# edit-source: " + DefaultManifestPath + "\n")
	out.WriteString("#\n")
	out.WriteString("# Every value below is the manifest's. Editing one here detaches the build\n")
	out.WriteString("# from the provenance it is reviewed against, which is why the region is\n")
	out.WriteString("# regenerated and compared rather than maintained.\n")

	scalar := func(name, value string) {
		fmt.Fprintf(&out, "readonly %s=%q\n", name, value)
	}
	scalar("derivative_version", m.DerivativeVersion)
	scalar("upstream_repository", m.Upstream.Repository)
	scalar("upstream_commit", m.Upstream.Commit)
	scalar("build_image", m.Build.ContainerImage)
	scalar("build_go_version", m.Build.GoVersion)
	scalar("build_cgo_enabled", cgo)
	scalar("build_target_os", m.Build.TargetOS)
	scalar("build_target_arch", m.Build.TargetArch)
	scalar("build_network", m.Build.NetworkDuringTestAndBuild)
	scalar("build_module_mode", m.Build.ModuleMode)
	scalar("build_tags", strings.Join(m.Build.Tags, ","))
	scalar("build_reproducible_rebuilds", fmt.Sprintf("%d", m.Build.ReproducibleRebuilds))
	scalar("build_maximum_required_glibc", m.Build.MaximumRequiredGLIBC)
	scalar("expected_binary_sha256", m.Build.BinarySHA256)

	// Arrays rather than numbered scalars. The fifth patch digest went unchecked
	// because five patches were five hand-written pairs of variables and the
	// contract test enumerated four of them; a list cannot be enumerated short
	// of its length.
	array := func(name string, values []string) {
		fmt.Fprintf(&out, "readonly %s=(\n", name)
		for _, value := range values {
			fmt.Fprintf(&out, "  %q\n", value)
		}
		out.WriteString(")\n")
	}
	array("patch_paths", paths(m.Patches))
	array("patch_sha256s", digests(m.Patches))
	array("overlay_paths", paths(m.Overlays))
	array("overlay_sha256s", digests(m.Overlays))
	array("overlay_targets", installTargets(m.Overlays))

	out.WriteString(regionEnd + "\n")
	return out.String(), nil
}

// SpliceRegion replaces the generated region of script with a freshly rendered
// one and returns the result. It fails rather than appending when the markers
// are missing, because a script with no region is a script somebody replaced.
func (m Manifest) SpliceRegion(script string) (string, error) {
	region, err := m.RenderRegion()
	if err != nil {
		return "", err
	}
	begin := strings.Index(script, regionBegin)
	if begin < 0 {
		return "", fmt.Errorf("build script has no %q marker", regionBegin)
	}
	end := strings.Index(script, regionEnd)
	if end < begin {
		return "", fmt.Errorf("build script has no %q marker after its beginning", regionEnd)
	}
	return script[:begin] + region + script[end+len(regionEnd)+1:], nil
}

func paths(sources []Source) []string {
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		out = append(out, source.Path)
	}
	return out
}

func digests(sources []Source) []string {
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		out = append(out, source.SHA256)
	}
	return out
}

func installTargets(sources []Source) []string {
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		out = append(out, source.InstallTarget())
	}
	return out
}
