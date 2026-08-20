package telemetrymanifest

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryManifestIsValidAndPinned(t *testing.T) {
	t.Parallel()

	manifest, err := Load(filepath.Join("..", "..", "config", "telemetry-artifacts.yaml"))
	if err != nil {
		t.Fatalf("load telemetry manifest: %v", err)
	}
	if manifest.Collector.Version != "v0.158.0" ||
		manifest.Collector.Archive.SHA256 != "7623348c295ec7b00d86c30040a30730f7e3537e813b34c880c1d5abb9bbe8d5" {
		t.Fatalf("collector is not exactly pinned: %#v", manifest.Collector)
	}
	if manifest.Store.Version != "v0.92.0" ||
		manifest.Store.Archive.SHA256 != "b8f95d3cea4ebca650b24df52a8232bf1da3d5293005c7942148a8d58f3c2879" {
		t.Fatalf("store is not exactly pinned: %#v", manifest.Store)
	}
	// Every published asset is pinned, not only the archive.
	for name, digest := range map[string]string{
		"checksum": manifest.Collector.Checksum.SHA256,
		"sbom":     manifest.Collector.SBOM.SHA256,
		"sigstore": manifest.Collector.SigstoreBundle.SHA256,
	} {
		if len(digest) != 64 {
			t.Errorf("collector %s asset is not pinned: %q", name, digest)
		}
	}
	fingerprint, err := manifest.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fingerprint, "sha256:") || len(fingerprint) != len("sha256:")+64 {
		t.Fatalf("unexpected fingerprint %q", fingerprint)
	}
}

func TestValidationRejectsMutableUnsafeOrSharedInputs(t *testing.T) {
	t.Parallel()

	base, err := Load(filepath.Join("..", "..", "config", "telemetry-artifacts.yaml"))
	if err != nil {
		t.Fatalf("load telemetry manifest: %v", err)
	}
	for _, testCase := range []struct {
		name    string
		mutate  func(*Manifest)
		message string
	}{
		{"mutable collector alias", func(m *Manifest) {
			m.Collector.Archive.URL = "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/latest/otelcol-contrib_0.158.0_linux_amd64.tar.gz"
		}, "collector.archive.url"},
		{"foreign collector host", func(m *Manifest) {
			m.Collector.Archive.URL = "https://example.com/otelcol-contrib_0.158.0_linux_amd64.tar.gz"
		}, "collector.archive.url"},
		{"collector version drift", func(m *Manifest) { m.Collector.Version = "v0.157.0" }, "collector.archive.name"},
		{"unpinned sbom", func(m *Manifest) { m.Collector.SBOM.SHA256 = "" }, "collector.sbom.sha256"},
		{"foreign store host", func(m *Manifest) {
			m.Store.Archive.URL = "https://example.com/openobserve-v0.92.0-linux-amd64.tar.gz"
		}, "store.archive.url"},
		{"store version drift", func(m *Manifest) { m.Store.Version = "v0.91.5" }, "store.archive.name"},
		{"unpinned store archive", func(m *Manifest) { m.Store.Archive.SHA256 = "" }, "store.archive.sha256"},
		{"shared telemetry bucket", func(m *Manifest) { m.Store.Bucket = "myattention-telemetry" }, "store.bucket"},
		{"undeclared stream", func(m *Manifest) { m.Store.Streams = append(m.Store.Streams, "fleet_extra") }, "store.streams"},
		{"unbounded retention", func(m *Manifest) { m.Store.RetentionDays = 4000 }, "store.retention_days"},
		{"public transport target", func(m *Manifest) { m.Transport.TargetAddress = "8.8.8.8" }, "transport.target_address"},
		{"same host transport", func(m *Manifest) { m.Transport.TargetAddress = m.Transport.SourceAddress }, "transport"},
		{"privileged port", func(m *Manifest) { m.Transport.TargetPort = 80 }, "transport.target_port"},
		{"relative queue", func(m *Manifest) { m.Transport.QueueDirectory = "telemetry-queue" }, "transport.queue_directory"},
	} {
		mutated := base
		mutated.Store.Streams = append([]string(nil), base.Store.Streams...)
		testCase.mutate(&mutated)
		err := mutated.Validate()
		if err == nil || !strings.Contains(err.Error(), testCase.message) {
			t.Errorf("%s: expected %q, got %v", testCase.name, testCase.message, err)
		}
	}
}

func TestDecodeRejectsUnknownAndMultipleDocuments(t *testing.T) {
	t.Parallel()

	if _, err := Decode(strings.NewReader("schema_version: 1\nunknown: true\n")); err == nil ||
		!strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
	if _, err := Decode(strings.NewReader("schema_version: 1\n---\nschema_version: 1\n")); err == nil ||
		!strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("expected multiple-document error, got %v", err)
	}
}

// Per-stream retention is a policy the store actually applies, so a value the
// store cannot honour -- a stream it does not carry, or a window longer than
// the store default -- is a manifest defect rather than a runtime surprise.
func TestStreamRetentionMustNameADeclaredStreamWithinTheDefault(t *testing.T) {
	for name, mutate := range map[string]func(*Manifest){
		"unknown stream":      func(m *Manifest) { m.Store.StreamRetentionDays = map[string]int{"not_a_stream": 14} },
		"longer than default": func(m *Manifest) { m.Store.StreamRetentionDays = map[string]int{"fleet_logs": 400} },
		"shorter than a week": func(m *Manifest) { m.Store.StreamRetentionDays = map[string]int{"fleet_logs": 6} },
	} {
		t.Run(name, func(t *testing.T) {
			manifest := mustLoadManifest(t)
			mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("invalid stream retention was accepted")
			}
		})
	}

	manifest := mustLoadManifest(t)
	manifest.Store.StreamRetentionDays = map[string]int{"fleet_logs": 14}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("a declared stream inside the default window was refused: %v", err)
	}
}

func mustLoadManifest(t *testing.T) Manifest {
	t.Helper()
	manifest, err := Load(filepath.Join("..", "..", "config", "telemetry-artifacts.yaml"))
	if err != nil {
		t.Fatalf("load telemetry manifest: %v", err)
	}
	return manifest
}
