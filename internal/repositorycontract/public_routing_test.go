package repositorycontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repositoryRoot = "../.."

func TestPublicRepositoryHasNoPrivateFleetRouting(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob(repositoryRoot + "/.github/workflows/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("public repository has no workflows to validate")
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(raw))
		for _, forbidden := range []string{"self-hosted", "nddev-linux", "amsterdam"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("public workflow %s contains private runner identity %q", filepath.Base(path), forbidden)
			}
		}
	}
}
