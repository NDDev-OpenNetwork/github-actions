package repositorycontract

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/garmderivative"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerrelease"
)

const upstreamBaselinePath = repositoryRoot + "/docs/upstream-baseline.md"

// docs/upstream-baseline.md is the table an operator reads to answer "is the
// binary on this host the one this tree describes". It got that wrong: commit
// 606b444 bumped the GARM row from nddev.10 to nddev.11 and updated the manifest
// digest to 94cd41917c..., but left the row's NDDev binary digest at
// 2724649896..., which is nddev.10's. Anyone verifying a correct binary against
// the table would have found a mismatch and been unable to tell drift from
// tampering.
//
// The two bumps before it moved both. So the failure mode is not that the table
// is maintained badly; it is that nothing made the two follow each other.
func readUpstreamBaseline(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(upstreamBaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// row returns the table row whose first cell is the named component.
func row(t *testing.T, baseline, component string) string {
	t.Helper()
	for _, line := range strings.Split(baseline, "\n") {
		if !strings.HasPrefix(line, "| "+component+" |") {
			continue
		}
		return line
	}
	t.Fatalf("docs/upstream-baseline.md has no %q row", component)
	return ""
}

// mustState fails unless the row contains the value, naming what it should have
// said. A digest that is merely absent is as bad as one that is wrong: the row
// exists to let somebody check.
func mustState(t *testing.T, row, component, field, want string) {
	t.Helper()
	if !strings.Contains(row, want) {
		t.Errorf("%s row does not state %s %s; the manifest declares it and the table has to agree", component, field, want)
	}
}

func TestBaselineTableStatesTheGARMDerivativeTheManifestDeclares(t *testing.T) {
	t.Parallel()
	manifest, err := garmderivative.Load(repositoryRoot + "/" + garmderivative.DefaultManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	baseline := readUpstreamBaseline(t)
	garm := row(t, baseline, "GARM")

	mustState(t, garm, "GARM", "derivative version", manifest.DerivativeVersion)
	mustState(t, garm, "GARM", "upstream release", manifest.Upstream.Release)
	mustState(t, garm, "GARM", "upstream commit", manifest.Upstream.Commit)
	mustState(t, garm, "GARM", "upstream asset digest", manifest.Upstream.ReleaseAssetSHA256)
	mustState(t, garm, "GARM", "NDDev binary digest", manifest.Build.BinarySHA256)

	// A superseded digest left in the row is the exact shape of the defect, and
	// it reads as current because it sits where the current one belongs.
	for _, digest := range digestsIn(garm) {
		switch digest {
		case manifest.Upstream.ReleaseAssetSHA256, manifest.Build.BinarySHA256:
		default:
			t.Errorf("GARM row states digest %s, which is neither the upstream asset nor the NDDev binary the manifest declares", digest)
		}
	}
}

func TestBaselineTableStatesTheProviderTheManifestDeclares(t *testing.T) {
	t.Parallel()
	manifest, err := providerrelease.Load(repositoryRoot + "/" + providerrelease.DefaultManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	baseline := readUpstreamBaseline(t)
	provider := row(t, baseline, "GARM Incus provider")

	mustState(t, provider, "GARM Incus provider", "derivative version", manifest.DerivativeVersion)
	mustState(t, provider, "GARM Incus provider", "upstream release", manifest.Upstream.Release)
	mustState(t, provider, "GARM Incus provider", "upstream commit", manifest.Upstream.Commit)
	mustState(t, provider, "GARM Incus provider", "upstream asset digest", manifest.Upstream.ReleaseAssetSHA256)

	for _, digest := range digestsIn(provider) {
		if digest != manifest.Upstream.ReleaseAssetSHA256 {
			t.Errorf("provider row states digest %s, which the manifest does not declare", digest)
		}
	}
}

var sha256Pattern = regexp.MustCompile(`\bsha256:([0-9a-f]{64})\b`)

func digestsIn(row string) []string {
	matches := sha256Pattern.FindAllStringSubmatch(row, -1)
	digests := make([]string, 0, len(matches))
	for _, match := range matches {
		digests = append(digests, match[1])
	}
	return digests
}
