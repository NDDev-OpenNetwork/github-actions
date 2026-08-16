package deploycontract

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/garmderivative"
)

// The manifest's schema, its shape rules and the mapping from its fields into
// the build script all live in internal/garmderivative now. What is left here is
// the part that is a contract rather than a shape: which files each patch is
// allowed to touch, which behaviour it has to carry, and what it must never
// carry. Those are reviewed decisions about this derivative, not properties of
// derivative manifests in general.
//
// Values the manifest already states -- the version, the build container, the
// digests -- are deliberately not restated here. Repeating one would recreate
// the defect this split exists to remove: the script used to hold its own copy
// of every value, and the fifth patch digest was compared against nothing.
func loadDerivativeManifest(t *testing.T) garmderivative.Manifest {
	t.Helper()
	manifest, err := garmderivative.Load(filepath.Join("../..", garmderivative.DefaultManifestPath))
	if err != nil {
		t.Fatalf("load GARM derivative manifest: %v", err)
	}
	return manifest
}

// A host running a different derivative than the one this tree describes is a
// host whose behaviour nothing here predicts. This is the binding that makes the
// manifest the single statement of which derivative the fleet runs.
func TestEveryHostRunsTheDerivativeThisTreeDescribes(t *testing.T) {
	t.Parallel()
	manifest := loadDerivativeManifest(t)
	for name, platform := range declaredHostConfigs(t) {
		if platform.ControlPlane.ManagerVersion != manifest.DerivativeVersion {
			t.Fatalf("%s manager version = %q, manifest declares %q", name,
				platform.ControlPlane.ManagerVersion, manifest.DerivativeVersion)
		}
	}
}

// Each patch is allowed to touch exactly the files listed for it. A derivative
// stays reviewable only while its diff stays where it was reviewed; a patch that
// silently grew a file is a change nobody looked at.
func TestEachPatchTouchesOnlyTheFilesItWasReviewedFor(t *testing.T) {
	t.Parallel()
	manifest := loadDerivativeManifest(t)

	wantPatchPaths := [][]string{
		{
			"workers/provider/instance_manager.go",
			"workers/provider/instance_manager_wake_test.go",
			"workers/scaleset/autoscale_wake_test.go",
			"workers/scaleset/scaleset.go",
		},
		{
			"workers/scaleset/interfaces.go",
			"workers/scaleset/scaleset_helper.go",
			"workers/scaleset/scaleset_listener.go",
		},
		{
			"workers/provider/failed_scale_set_runner_cleanup_test.go",
			"workers/provider/instance_manager.go",
			"workers/provider/provider_helper.go",
		},
		{
			"workers/provider/instance_manager.go",
			"workers/provider/nddev_direct_jit.go",
			"workers/scaleset/nddev_direct_jit_state.go",
			"workers/scaleset/nddev_direct_jit_state_test.go",
			"workers/scaleset/scaleset_helper.go",
			"workers/provider/nddev_direct_jit_test.go",
		},
		{
			"workers/provider/instance_manager.go",
			"workers/scaleset/scaleset_listener.go",
		},
	}
	if len(manifest.Patches) != len(wantPatchPaths) {
		t.Fatalf("manifest declares %d patches, this contract reviews %d; add the new patch's file list here",
			len(manifest.Patches), len(wantPatchPaths))
	}

	pathPattern := regexp.MustCompile(`(?m)^diff --git a/(\S+) b/(\S+)$`)
	for index, patch := range manifest.Patches {
		content := readAndVerifyDigest(t, patch)
		matches := pathPattern.FindAllStringSubmatch(content, -1)
		gotPaths := make([]string, 0, len(matches))
		for _, match := range matches {
			if match[1] != match[2] {
				t.Fatalf("GARM patch %d renames %q to %q", index+1, match[1], match[2])
			}
			gotPaths = append(gotPaths, match[1])
		}
		if !slices.Equal(gotPaths, wantPatchPaths[index]) {
			t.Fatalf("GARM patch %d scope = %v, want %v", index+1, gotPaths, wantPatchPaths[index])
		}
	}
}

// What each patch has to carry, stated as the symbols and strings that make its
// declared purpose true. A patch whose digest matches but whose behaviour was
// gutted would pass every other check here.
func TestEachPatchCarriesTheBehaviourItDeclares(t *testing.T) {
	t.Parallel()
	manifest := loadDerivativeManifest(t)
	content := make([]string, 0, len(manifest.Patches))
	for _, patch := range manifest.Patches {
		content = append(content, readAndVerifyDigest(t, patch))
	}

	required := [][]string{
		{
			"autoscaleWake", "signalAutoScale", "reconcileWake", "signalReconcile",
			"time.NewTicker(5 * time.Second)", "TestScaleSetUpdateSignalsAutoScale",
			"scaleDownProtected", "TestScaleDownProtectsStartupAndExecutionStates",
		},
		{
			"GetEntity", "ObserveLifecycle", "ObserveAvailable", "SelectForAcquire",
			"CompleteAcquire", "FailAcquire", "HasQueuedAvailable", "AcquireJobs",
			"retainAvailableMessage", "acquireAttempted",
		},
		{
			"RemoveScaleSetRunner", "InstanceError", "InstancePendingDelete", "AgentID",
			"TestFailedScaleSetInstanceRetainsAgentIDUntilTransientRemovalConverges",
		},
		{
			"withNDDevDirectJIT", "nddev_direct_jit", "nddev_encoded_jit_config",
			"static extra specs contain reserved field", "LeavesUnrelatedProvidersByteExact",
			"FailsClosed", "nddevDirectJITStartedTransitions",
			"RunnerInstalling, params.RunnerIdle, params.RunnerActive",
			"GitHub JobStarted message is authoritative",
			"transitions := []params.RunnerStatus{params.RunnerActive}",
		},
		{
			"direct JIT phase", "acquire-jobs-started", "acquire-jobs-completed",
			"acquire-jobs-failed", "provider-create-started", "provider-create-completed",
			"provider-create-failed", "duration_ms",
		},
	}
	if len(required) != len(content) {
		t.Fatalf("manifest declares %d patches, this contract states behaviour for %d", len(content), len(required))
	}
	for index, symbols := range required {
		for _, symbol := range symbols {
			if !strings.Contains(content[index], symbol) {
				t.Errorf("GARM patch %d is missing %q", index+1, symbol)
			}
		}
	}

	// Telemetry runs on every acquisition and every provider call, so a field
	// that carries a credential would put one in a log line on the hot path.
	for _, forbidden := range []string{"nddev_encoded_jit_config", "JitConfiguration", "InstanceToken", "CACertBundle"} {
		if strings.Contains(content[4], forbidden) {
			t.Errorf("GARM direct-JIT phase telemetry patch contains secret-bearing field %q", forbidden)
		}
	}

	// Admission has to be recorded before the job is acquired and a deferred
	// message has to survive being acknowledged, or a crash between the two
	// loses work that GitHub believes was taken. Ordering is the property; the
	// symbols above only prove the code is present.
	queue := content[1]
	lifecycleIndex := strings.Index(queue, "intentCoordinator.ObserveLifecycle")
	availableIndex := strings.Index(queue, "intentCoordinator.ObserveAvailable")
	acquireIndex := strings.Index(queue, "scaleSetClient.AcquireJobs")
	retainIndex := strings.Index(queue, "if retainAvailableMessage")
	lastMessageIndex := strings.Index(queue, "l.scaleSetHelper.SetLastMessageID")
	deleteMessageIndex := strings.Index(queue, "messageSession.DeleteMessage")
	if lifecycleIndex < 0 || availableIndex < 0 || acquireIndex < 0 ||
		!(lifecycleIndex < availableIndex && availableIndex < acquireIndex) {
		t.Fatal("GARM queue patch does not record lifecycle before available selection and acquisition")
	}
	if retainIndex < 0 || lastMessageIndex < 0 || deleteMessageIndex < 0 ||
		!(retainIndex < lastMessageIndex && retainIndex < deleteMessageIndex) {
		t.Fatal("GARM queue patch can acknowledge a message before deferred-retention check")
	}
}

// The queue coordinator is the one file this derivative replaces wholesale, so
// the properties that make it safe to do that are stated rather than assumed.
func TestQueueOverlayCarriesItsDurabilityProperties(t *testing.T) {
	t.Parallel()
	manifest := loadDerivativeManifest(t)
	if len(manifest.Overlays) == 0 {
		t.Fatal("no overlay declared")
	}
	coordinator := readAndVerifyDigest(t, manifest.Overlays[0])
	for _, required := range []string{
		"syscall.Flock", "queueSchedulerStride", "priority_aging_seconds",
		"os.Rename", "directoryHandle.Sync", "DisallowUnknownFields",
	} {
		if !strings.Contains(coordinator, required) {
			t.Errorf("GARM queue overlay is missing %q", required)
		}
	}
	for _, overlay := range manifest.Overlays[1:] {
		readAndVerifyDigest(t, overlay)
	}
}

// The build script is executable and generated from the manifest. That it is
// current is held by internal/garmderivative; that it can be run at all is held
// here, because a non-executable script fails only when somebody tries to build.
func TestBuildScriptIsExecutable(t *testing.T) {
	t.Parallel()
	info, err := os.Stat(filepath.Join("../..", garmderivative.DefaultScriptPath))
	if err != nil {
		t.Fatalf("stat GARM build script: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatal("GARM build script is not executable")
	}
}

func readAndVerifyDigest(t *testing.T, source garmderivative.Source) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("../..", filepath.FromSlash(source.Path)))
	if err != nil {
		t.Fatalf("read %s: %v", source.Path, err)
	}
	digest := sha256.Sum256(content)
	if got := hex.EncodeToString(digest[:]); got != source.SHA256 {
		t.Fatalf("%s digest = %s, manifest declares %s", source.Path, got, source.SHA256)
	}
	return string(content)
}
