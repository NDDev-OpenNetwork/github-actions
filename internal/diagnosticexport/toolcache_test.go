package diagnosticexport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
)

func TestExtractToolCacheEventsFromRunnerArtifacts(t *testing.T) {
	t.Parallel()
	archive := toolCacheArchive(t, map[string]string{
		"runner/Worker.log": "prefix nddev_tool_cache_event={\"source\":\"upstream\",\"cache_result\":\"miss\",\"sha256\":\"" + strings.Repeat("a", 64) + "\",\"bytes\":42,\"duration_ms\":7}\nnddev_tool_cache_event={bad}\n",
		"incus/qemu.log":    "nddev_tool_cache_event={\"source\":\"ignored\"}\n",
	})
	events, rejected := ExtractToolCacheEvents(archive)
	if len(events) != 1 || rejected != 1 {
		t.Fatalf("events=%#v rejected=%d", events, rejected)
	}
	if events[0].Source != "upstream" || events[0].Bytes != 42 || events[0].DurationMS != 7 {
		t.Fatalf("event=%#v", events[0])
	}
}

func TestSummaryBindsToolCacheEventToBundleIdentity(t *testing.T) {
	t.Parallel()
	var summary Summary
	captured := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	summary.addToolCacheEvents(Bundle{
		Manifest:   workerdiagnostics.Manifest{Instance: workerdiagnostics.Instance{Name: "runner-1", Repository: "example/repository"}},
		CapturedAt: captured, RejectedToolCacheEvents: 2,
		ToolCacheEvents: []ToolCacheEvent{{Source: "cache", CacheResult: "hit", SHA256: strings.Repeat("a", 64)}},
	})
	if summary.ToolCacheEventCount != 1 || summary.RejectedToolCacheEventCount != 2 || len(summary.ToolCacheEvents) != 1 {
		t.Fatalf("summary=%#v", summary)
	}
	observed := summary.ToolCacheEvents[0]
	if observed.Repository != "example/repository" || observed.Runner != "runner-1" || !observed.CapturedAt.Equal(captured) {
		t.Fatalf("observed=%#v", observed)
	}
}

func toolCacheArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	for name, content := range files {
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
