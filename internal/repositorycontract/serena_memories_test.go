package repositorycontract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	repositoryAnchorPath = repositoryRoot + "/.gds/repository.yaml"
	memoryDirectory      = repositoryRoot + "/.serena/memories"
	memoryIndex          = "CORE-01-INDEX.md"
)

// The anchor declares whether this repository carries durable agent knowledge.
// It said it did while `.serena/memories/` did not exist, so an agent following
// the generated instructions was sent to read a source that was not there
// (#251). Either the declaration or the tree has to move; this makes sure they
// move together.
func TestSerenaAnchorAndTheMemorySetAgree(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(repositoryAnchorPath)
	if err != nil {
		t.Fatal(err)
	}
	var anchor struct {
		Agent struct {
			Serena struct {
				Enabled            bool `yaml:"enabled"`
				ProvenanceRequired bool `yaml:"provenance_required"`
			} `yaml:"serena"`
		} `yaml:"agent"`
	}
	if err := yaml.Unmarshal(raw, &anchor); err != nil {
		t.Fatalf("parse repository anchor: %v", err)
	}

	entries, readErr := os.ReadDir(memoryDirectory)
	present := readErr == nil && len(memoryFiles(entries)) > 0

	switch {
	case anchor.Agent.Serena.Enabled && !present:
		t.Fatal(".gds/repository.yaml declares agent.serena.enabled, and .serena/memories/ holds no memory; " +
			"an agent following the generated instructions is sent to a source that does not exist")
	case !anchor.Agent.Serena.Enabled && present:
		t.Fatal(".serena/memories/ holds memories the anchor does not declare, so nothing keeps them current")
	}
	if !anchor.Agent.Serena.Enabled {
		return
	}
	if anchor.Agent.Serena.ProvenanceRequired && !containsIndex(entries) {
		t.Fatalf("provenance_required is declared but %s is missing; a memory set with no index "+
			"cannot say what it covers", memoryIndex)
	}
}

// A memory that states nothing, or that reads as a plan rather than a fact, is
// worse than none: it is trusted like the rest. This holds the cheap properties
// -- there is content, it is a document, and it does not carry the tense of
// something that has not happened.
func TestEveryMemoryStatesSomethingThatIsTrueNow(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(memoryDirectory)
	if err != nil {
		t.Skip("no memory set to check")
	}
	files := memoryFiles(entries)
	if len(files) == 0 {
		t.Skip("no memory set to check")
	}
	namePattern := regexp.MustCompile(`^[A-Z]+-[0-9]{2}-[A-Z0-9-]+\.md$`)
	for _, name := range files {
		if !namePattern.MatchString(name) {
			t.Errorf("memory %q does not follow AREA-NN-SLUG.md", name)
		}
		content, readErr := os.ReadFile(filepath.Join(memoryDirectory, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		body := string(content)
		if len(strings.TrimSpace(body)) < 200 {
			t.Errorf("memory %q is too short to carry a fact worth storing", name)
		}
		if !strings.HasPrefix(strings.TrimSpace(body), "# ") {
			t.Errorf("memory %q does not open with a heading saying what it covers", name)
		}
		// Memories record what is true, not what someone means to do. A plan
		// stored here outlives the intention and is read as fact.
		for _, tense := range []string{"we will ", "TODO", "we plan to ", "should be implemented"} {
			if strings.Contains(body, tense) {
				t.Errorf("memory %q contains %q; memories state what is true now, not what is intended", name, tense)
			}
		}
	}
}

func memoryFiles(entries []os.DirEntry) []string {
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		files = append(files, entry.Name())
	}
	return files
}

func containsIndex(entries []os.DirEntry) bool {
	for _, entry := range entries {
		if entry.Name() == memoryIndex {
			return true
		}
	}
	return false
}
