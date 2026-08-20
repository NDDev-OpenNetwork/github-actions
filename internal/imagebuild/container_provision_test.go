package imagebuild

import (
	"strings"
	"testing"
)

func TestContainerMetadataBootstrapRetriesUntilRunnerServiceExists(t *testing.T) {
	t.Parallel()
	raw, err := scripts.ReadFile("assets/container-provision.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"cloud-init completed without installing the one-job runner service",
		"compgen -G '/etc/systemd/system/actions.runner.*.service'",
		"Restart=on-failure",
		"RestartSec=5s",
		"StartLimitIntervalSec=0",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("container metadata bootstrap is missing %q", required)
		}
	}
}
