package deploycontract

import (
	"os"
	"strings"
	"testing"
)

func TestDirectJITLatencyHarnessSeparatesWorkflowAndProviderIdentity(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/direct-jit-latency-series.sh")
	if err != nil {
		t.Fatalf("read direct-JIT latency harness: %v", err)
	}
	script := string(raw)
	for _, required := range []string{
		`head_sha=$(gh api "repos/${repository}/commits/${ref}" --jq .sha)`,
		`user.nddev.provider-version`,
		`user.nddev.provider-commit`,
		`IFS=$'\t' read -r provider_version provider_commit`,
		`[[ ${provider_version} == "${expected_provider_version}" ]]`,
		`[[ ${provider_commit} =~ ^[0-9a-f]{40}$ ]]`,
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("latency harness is missing identity invariant %q", required)
		}
	}
	if strings.Contains(script, "provider_commit=$(git rev-parse HEAD)") {
		t.Fatal("latency harness must not conflate the workflow HEAD with the deployed provider commit")
	}
}
