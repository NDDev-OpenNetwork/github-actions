package repositorycontract

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestToolCacheCompositePublishesStableInputsAndOutputs(t *testing.T) {
	root := toolCacheRepositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "actions/tool-cache/action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Inputs  map[string]any `yaml:"inputs"`
		Outputs map[string]any `yaml:"outputs"`
		Runs    struct {
			Using string `yaml:"using"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(raw, &action); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"url", "sha256", "output", "max-bytes"} {
		if _, exists := action.Inputs[input]; !exists {
			t.Fatalf("tool-cache input %q is missing", input)
		}
	}
	for _, output := range []string{"source", "bytes", "duration_ms"} {
		if _, exists := action.Outputs[output]; !exists {
			t.Fatalf("tool-cache output %q is missing", output)
		}
	}
	if action.Runs.Using != "composite" {
		t.Fatalf("tool-cache action uses %q", action.Runs.Using)
	}
}

func TestToolCacheUsesVerifiedUpstreamWithoutFleetAssignment(t *testing.T) {
	result := runToolCache(t, "none")
	if !strings.Contains(result.outputs, "source=upstream\n") || !strings.Contains(result.stdout, `"source":"upstream"`) {
		t.Fatalf("upstream result is not observable: outputs=%q stdout=%q", result.outputs, result.stdout)
	}
	if strings.TrimSpace(result.invocations) != "upstream" {
		t.Fatalf("unexpected curl path: %q", result.invocations)
	}
}

func TestToolCachePrefersVerifiedFleetObject(t *testing.T) {
	result := runToolCache(t, "hit")
	if !strings.Contains(result.outputs, "source=cache\n") || !strings.Contains(result.stdout, `"cache_result":"hit"`) {
		t.Fatalf("cache result is not observable: outputs=%q stdout=%q", result.outputs, result.stdout)
	}
	if strings.TrimSpace(result.invocations) != "cache-get" {
		t.Fatalf("cache hit reached another network path: %q", result.invocations)
	}
}

func TestToolCacheOutageFallsBackToVerifiedUpstream(t *testing.T) {
	result := runToolCache(t, "unavailable")
	if !strings.Contains(result.outputs, "source=upstream\n") || !strings.Contains(result.stdout, `"cache_result":"http-503+store-http-503"`) {
		t.Fatalf("cache outage did not degrade visibly: outputs=%q stdout=%q", result.outputs, result.stdout)
	}
	if strings.TrimSpace(result.invocations) != "cache-get\nupstream\ncache-put" {
		t.Fatalf("cache outage did not use the ordinary upstream path: %q", result.invocations)
	}
}

type toolCacheResult struct {
	outputs     string
	stdout      string
	invocations string
}

func runToolCache(t *testing.T, cacheMode string) toolCacheResult {
	t.Helper()
	root := toolCacheRepositoryRoot(t)
	directory := t.TempDir()
	runnerTemp := filepath.Join(directory, "runner-temp")
	bin := filepath.Join(directory, "bin")
	if err := os.MkdirAll(runnerTemp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := []byte("immutable verified tool artifact\n")
	fixturePath := filepath.Join(directory, "fixture")
	if err := os.WriteFile(fixturePath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(fixture))
	invocations := filepath.Join(directory, "curl-invocations")
	fakeCurl := `#!/usr/bin/env bash
set -euo pipefail
output=
upload=false
cache=false
while (($#)); do
  case "$1" in
    --output) output=$2; shift 2 ;;
    --upload-file) upload=true; shift 2 ;;
    --aws-sigv4) cache=true; shift 2 ;;
    --user|--cacert|--connect-timeout|--max-time|--max-filesize|--retry|--retry-max-time|--write-out|--request) shift 2 ;;
    --retry-all-errors|--silent|--show-error|--fail|--location|--proto|--tlsv1.2) shift ;;
    *) shift ;;
  esac
done
if [[ "$upload" == true ]]; then
  printf 'cache-put\n' >>"$FAKE_CURL_LOG"
  printf '%s' "$FAKE_CACHE_STATUS"
elif [[ "$cache" == true ]]; then
  printf 'cache-get\n' >>"$FAKE_CURL_LOG"
  if [[ "$FAKE_CACHE_STATUS" == 200 ]]; then cp "$FAKE_CURL_FIXTURE" "$output"; fi
  printf '%s' "$FAKE_CACHE_STATUS"
else
  printf 'upstream\n' >>"$FAKE_CURL_LOG"
  cp "$FAKE_CURL_FIXTURE" "$output"
fi
`
	fakeCurlPath := filepath.Join(bin, "curl")
	if err := os.WriteFile(fakeCurlPath, []byte(fakeCurl), 0o700); err != nil {
		t.Fatal(err)
	}
	githubOutput := filepath.Join(directory, "github-output")
	output := filepath.Join(runnerTemp, "tools", "tool.tar.gz")
	cacheStatus := "200"
	if cacheMode == "unavailable" {
		cacheStatus = "503"
	}
	command := exec.Command("bash", filepath.Join(root, "actions/tool-cache/tool-cache.sh"))
	command.Env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"RUNNER_TEMP="+runnerTemp,
		"GITHUB_OUTPUT="+githubOutput,
		"NDDEV_TOOL_CACHE_URL=https://github.com/example/tool/releases/download/v1/tool.tar.gz",
		"NDDEV_TOOL_CACHE_SHA256="+digest,
		"NDDEV_TOOL_CACHE_OUTPUT="+output,
		"NDDEV_TOOL_CACHE_MAX_BYTES=1024",
		"FAKE_CURL_FIXTURE="+fixturePath,
		"FAKE_CURL_LOG="+invocations,
		"FAKE_CACHE_STATUS="+cacheStatus,
	)
	if cacheMode != "none" {
		ca := filepath.Join(directory, "ca.pem")
		if err := os.WriteFile(ca, []byte("test CA"), 0o600); err != nil {
			t.Fatal(err)
		}
		command.Env = append(command.Env,
			"AWS_ACCESS_KEY_ID=AKIA0123456789ABCDEF",
			"AWS_SECRET_ACCESS_KEY="+strings.Repeat("a", 64),
			"AWS_REGION=us-east-1",
			"AWS_CA_BUNDLE="+ca,
			"SCCACHE_BUCKET=github-actions-cache",
			"SCCACHE_ENDPOINT=https://192.0.2.1:9002",
			"NDDEV_CACHE_DELIVERY_ID="+strings.Repeat("b", 64),
			"NDDEV_CACHE_ROLE=trusted-writer",
			"NDDEV_CACHE_MODE=read-write",
			"NDDEV_CACHE_PREFIX_ROOT=example/repository/trust/trusted",
		)
	}
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("tool-cache failed: %v\n%s", err, combined)
	}
	actual, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(fixture) {
		t.Fatal("tool-cache output differs from the verified fixture")
	}
	outputs, err := os.ReadFile(githubOutput)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(invocations)
	if err != nil {
		t.Fatal(err)
	}
	return toolCacheResult{outputs: string(outputs), stdout: string(combined), invocations: string(calls)}
}

func toolCacheRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve repository root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}
