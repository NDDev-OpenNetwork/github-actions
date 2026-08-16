package deploycontract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The provider's compatibility probe checks that /etc/garm/cache is owned by the
// group the deployment grants. It used to check the caller's own effective gid,
// so it asked a different question and answered it correctly only when run as
// the service account -- as root, which is how the runbook runs it, it demanded
// 0:0 and failed on a correctly deployed directory (#230).
//
// The expected group is now a constant, which means it can drift from the
// tmpfiles line that creates the directory. This is what stops that.
func TestProbeExpectsTheGroupTheDeploymentGrants(t *testing.T) {
	t.Parallel()
	tmpfiles, err := os.ReadFile(filepath.Join("..", "..", "deploy", "fleet-host", "gha-fleet.tmpfiles"))
	if err != nil {
		t.Fatal(err)
	}
	// d /etc/garm/cache 0750 root garm -
	line := regexp.MustCompile(`(?m)^d\s+/etc/garm/cache\s+(\S+)\s+(\S+)\s+(\S+)`)
	match := line.FindStringSubmatch(string(tmpfiles))
	if match == nil {
		t.Fatal("gha-fleet.tmpfiles does not create /etc/garm/cache; the probe checks a directory nothing ships")
	}
	mode, owner, group := match[1], match[2], match[3]
	if mode != "0750" {
		t.Errorf("/etc/garm/cache is shipped %s; the probe requires 0750", mode)
	}
	if owner != "root" {
		t.Errorf("/etc/garm/cache is shipped owned by %q; the probe requires root", owner)
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "garmproviderincus", "provider", "cache_delivery.go"))
	if err != nil {
		t.Fatal(err)
	}
	// Comment lines are scanned out. The comment above the constant explains what
	// os.Getegid() got wrong, and naming the defect is not committing it.
	var code strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code.WriteString(line)
		code.WriteString("\n")
	}
	provider := code.String()
	want := `const cacheCredentialGroup = "` + group + `"`
	if !strings.Contains(provider, want) {
		t.Errorf("tmpfiles grants group %q on /etc/garm/cache, and the provider does not expect it; want %s", group, want)
	}
	// The old form is what made the answer depend on who was asking.
	if strings.Contains(provider, "os.Getegid()") {
		t.Error("the provider derives the expected group from the caller's effective gid again; " +
			"that makes the probe pass as the service account and fail as root")
	}
}
