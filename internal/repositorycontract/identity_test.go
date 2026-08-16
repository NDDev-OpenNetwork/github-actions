// Package repositorycontract holds the checks that keep this repository
// self-consistent: the locator its commands default to, the credential anchor
// they resolve, and the generated projections that describe both.
//
// It is deliberately separate from deploycontract, which records what a
// deployment observed. These assertions are about the source tree itself, so
// they must hold on a clean checkout with no fleet in reach.
package repositorycontract

import (
	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"

	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	repositoryRoot = "../.."

	// currentLocator is the repository under the organization that owns the
	// fleet credential. It also owns the App and receives its installation.
	currentOwner    = "NDDev-OpenNetwork"
	currentLocator  = "NDDev-OpenNetwork/github-actions"
	previousLocator = "example-user/github-actions"
)

// historicalLocatorPaths may keep the pre-transfer locator. An audit record
// binds itself to the observation that produced it, and a run that happened
// under the old locator did happen under the old locator; rewriting these
// would describe a run that never took place. Zot namespaces are object-store
// paths bound by digest into two audit records, one of which can only be
// re-gathered by rebooting a fleet host.
var historicalLocatorPaths = []string{
	"benchmark/evidence/",
	"config/",
	"deploy/fleet-host/zot.json",
	"docs/adr/",
	"docs/benchmark-phase0.md",
	"docs/cache-plane.md",
	"docs/host-baseline.md",
	"internal/",
	"testdata/",
	"third_party/",
	".gds/repository.yaml",
	"SECURITY.md",
	".github/CODEOWNERS",
}

// Operator-facing commands and defaults must name the repository as it exists
// now. A runbook that sends an operator to a locator GitHub no longer serves
// is worse than no runbook, because it fails only at the point of use.
func TestCurrentFacingSurfacesUseTheCurrentLocator(t *testing.T) {
	t.Parallel()

	var offenders []string
	for _, relative := range trackedFiles(t) {
		if exemptFromLocatorCheck(relative) || !scannableExtension(relative) {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(repositoryRoot, relative))
		if errors.Is(readErr, os.ErrNotExist) {
			// Tracked but absent from the working tree. git still names it,
			// and a file that is not there cannot contain anything.
			continue
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		for number, line := range strings.Split(string(content), "\n") {
			if retainedObjectStorePath(line) {
				continue
			}
			if strings.Contains(line, previousLocator) {
				offenders = append(offenders, relative+":"+itoa(number+1))
			}
		}
	}
	if len(offenders) != 0 {
		t.Fatalf("current-facing surfaces still name %s:\n  %s\n"+
			"Move the reference to the current locator, or add its path to "+
			"historicalLocatorPaths with the reason it must not move.",
			previousLocator, strings.Join(offenders, "\n  "))
	}
}

// The credential anchor, the CLI defaults that resolve it and the bootstrap
// runbook are three descriptions of one App. They drifted apart once already,
// and a clean checkout could not recreate the credential as a result.
func TestCredentialSurfacesDescribeOneApp(t *testing.T) {
	t.Parallel()

	anchorRaw, err := os.ReadFile(filepath.Join(repositoryRoot, "config/garm-credential-anchor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var anchor struct {
		CredentialName string `json:"credential_name"`
		AppID          int64  `json:"app_id"`
		InstallationID int64  `json:"installation_id"`
	}
	if err := json.Unmarshal(anchorRaw, &anchor); err != nil {
		t.Fatal(err)
	}
	if anchor.CredentialName == "" || anchor.AppID <= 0 || anchor.InstallationID <= 0 {
		t.Fatalf("incomplete credential anchor: %#v", anchor)
	}

	// The CLI no longer restates the App: it names a tenant and the registry
	// answers. That removes two of the three copies this test was written to
	// keep in step, so the comparison moves to the remaining source rather
	// than to the literals that used to stand in for it.
	deployed, err := tenant.ByID(tenant.DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	if deployed.Repository != currentLocator {
		t.Errorf("default tenant serves %q, anchor names %q", deployed.Repository, currentLocator)
	}
	if deployed.CredentialName != anchor.CredentialName {
		t.Errorf("default tenant credential is %q, anchor records %q", deployed.CredentialName, anchor.CredentialName)
	}
	if deployed.AppSlug != anchor.CredentialName {
		t.Errorf("default tenant App slug is %q, anchor records credential %q", deployed.AppSlug, anchor.CredentialName)
	}
	if deployed.HomepageURL != "https://github.com/"+currentLocator {
		t.Errorf("default tenant homepage is %q", deployed.HomepageURL)
	}

	main := readFile(t, "cmd/gha-fleet/main.go")
	for _, wanted := range []string{
		`"tenant", tenant.DefaultID`,
		`"owner-type", githubappbootstrap.OwnerTypeOrganization`,
	} {
		if !strings.Contains(main, wanted) {
			t.Errorf("cmd/gha-fleet/main.go does not default to %s", wanted)
		}
	}

	runbook := readFile(t, "docs/github-app-bootstrap.md")
	for _, wanted := range []string{
		"--repository " + currentLocator,
		"--owner-type organization",
		"`" + currentOwner + "`",
	} {
		if !strings.Contains(runbook, wanted) {
			t.Errorf("docs/github-app-bootstrap.md does not document %q", wanted)
		}
	}
}

// A generated projection is only trustworthy while it matches the digest its
// lock recorded. Editing one by hand detaches it from the canonical input it
// claims to describe, and nothing else in the repository would notice.
func TestGeneratedProjectionsMatchTheirLock(t *testing.T) {
	t.Parallel()

	lock := readFile(t, ".gds/bundle.lock.yaml")
	var path string
	checked := 0
	for _, line := range strings.Split(lock, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "- path:"):
			path = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- path:")), `"`)
		case strings.HasPrefix(trimmed, "digest:") && path != "":
			wanted := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "digest:")), `"`)
			content, err := os.ReadFile(filepath.Join(repositoryRoot, path))
			if err != nil {
				t.Fatalf("locked projection %s is missing: %v", path, err)
			}
			sum := sha256.Sum256(content)
			if observed := "sha256:" + hex.EncodeToString(sum[:]); observed != wanted {
				t.Errorf("%s digest is %s, lock records %s; regenerate instead of editing it", path, observed, wanted)
			}
			checked++
			path = ""
		}
	}
	if checked == 0 {
		t.Fatal("bundle lock records no projection digests to verify")
	}
}

// retainedObjectStorePath reports whether a line names the Zot cache
// namespaces or identities rather than the GitHub repository. They kept the
// former owner on purpose: nothing resolves them against the forge, and two
// audit records bind themselves to the SHA-256 of the Zot configuration, one
// of which can only be re-gathered by rebooting a fleet host. docs/cache-plane.md
// records the decision and what renaming them would cost.
func retainedObjectStorePath(line string) bool {
	return strings.Contains(line, "cache/"+previousLocator) ||
		strings.Contains(line, "gha-zot-example-user-")
}

func readFile(t *testing.T, relative string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repositoryRoot, relative))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// trackedFiles asks git what this repository contains, rather than asking the
// filesystem what happens to be lying in it. Walking the tree made the answer
// depend on untracked build output -- a benchmark run leaves .next/ and .venv/
// behind -- so the same commit passed in CI and failed for whoever had built
// locally. The subject of these assertions is the repository, which is exactly
// what the index enumerates.
func trackedFiles(t *testing.T) []string {
	t.Helper()

	command := exec.Command("git", "-C", repositoryRoot, "ls-files", "-z")
	out, err := command.Output()
	if err != nil {
		// `make test-umask` copies the working tree into a temporary
		// directory and runs there, so git has no repository to answer for.
		// That copy is `git ls-files --cached --others --exclude-standard`,
		// which is already the set this function wants: walking it cannot
		// reach ignored build output because none was copied. Anywhere else,
		// git is authoritative and its failure is a real one.
		if !insideGitRepository() {
			return walkedFiles(t)
		}
		t.Fatalf("enumerate tracked files: %v", err)
	}
	var files []string
	for _, name := range strings.Split(string(out), "\x00") {
		if name != "" {
			files = append(files, name)
		}
	}
	if len(files) == 0 {
		t.Fatal("git reported no tracked files; these assertions would pass vacuously")
	}
	return files
}

func exemptFromLocatorCheck(relative string) bool {
	for _, exempt := range historicalLocatorPaths {
		if strings.HasPrefix(relative, exempt) {
			return true
		}
	}
	return false
}

func scannableExtension(relative string) bool {
	switch filepath.Ext(relative) {
	case ".go", ".md", ".yml", ".yaml", ".sh", ".json", ".toml":
		return true
	default:
		return false
	}
}

func insideGitRepository() bool {
	return exec.Command("git", "-C", repositoryRoot, "rev-parse", "--git-dir").Run() == nil
}

// walkedFiles enumerates a tree that git has already filtered. It is sound
// only in that case, which is why it is not the primary path.
func walkedFiles(t *testing.T) []string {
	t.Helper()

	var files []string
	err := filepath.WalkDir(repositoryRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, relErr := filepath.Rel(repositoryRoot, path)
		if relErr != nil {
			return relErr
		}
		if entry.IsDir() {
			if relative == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, relative)
		return nil
	})
	if err != nil {
		t.Fatalf("enumerate copied files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("the copied tree is empty; these assertions would pass vacuously")
	}
	return files
}
