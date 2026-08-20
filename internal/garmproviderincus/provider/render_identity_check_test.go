package provider

import (
	"strings"
	"testing"
)

// The refactor that made the namespace one authority must not have changed a
// byte the guest executes. These are the exact three lines the two jq
// expressions carried as literals before, so if the rendered clause differs in
// spacing, ordering or quoting, the guest is running something else.
func TestRenderedRoleClauseIsWhatTheGuestUsedToCarry(t *testing.T) {
	t.Parallel()
	want := `    (.role == "trusted-writer" and .mode == "read-write" and .prefix_root == "example-org/example-actions/trust/trusted") or
    (.role == "untrusted-writer" and .mode == "read-write" and .prefix_root == "example-org/example-actions/trust/untrusted") or
    (.role == "release-reader" and .mode == "read-only" and .prefix_root == "example-org/example-actions/trust/promoted")`

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
	}
}
