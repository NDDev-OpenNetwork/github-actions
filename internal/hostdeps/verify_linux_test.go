//go:build linux

package hostdeps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNestedDockerRequiresBridgeNetfilterModuleAndControl(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := verifyNestedDockerAt(root); err == nil || !strings.Contains(err.Error(), "not loaded") {
		t.Fatalf("missing module was accepted: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sys/module/br_netfilter"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyNestedDockerAt(root); err == nil || !strings.Contains(err.Error(), "control is unavailable") {
		t.Fatalf("missing bridge control was accepted: %v", err)
	}
	controlRoot := filepath.Join(root, "proc/sys/net/bridge")
	if err := os.MkdirAll(controlRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bridge-nf-call-arptables", "bridge-nf-call-ip6tables", "bridge-nf-call-iptables"} {
		if err := os.WriteFile(filepath.Join(controlRoot, name), []byte("0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyNestedDockerAt(root); err != nil {
		t.Fatalf("complete nested Docker host contract was refused: %v", err)
	}
}
