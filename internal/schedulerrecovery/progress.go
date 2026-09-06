package schedulerrecovery

import (
	"fmt"
	"strings"
)

// validateProgress requires a complete, disjoint partition of the exact attempt.
// Missing observations are unresolved work, never evidence of recovery.
func validateProgress(expected, progressed, remaining []string) error {
	if len(expected) == 0 {
		return fmt.Errorf("recovery requires at least one exact identity")
	}
	wanted := make(map[string]bool, len(expected))
	for _, id := range expected {
		if strings.TrimSpace(id) != id || id == "" || strings.ContainsAny(id, ",\x00\r\n") {
			return fmt.Errorf("recovery contains an invalid identity")
		}
		if _, exists := wanted[id]; exists {
			return fmt.Errorf("recovery contains a duplicate identity")
		}
		wanted[id] = false
	}
	for _, group := range [][]string{progressed, remaining} {
		for _, id := range group {
			seen, exists := wanted[id]
			if !exists {
				return fmt.Errorf("progress contains an identity outside the attempt")
			}
			if seen {
				return fmt.Errorf("progress contains duplicate or overlapping identities")
			}
			wanted[id] = true
		}
	}
	for _, seen := range wanted {
		if !seen {
			return fmt.Errorf("progress omits an identity from the attempt")
		}
	}
	return nil
}
