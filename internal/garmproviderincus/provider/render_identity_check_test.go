package provider

import (
	"strings"
	"testing"
)

// The guest validates the public namespace shape and trust suffix without
// compiling any deployment tenant identity into the provider artifact.
func TestRenderedRoleClauseIsTenantNeutral(t *testing.T) {
	t.Parallel()
	want := `    (.role == "trusted-writer" and .mode == "read-write" and (.prefix_root | test("^[^/]+/[^/]+/trust/trusted$"))) or
    (.role == "untrusted-writer" and .mode == "read-write" and (.prefix_root | test("^[^/]+/[^/]+/trust/untrusted$"))) or
    (.role == "release-reader" and .mode == "read-only" and (.prefix_root | test("^[^/]+/[^/]+/trust/promoted$")))`

	if got := cacheRoleJQClause(); got != want {
		t.Fatalf("rendered clause differs from what the guest carried:\ngot:\n%s\nwant:\n%s", got, want)
	}
	for _, script := range map[string]string{
		"job-started hook": cacheJobStartedHook(),
		"setup script":     string(renderCacheSetupScript()),
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s does not carry the role clause", script[:40])
		}
		if strings.Contains(script, cacheRoleClausePlaceholder) {
			t.Errorf("%s still holds an unsubstituted placeholder", script[:40])
		}
		if strings.Contains(script, "example-org/example-actions") {
			t.Errorf("%s compiles the public example tenant into runtime authorization", script[:40])
		}
	}
}
