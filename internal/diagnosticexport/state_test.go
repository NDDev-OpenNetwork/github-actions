package diagnosticexport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateStoreIsAtomicPrivateAndStrict(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store := StateStore{Directory: directory}
	state := State{
		Status:  Status{SchemaVersion: 1, DeploymentStage: "canary", ObservedAt: "2026-08-08T16:00:00Z"},
		Exports: map[string]ExportRecord{"bundle": {SHA256: strings.Repeat("a", 64)}},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(directory, stateFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("state mode = %o", info.Mode().Perm())
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Exports["bundle"].SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("loaded state = %#v", loaded)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != stateFilename {
		t.Fatalf("temporary state leaked: %#v", entries)
	}
}

func TestStatusTimestampsRequireCanonicalUTC(t *testing.T) {
	status := Status{
		ObservedAt:    "2026-08-08T16:00:00Z",
		LastSuccessAt: "2026-08-08T16:00:00.123456789Z",
	}
	if observed, err := status.ObservedTime(); err != nil || !observed.Equal(time.Date(2026, 8, 8, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("ObservedTime() = %v, %v", observed, err)
	}
	if _, err := status.LastSuccessTime(); err != nil {
		t.Fatal(err)
	}
	status.ObservedAt = "2026-08-08T17:00:00+01:00"
	if _, err := status.ObservedTime(); err == nil {
		t.Fatal("non-UTC timestamp was accepted")
	}
}

func TestProgressIsSeparateAndLegacyStateRemainsReadable(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	store := StateStore{Directory: directory}
	observed := "2026-08-20T05:00:00Z"
	if err := store.Save(State{Status: Status{
		SchemaVersion: 1, DeploymentStage: StageCanary, ObservedAt: observed,
	}, Exports: map[string]ExportRecord{}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProgress(Progress{
		SchemaVersion: 1, ObservedAt: observed, LastProgressAt: observed, LastFullSyncAt: observed,
		ScannedBundles: 3, ExportedBundles: 3, DeletedBundles: 3,
	}); err != nil {
		t.Fatal(err)
	}
	legacyState, err := os.ReadFile(filepath.Join(directory, stateFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyState), "last_progress_at") || strings.Contains(string(legacyState), "last_full_sync_at") {
		t.Fatalf("progress fields broke the rollback-readable state: %s", legacyState)
	}
	status, err := ReadStatus(directory)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastProgressAt != observed || status.LastFullSyncAt != observed || status.DeletedBundles != 3 {
		t.Fatalf("merged status = %#v", status)
	}
	info, err := os.Stat(filepath.Join(directory, progressFilename))
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("progress file mode=%v err=%v", info, err)
	}
}

func TestStateStoreRejectsUnknownStateField(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"schema_version":1,"status":{},"exports":{},"unknown":true}`
	statePath := filepath.Join(directory, stateFilename)
	if err := os.WriteFile(statePath, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	// WriteFile only requests a mode; the process umask masks it. Under the
	// worker's umask 077 this fixture would land as 0600 and the store would
	// reject it for its mode before ever reaching the unknown-field check.
	if err := os.Chmod(statePath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := (StateStore{Directory: directory}).Load(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v", err)
	}
}
