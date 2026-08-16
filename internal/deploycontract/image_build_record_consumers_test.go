package deploycontract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestImageBuildRecordConsumersAgreeOnSchema binds every reader of
// /etc/nddev/image-build.json to the schema provision.sh actually writes.
//
// This exists because that coupling was broken once: raising the record to
// schema 2 to carry the baked toolchains updated the smoke scripts but not the
// self-hosted canary workflow, which kept asserting schema 1 and failed on the
// first job that ran against the promoted image. Nothing in the repository
// connected the writer to that reader, so a grep that missed one directory was
// enough to ship it. A test is the only durable link.
func TestImageBuildRecordConsumersAgreeOnSchema(t *testing.T) {
	provision := readRepoFile(t, "internal/imagebuild/assets/provision.sh")
	writerVersions := regexp.MustCompile(`\{schema_version:(\d+),[^}]*image-build|schema_version:(\d+), manifest_fingerprint`).
		FindStringSubmatch(provision)
	if writerVersions == nil {
		t.Fatal("provision.sh no longer writes a recognisable image-build record; update this test with it")
	}
	written := writerVersions[1]
	if written == "" {
		written = writerVersions[2]
	}
	if written == "" {
		t.Fatal("could not determine the schema version provision.sh writes")
	}

	// Every field the record is expected to carry must actually be written.
	for _, field := range []string{
		"manifest_fingerprint", "recipe_fingerprint", "runner_version", "runner_sha256",
		"source_release", "source_disk_sha256", "package_manifest_sha256",
		"sccache_version", "sccache_archive_sha256", "sccache_binary_sha256",
		"runner_tool_cache", "toolchains",
	} {
		if !strings.Contains(provision, field+":$") && !strings.Contains(provision, field+":") {
			t.Errorf("provision.sh does not write %q into the build record", field)
		}
	}

	// Any file that reads the record and pins a schema version must pin the
	// one that is written. Walking the tree keeps a new reader from escaping.
	assertion := regexp.MustCompile(`\.schema_version\s*==\s*(\d+)`)
	readers := 0
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "target", ".venv", "dist", "third_party":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".sh", ".yml", ".yaml":
		default:
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(raw)
		if !strings.Contains(content, "image-build.json") {
			return nil
		}
		readers++
		for _, match := range assertion.FindAllStringSubmatch(content, -1) {
			if match[1] != written {
				t.Errorf("%s asserts image-build schema %s, provision.sh writes %s",
					path, match[1], written)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// provision.sh, both smoke scripts, sanitize.sh and the two workflows.
	if readers < 5 {
		t.Fatalf("found only %d image-build record consumers; the walk is not covering the tree", readers)
	}
}

func readRepoFile(t *testing.T, relative string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", relative))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
