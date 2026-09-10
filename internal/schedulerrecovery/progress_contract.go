package schedulerrecovery

import (
	"fmt"
	"strings"
)

// validateProgress requires a complete, disjoint partition of the original
// identities. Empty output, omitted identities, duplicates and unrelated jobs
// are not proof of recovery. Adapters must separately prove each transition.
func validateProgress(expected, progressed, remaining []string) error {
	if len(expected) == 0 {
		return fmt.Errorf("recovery progress requires expected identities")
	}
	want := make(map[string]bool, len(expected))
	for _, id := range expected {
		if strings.TrimSpace(id) == "" || strings.ContainsAny(id, ",\x00\r\n") {
			return fmt.Errorf("invalid recovery identity")
		}
		if _, duplicate := want[id]; duplicate {
			return fmt.Errorf("duplicate expected recovery identity")
		}
		want[id] = false
	}
	for _, group := range [][]string{progressed, remaining} {
		for _, id := range group {
			seen, exists := want[id]
			if !exists || seen {
				return fmt.Errorf("progress contains an unexpected or repeated identity")
			}
			want[id] = true
		}
	}
	for _, seen := range want {
		if !seen {
			return fmt.Errorf("progress omits an expected recovery identity")
		}
	}
	return nil
}
