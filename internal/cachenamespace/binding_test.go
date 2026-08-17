package cachenamespace_test

import (
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
