package cachenamespace_test

import (
	"os"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/cachenamespace"
	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
)

const repositoryRoot = "../.."

// The identity manifest and the canary workflow cannot call Go, so they carry
// the namespace as text. That is the residue of the twenty-four literals, and it
// is bound here rather than left to be noticed: a root that changed in the
// package and not in these two would grant one namespace and prove another.
func TestIdentityManifestUsesTheNamespaceThePackageBuilds(t *testing.T) {
	t.Parallel()
	config, err := rustfscache.Load(repositoryRoot + "/config/rustfs-cache-identities.yaml")
	if err != nil {
		t.Fatalf("load cache identities: %v", err)
	}
	roots := make(map[string]struct{}, len(cachenamespace.TrustClasses()))
	for _, root := range cachenamespace.PrefixRoots() {
		roots[root] = struct{}{}
	}
	if len(config.Identities) == 0 {
		t.Fatal("the identity manifest declares no identity")
	}
	for _, identity := range config.Identities {
		if _, known := roots[identity.Prefix]; !known {
			t.Errorf("identity %q is scoped to %q, which is not a namespace this package builds",
				identity.Role, identity.Prefix)
		}
	}
}

// The canary is the only end-to-end proof that a credential can write its own
// namespace and cannot write another's. It writes both keys as text, so a
// namespace change that missed it would leave the canary proving isolation
// between two namespaces nobody uses.
func TestCanaryProvesIsolationBetweenNamespacesThePackageBuilds(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(repositoryRoot + "/.github/workflows/self-hosted-canary.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)

	trusted := cachenamespace.MustPrefixRoot(cachenamespace.Trusted)
	untrusted := cachenamespace.MustPrefixRoot(cachenamespace.Untrusted)
	if !strings.Contains(workflow, trusted) {
		t.Errorf("the canary does not assert its own namespace %q", trusted)
	}
	// The cross-namespace key it must be refused.
	if !strings.Contains(workflow, untrusted+"/") {
		t.Errorf("the canary does not attempt the counterpart namespace %q", untrusted)
	}
	// Any trust-scoped path in the workflow has to be one this package builds.
	for _, field := range strings.Fields(workflow) {
		index := strings.Index(field, cachenamespace.Organization+"/"+cachenamespace.Repository+"/trust/")
		if index < 0 {
			continue
		}
		candidate := strings.Trim(field[index:], `"'`)
		matched := false
		for _, root := range cachenamespace.PrefixRoots() {
			if strings.HasPrefix(candidate, root) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("the canary names trust-scoped path %q, which no namespace this package builds covers", candidate)
		}
	}
}
