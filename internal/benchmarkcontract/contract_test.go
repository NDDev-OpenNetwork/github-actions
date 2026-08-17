package benchmarkcontract

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NDDev-OpenNetwork/github-actions/internal/imagemanifest"
)

const (
	repositoryRoot    = "../.."
	workflowPath      = "../../.github/workflows/representative-benchmark.yml"
	metricsPath       = "../../scripts/benchmark-metrics.sh"
	installerPath     = "../../scripts/install-benchmark-toolchain.sh"
	sccachePath       = "../../scripts/configure-sccache.sh"
	sccacheSeriesPath = "../../scripts/sccache-statistical-series.sh"
)

type workflowSpec struct {
	On          map[string]any    `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Concurrency struct {
		CancelInProgress bool `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Name           string         `yaml:"name"`
	If             string         `yaml:"if"`
	RunsOn         string         `yaml:"runs-on"`
	TimeoutMinutes int            `yaml:"timeout-minutes"`
	Env            map[string]any `yaml:"env"`
	Steps          []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Name string         `yaml:"name"`
	If   string         `yaml:"if"`
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
}

func TestRepresentativeWorkflowIsManualPinnedAndLeastPrivilege(t *testing.T) {
	raw := read(t, workflowPath)
	var workflow workflowSpec
	if err := yaml.Unmarshal([]byte(raw), &workflow); err != nil {
		t.Fatal(err)
	}
	if len(workflow.On) != 1 {
		t.Fatalf("benchmark workflow must expose only one event: %#v", workflow.On)
	}
	if _, exists := workflow.On["workflow_dispatch"]; !exists {
		t.Fatalf("benchmark workflow is not manual-only: %#v", workflow.On)
	}
	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		t.Fatalf("benchmark permissions are not least privilege: %#v", workflow.Permissions)
	}
	if workflow.Concurrency.CancelInProgress {
		t.Fatal("benchmark samples must not cancel one another")
	}

	expectedWorkloads := map[string]string{
		"go":        "go",
		"rust":      "rust",
		"python-uv": "python-uv",
		"bun-next":  "bun-next",
		"docker":    "docker",
	}
	if len(workflow.Jobs) != len(expectedWorkloads) {
		t.Fatalf("expected exactly five representative jobs, got %d", len(workflow.Jobs))
	}
	usesCount := make(map[string]int)
	for jobID, workload := range expectedWorkloads {
		job, exists := workflow.Jobs[jobID]
		if !exists {
			t.Errorf("workflow is missing %s job", jobID)
			continue
		}
		if job.RunsOn != "ubuntu-24.04" || job.TimeoutMinutes != 30 {
			t.Errorf("%s runner/timeout contract drifted: %#v", jobID, job)
		}
		expectedSelector := "${{ inputs.workload == 'all' || inputs.workload == '" + jobID + "' }}"
		if job.If != expectedSelector {
			t.Errorf("%s workload selector drifted: %q", jobID, job.If)
		}
		if got := fmt.Sprint(job.Env["NDDEV_BENCHMARK_WORKLOAD"]); got != workload {
			t.Errorf("%s workload identity is %q", jobID, got)
		}

		stepsByName := make(map[string]workflowStep)
		for _, step := range job.Steps {
			stepsByName[step.Name] = step
			if step.Uses != "" {
				usesCount[step.Uses]++
				if !regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`).MatchString(step.Uses) {
					t.Errorf("%s action is not pinned to a full commit: %q", jobID, step.Uses)
				}
			}
		}
		for _, name := range []string{
			"Check out source", "Start benchmark metrics", "Restore dependency cache",
			"Resolve dependencies", "Build workload", "Test workload",
			"Finish benchmark metrics", "Upload benchmark record",
		} {
			if _, exists := stepsByName[name]; !exists {
				t.Errorf("%s is missing the %q phase", jobID, name)
			}
		}
		checkout := stepsByName["Check out source"]
		if fmt.Sprint(checkout.With["persist-credentials"]) != "false" || fmt.Sprint(checkout.With["fetch-depth"]) != "1" {
			t.Errorf("%s checkout can retain credentials or history: %#v", jobID, checkout.With)
		}
		cache := stepsByName["Restore dependency cache"]
		expectedCacheCondition := "${{ inputs.cache_mode == 'warm' }}"
		if cache.If != expectedCacheCondition {
			t.Errorf("%s can restore/save a cache during a cold sample: %q", jobID, cache.If)
		}
		upload := stepsByName["Upload benchmark record"]
		if fmt.Sprint(upload.With["retention-days"]) != "1" || fmt.Sprint(upload.With["if-no-files-found"]) != "error" {
			t.Errorf("%s artifact retention contract drifted: %#v", jobID, upload.With)
		}
	}

	expectedUses := map[string]int{
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1":        5,
		"actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e":        1,
		"actions/setup-python@5fda3b95a4ea91299a34e894583c3862153e4b97":    1,
		"actions/cache@55cc8345863c7cc4c66a329aec7e433d2d1c52a9":           5,
		"actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a": 5,
	}
	if !equalStringIntMaps(usesCount, expectedUses) {
		t.Fatalf("benchmark action set drifted:\nactual: %#v\nexpected: %#v", usesCount, expectedUses)
	}
	for action := range usesCount {
		if !strings.HasPrefix(action, "actions/") {
			t.Errorf("repository policy permits only GitHub-owned actions: %q", action)
		}
	}
	for _, required := range []string{
		"workload:", "- all", "- go", "- rust", "- python-uv", "- bun-next", "- docker",
		"scripts/install-benchmark-toolchain.sh rust",
		"scripts/install-benchmark-toolchain.sh uv",
		"scripts/install-benchmark-toolchain.sh bun",
	} {
		if !strings.Contains(raw, required) {
			t.Errorf("workflow is missing checksum-pinned toolchain installation %q", required)
		}
	}
	for _, forbidden := range []string{
		"pull_request_target", "\n  push:", "\n  pull_request:", "\n  schedule:",
		"secrets.", "github.token", "permissions: write", "--privileged",
		"/var/run/docker.sock", "/run/incus/unix.socket", "sudo ",
		"actions-rust-lang/", "astral-sh/setup-uv", "oven-sh/setup-bun",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("benchmark workflow contains forbidden capability %q", forbidden)
		}
	}
}

func TestNDDevSccacheAdapterIsPinnedScopedAndFailClosed(t *testing.T) {
	info, err := os.Stat(sccachePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("sccache adapter mode is not executable: %o", info.Mode().Perm())
	}
	raw := read(t, sccachePath)
	for _, required := range []string{
		"set -Eeuo pipefail", "set +x", "umask 077", "sccache 0.17.0",
		"NDDEV_CACHE_PREFIX_ROOT", "GITHUB_REPOSITORY", "linux/amd64",
		"sha256sum -- \"${lock_path}\"", "SCCACHE_S3_KEY_PREFIX",
		"SCCACHE_S3_RW_MODE", "READ_ONLY", "READ_WRITE", "SCCACHE_CLIENT_SIDE=1",
		"release-reader:read-only:release", "trusted-writer:read-write:benchmark",
		"lock file is outside GITHUB_WORKSPACE", "NDDEV_SCCACHE_NAMESPACE_SHA256",
	} {
		if !strings.Contains(raw, required) {
			t.Errorf("sccache adapter is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"set -x", "eval ", "curl ", "wget ", "latest", "GITHUB_TOKEN", "github.token",
		"AWS_SECRET_ACCESS_KEY=%", "AWS_ACCESS_KEY_ID=%", "sudo ",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("sccache adapter contains forbidden capability %q", forbidden)
		}
	}
	workflow := read(t, workflowPath)
	for _, required := range []string{
		"scripts/configure-sccache.sh benchmark/rust/Cargo.lock rustc-1.97.1 benchmark",
		"sccache --show-stats --stats-format json", ".stats.cache_hits.counts[]",
		"sccache --stop-server", "LOCAL_CACHE_HIT",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("representative workflow is missing sccache proof %q", required)
		}
	}
}

func TestSccacheStatisticalSeriesIsStrictAndResumable(t *testing.T) {
	info, err := os.Stat(sccacheSeriesPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("sccache statistical harness mode is not executable: %o", info.Mode().Perm())
	}
	raw := read(t, sccacheSeriesPath)
	for _, required := range []string{
		"set -Eeuo pipefail", "GHA_REQUIRED_SAMPLES:-20", "GHA_MAX_LOAD_1:-4",
		"required_cache_hit_rate_percent_exclusive:70", "required_median_speedup_factor:3",
		"workflow_dispatch", "-f workload=rust", "-f environment=nddev",
		".conclusion == \"success\"", "expected one artifact", "artifact_digest",
		"machine_id_sha256", "sccache hits=", "github_log_secret_shape_matches",
		"diagnostic_export_sync.state == \"synchronized\"", "legacy_runners.listeners == 12",
		"unique_machine_ids", "unique_artifact_digests", "example_platform_healthy", "captcha_healthy",
		"failed_systemd_units", "github_runner_registrations",
		"cache_hit_rate_gate_complete", "median_speedup_gate_complete",
		"statistical_cache_gate_complete", "jq -e '.verdict.statistical_cache_gate_complete'",
	} {
		if !strings.Contains(raw, required) {
			t.Errorf("sccache statistical harness is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"GHA_REQUIRED_SAMPLES:-1", "required_median_speedup_factor:1", "cancel-in-progress: true",
		"/var/run/docker.sock", "/run/incus/unix.socket", "GITHUB_TOKEN=", "set -x",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("sccache statistical harness contains forbidden behavior %q", forbidden)
		}
	}
}

func TestToolchainInstallerPinsArtifactsAndNeverReceivesWorkflowCredentials(t *testing.T) {
	info, err := os.Stat(installerPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("toolchain installer mode is not executable: %o", info.Mode().Perm())
	}
	raw := read(t, installerPath)
	for _, required := range []string{
		"set -euo pipefail", "umask 077", "download_verified", "--proto '=https'", "--tlsv1.2",
		"https://static.rust-lang.org/rustup/archive/1.28.2/x86_64-unknown-linux-gnu/rustup-init",
		"20a06e644b0d9bd2fbdbfd52d42540bdde820ea7df86e92e533c073da0cdd43c",
		"https://github.com/astral-sh/uv/releases/download/0.11.30/uv-x86_64-unknown-linux-gnu.tar.gz",
		"04bc7d180d6138bf6dc08387acf507a823f397a98fea55da36b0ccc7fbce3b68",
		"https://github.com/oven-sh/bun/releases/download/bun-v1.3.14/bun-linux-x64.zip",
		"951ee2aee855f08595aeec6225226a298d3fea83a3dcd6465c09cbccdf7e848f",
		"--default-toolchain 1.97.1", "uv 0.11.30", "1.3.14",
		"Linux x86_64", "find \"${scratch}\" -mindepth 1 -delete",
	} {
		if !strings.Contains(raw, required) {
			t.Errorf("toolchain installer is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"| sh", "| bash", "eval ", "sudo ", "latest", "GITHUB_TOKEN", "github.token",
		"password", "secret", "authorization:",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("toolchain installer contains forbidden text %q", forbidden)
		}
	}
}

// TestBakedToolchainsMatchBenchmarkPins is the invariant that makes baking pay
// off. Each representative installer exits early only when the pinned version
// is already reported, and actions/setup-go only skips its download when the
// requested version is already in the runner tool cache. If an image manifest
// pin and a benchmark pin ever diverge, every job silently resumes downloading
// and installing that toolchain while the images still carry the unused copy.
func TestBakedToolchainsMatchBenchmarkPins(t *testing.T) {
	installer := read(t, installerPath)
	workflow := read(t, workflowPath)
	for _, manifestFile := range []string{"golden-image.yaml", "golden-image-integration.yaml"} {
		manifest, err := imagemanifest.Load(filepath.Join(repositoryRoot, "config", manifestFile))
		if err != nil {
			t.Fatalf("load %s: %v", manifestFile, err)
		}
		pinned := make(map[string]string, len(manifest.Toolchains))
		for _, toolchain := range manifest.Toolchains {
			pinned[toolchain.Name] = toolchain.Version
		}
		if !slices.Equal(slices.Sorted(maps.Keys(pinned)), imagemanifest.BakedToolchains()) {
			t.Fatalf("%s does not bake the exact toolchain set: %v", manifestFile, pinned)
		}
		for _, shortCircuit := range []string{
			`"$(rustc --version 2>/dev/null || true)" == "rustc ` + pinned["rust"] + ` "*`,
			`"$(cargo --version 2>/dev/null || true)" == "cargo ` + pinned["rust"] + ` "*`,
			`"$(uv --version 2>/dev/null || true)" == "uv ` + pinned["uv"] + `"*`,
			`"$(bun --version 2>/dev/null || true)" == "` + pinned["bun"] + `"`,
		} {
			if !strings.Contains(installer, shortCircuit) {
				t.Errorf("%s: benchmark installer has no short-circuit for %q", manifestFile, shortCircuit)
			}
		}
		if !strings.Contains(workflow, "go-version: "+pinned["go"]) {
			t.Errorf("%s: benchmark workflow does not request baked Go %s", manifestFile, pinned["go"])
		}
	}
}

func TestDockerBenchmarkCacheIsPortableAcrossDefaultDrivers(t *testing.T) {
	raw := read(t, workflowPath)
	for _, required := range []string{
		"BUILDKIT_CACHE_FILE=%s/nddev-benchmark-cache/docker-image.tar",
		"BENCHMARK_CACHE_IMAGE: nddev-runner-benchmark:cache-v2",
		"representative-v2-${{ inputs.environment }}-docker-",
		`docker load --input "${BUILDKIT_CACHE_FILE}"`,
		`docker image inspect "${BENCHMARK_CACHE_IMAGE}"`,
		`cache_from=(--cache-from "${BENCHMARK_CACHE_IMAGE}")`,
		"--build-arg BUILDKIT_INLINE_CACHE=1",
		`docker save --output "${cache_next}" "${BENCHMARK_CACHE_IMAGE}"`,
		`chmod 0600 -- "${cache_next}"`,
	} {
		if !strings.Contains(raw, required) {
			t.Errorf("Docker benchmark cache contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"BUILDKIT_CACHE_DIR", "type=local", "docker buildx create",
		"--driver docker-container", "--cache-to type=gha",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("Docker benchmark cache depends on a non-portable capability %q", forbidden)
		}
	}
}

func TestMetricsRecorderRejectsAmbiguityAndProducesBoundedJSON(t *testing.T) {
	info, err := os.Stat(metricsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("metrics recorder mode is not executable: %o", info.Mode().Perm())
	}
	raw := read(t, metricsPath)
	for _, required := range []string{
		"set -euo pipefail", "umask 077", "sha256sum /etc/machine-id",
		"^(go|rust|python-uv|bun-next|docker)$", "network_rx_bytes",
		"schema_version: 1", "chmod 0600",
	} {
		if !strings.Contains(raw, required) {
			t.Errorf("metrics recorder is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"set -x", "eval ", "curl ", "/etc/gha-fleet", "credentials", "password", "secret",
	} {
		if strings.Contains(strings.ToLower(raw), forbidden) {
			t.Errorf("metrics recorder contains forbidden text %q", forbidden)
		}
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatalf("jq is required to verify benchmark records: %v", err)
	}

	temporary := t.TempDir()
	common := map[string]string{
		"NDDEV_BENCHMARK_WORKLOAD":    "go",
		"NDDEV_BENCHMARK_ENVIRONMENT": "github-hosted",
		"NDDEV_BENCHMARK_CACHE_MODE":  "cold",
		"NDDEV_BENCHMARK_ITERATION":   "cold-01",
		"RUNNER_TEMP":                 temporary,
	}
	runScript(t, common, "start")
	finish := cloneMap(common)
	finish["NDDEV_BENCHMARK_CACHE_HIT"] = "disabled"
	finish["NDDEV_BENCHMARK_TOOLCHAIN"] = "go version go1.26.5 linux/amd64"
	finish["GITHUB_SHA"] = strings.Repeat("a", 40)
	finish["GITHUB_RUN_ID"] = "12345"
	finish["GITHUB_RUN_ATTEMPT"] = "1"
	runScript(t, finish, "finish")

	resultPath := filepath.Join(temporary, "nddev-benchmark-go", "result.json")
	resultInfo, err := os.Stat(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if resultInfo.Mode().Perm() != 0o600 {
		t.Fatalf("benchmark result mode is %o, expected 600", resultInfo.Mode().Perm())
	}
	var record struct {
		SchemaVersion   int    `json:"schema_version"`
		Workload        string `json:"workload"`
		Environment     string `json:"environment"`
		CacheMode       string `json:"cache_mode"`
		CacheHit        string `json:"cache_hit"`
		Commit          string `json:"commit"`
		RunID           int64  `json:"run_id"`
		RunAttempt      int64  `json:"run_attempt"`
		ElapsedNS       int64  `json:"elapsed_ns"`
		NetworkRXBytes  int64  `json:"network_rx_bytes"`
		MachineIDSHA256 string `json:"machine_id_sha256"`
	}
	if err := json.Unmarshal([]byte(read(t, resultPath)), &record); err != nil {
		t.Fatal(err)
	}
	if record.SchemaVersion != 1 || record.Workload != "go" || record.Environment != "github-hosted" ||
		record.CacheMode != "cold" || record.CacheHit != "disabled" || record.Commit != strings.Repeat("a", 40) ||
		record.RunID != 12345 || record.RunAttempt != 1 || record.ElapsedNS < 0 || record.NetworkRXBytes < 0 {
		t.Fatalf("unexpected benchmark record: %#v", record)
	}
	if record.MachineIDSHA256 != "unavailable" && !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(record.MachineIDSHA256) {
		t.Fatalf("machine identity is not safely hashed: %q", record.MachineIDSHA256)
	}

	invalid := cloneMap(common)
	invalid["NDDEV_BENCHMARK_ITERATION"] = "../escape"
	command := exec.Command(metricsPath, "start")
	command.Env = mergeEnvironment(invalid)
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("invalid iteration was accepted: %s", output)
	}
}

func TestFixtureDependenciesAndImagesAreLocked(t *testing.T) {
	checks := map[string][]string{
		"benchmark/rust/Cargo.toml": {
			`doctest = false`, `anyhow = "=1.0.104"`, `rayon = "=1.12.0"`,
			`serde_json = "=1.0.151"`, `sha2 = "=0.11.0"`,
		},
		"benchmark/rust/Cargo.lock": {
			`name = "anyhow"`, `version = "1.0.104"`, `name = "rayon"`,
			`version = "1.12.0"`, `name = "sha2"`, `version = "0.11.0"`,
		},
		"benchmark/python/pyproject.toml": {
			`httpx==0.28.1`, `pydantic==2.13.4`, `pytest==9.1.1`, `hatchling==1.28.0`,
		},
		"benchmark/python/uv.lock": {
			`name = "httpx"`, `version = "0.28.1"`, `name = "pydantic"`,
			`version = "2.13.4"`, `name = "pytest"`, `version = "9.1.1"`,
		},
		"benchmark/bun-next/package.json": {
			`"packageManager": "bun@1.3.14"`, `"next": "16.3.0"`,
			`"react": "19.2.8"`, `"typescript": "5.9.3"`,
		},
		"benchmark/bun-next/bun.lock": {
			`"lockfileVersion": 1`, `"next": "16.3.0"`, `"@types/bun": "1.3.14"`,
		},
		"benchmark/docker/Dockerfile": {
			"ubuntu:24.04@sha256:561618e2c15bf2397621dd04f96926663a3b5616c189cf7e38db7e82f5c538ea",
		},
		"benchmark/docker/compose.yml": {
			"nginx:1.29-alpine@sha256:5616878291a2eed594aee8db4dade5878cf7edcb475e59193904b198d9b830de",
			"read_only: true", "no-new-privileges:true", "cap_drop:",
		},
	}
	for relative, required := range checks {
		raw := read(t, filepath.Join(repositoryRoot, relative))
		for _, value := range required {
			if !strings.Contains(raw, value) {
				t.Errorf("%s is missing locked contract %q", relative, value)
			}
		}
		for _, mutable := range []string{"@main", "@master", ":latest"} {
			if strings.Contains(raw, mutable) {
				t.Errorf("%s contains mutable reference %q", relative, mutable)
			}
		}
	}
	readme := read(t, filepath.Join(repositoryRoot, "benchmark/README.md"))
	for _, required := range []string{"20 cold", "20 cache-hit", "GitHub Jobs API", "hashed machine identity"} {
		if !strings.Contains(readme, required) {
			t.Errorf("benchmark protocol is missing %q", required)
		}
	}
}

func runScript(t *testing.T, environment map[string]string, mode string) {
	t.Helper()
	command := exec.Command(metricsPath, mode)
	command.Env = mergeEnvironment(environment)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("metrics recorder %s failed: %v\n%s", mode, err, output)
	}
}

func mergeEnvironment(overrides map[string]string) []string {
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	result := slices.DeleteFunc(os.Environ(), func(value string) bool {
		for _, key := range keys {
			if strings.HasPrefix(value, key+"=") {
				return true
			}
		}
		return false
	})
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func cloneMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func equalStringIntMaps(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
