package diagnosticexport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeObjectStore struct {
	objects   map[string]RemoteObject
	headCalls int
	putCalls  int
	putError  error
}

func (f *fakeObjectStore) Head(_ context.Context, bucket, key string) (RemoteObject, error) {
	f.headCalls++
	return f.objects[bucket+"/"+key], nil
}

func (f *fakeObjectStore) Put(_ context.Context, bucket string, bundle Bundle) error {
	f.putCalls++
	if f.putError != nil {
		return f.putError
	}
	f.objects[bucket+"/"+bundle.ObjectKey] = RemoteObject{
		Exists: true, Bytes: int64(len(bundle.Content)), SHA256: bundle.SHA256,
		SchemaVersion: "1",
	}
	return nil
}

func exporterFixture(t *testing.T) (Exporter, *fakeObjectStore, time.Time) {
	t.Helper()
	config, _ := diagnosticFixture(t)
	if err := os.Mkdir(config.StateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	remote := &fakeObjectStore{objects: make(map[string]RemoteObject)}
	now := time.Date(2026, 8, 8, 16, 0, 0, 0, time.UTC)
	exporter := Exporter{
		Config: config,
		Store:  remote,
		State:  StateStore{Directory: config.StateDirectory},
		Now:    func() time.Time { return now },
	}
	return exporter, remote, now
}

func TestExporterUploadsConfirmsAndUsesFreshJournal(t *testing.T) {
	exporter, remote, now := exporterFixture(t)
	summary, err := exporter.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.SourceBundles != 1 || summary.ExportedBundles != 1 || summary.PendingBundles != 0 ||
		remote.putCalls != 1 || remote.headCalls != 2 {
		t.Fatalf("first export = %#v, head=%d put=%d", summary, remote.headCalls, remote.putCalls)
	}
	status, err := ReadStatus(exporter.Config.StateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastSuccessAt != now.Format(time.RFC3339Nano) || status.LastErrorCode != "" ||
		status.ConsecutiveFailures != 0 {
		t.Fatalf("status = %#v", status)
	}

	exporter.Now = func() time.Time { return now.Add(10 * time.Minute) }
	if _, err := exporter.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if remote.putCalls != 1 || remote.headCalls != 2 {
		t.Fatalf("fresh journal contacted remote: head=%d put=%d", remote.headCalls, remote.putCalls)
	}

	exporter.Now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := exporter.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if remote.headCalls != 3 {
		t.Fatalf("remote revalidation head calls = %d", remote.headCalls)
	}
}

func TestExporterUploadsUnassignedWarmBundleInSeparateScope(t *testing.T) {
	config, _ := diagnosticFixtureForScope(
		t,
		"nddev-linux-standard",
		"nddev-linux-standard",
		"warm/nddev-linux-standard",
		"",
	)
	if err := os.Mkdir(config.StateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	remote := &fakeObjectStore{objects: make(map[string]RemoteObject)}
	exporter := Exporter{
		Config: config,
		Store:  remote,
		State:  StateStore{Directory: config.StateDirectory},
		Now:    func() time.Time { return time.Date(2026, 8, 8, 16, 0, 0, 0, time.UTC) },
	}
	summary, err := exporter.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.SourceBundles != 1 || summary.ExportedBundles != 1 || summary.PendingBundles != 0 || remote.putCalls != 1 {
		t.Fatalf("unassigned warm export = %#v, put=%d", summary, remote.putCalls)
	}
	for key := range remote.objects {
		if !strings.Contains(key, "/diagnostics/v1/unassigned-warm/") || strings.Contains(key, "/repository/") {
			t.Fatalf("unassigned warm remote key = %q", key)
		}
	}
}

func TestExporterFailsClosedOnRemoteCollision(t *testing.T) {
	exporter, remote, _ := exporterFixture(t)
	config := exporter.Config
	name := mustOnlyBundleName(t, config.SourceDirectory)
	bundle, err := ReadBundle(context.Background(), config, name)
	if err != nil {
		t.Fatal(err)
	}
	remote.objects[config.Bucket+"/"+bundle.ObjectKey] = RemoteObject{
		Exists: true, Bytes: int64(len(bundle.Content)), SHA256: "wrong", SchemaVersion: "1",
	}
	summary, err := exporter.Run(context.Background())
	var exportError ExportError
	if !errors.As(err, &exportError) || exportError.Code != "remote-collision" {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.PendingBundles != 1 || summary.ExportedBundles != 0 || remote.putCalls != 0 {
		t.Fatalf("collision summary = %#v", summary)
	}
	status, statusErr := ReadStatus(config.StateDirectory)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if status.LastErrorCode != "remote-collision" || status.ConsecutiveFailures != 1 {
		t.Fatalf("collision status = %#v", status)
	}
}

func TestExporterNeverDeletesSourceAfterRemoteFailure(t *testing.T) {
	exporter, remote, _ := exporterFixture(t)
	remote.putError = errors.New("synthetic unavailable")
	name := mustOnlyBundleName(t, exporter.Config.SourceDirectory)
	if _, err := exporter.Run(context.Background()); err == nil {
		t.Fatal("remote failure was accepted")
	}
	if _, err := os.Stat(filepath.Join(exporter.Config.SourceDirectory, name)); err != nil {
		t.Fatalf("source bundle was removed: %v", err)
	}
}

func TestExporterReportsFirstBundleVerificationCause(t *testing.T) {
	exporter, _, _ := exporterFixture(t)
	name := mustOnlyBundleName(t, exporter.Config.SourceDirectory)
	if err := os.Chmod(filepath.Join(exporter.Config.SourceDirectory, name), 0o640); err != nil {
		t.Fatal(err)
	}

	summary, err := exporter.Run(context.Background())
	var exportError ExportError
	if !errors.As(err, &exportError) || exportError.Code != "bundle-verify" || exportError.Failed != 1 {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(err.Error(), "diagnostic bundle ownership, type, link count or mode is unsafe") {
		t.Fatalf("Run() discarded bundle verification cause: %v", err)
	}
	if summary.PendingBundles != 1 || summary.ExportedBundles != 0 {
		t.Fatalf("verification failure summary = %#v", summary)
	}
	status, statusErr := ReadStatus(exporter.Config.StateDirectory)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if status.LastErrorCode != "bundle-verify" || status.ConsecutiveFailures != 1 {
		t.Fatalf("verification failure status = %#v", status)
	}
}

func mustOnlyBundleName(t *testing.T, directory string) string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return entry.Name()
		}
	}
	t.Fatal("fixture has no bundle")
	return ""
}
