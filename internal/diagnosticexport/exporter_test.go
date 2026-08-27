package diagnosticexport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
)

type fakeObjectStore struct {
	objects    map[string]RemoteObject
	headCalls  int
	putCalls   int
	putError   error
	putHook    func(Bundle)
	headErrors []error
}

func (f *fakeObjectStore) Head(_ context.Context, bucket, key string) (RemoteObject, error) {
	f.headCalls++
	if len(f.headErrors) > 0 {
		err := f.headErrors[0]
		f.headErrors = f.headErrors[1:]
		return RemoteObject{}, err
	}
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
	if f.putHook != nil {
		f.putHook(bundle)
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

func TestExporterRetriesRemoteHeadAcrossRouteConvergence(t *testing.T) {
	exporter, remote, _ := exporterFixture(t)
	remote.headErrors = []error{errors.New("route absent"), errors.New("route absent"), errors.New("route absent")}
	sleeps := 0
	exporter.Sleep = func(context.Context, time.Duration) error {
		sleeps++
		return nil
	}
	summary, err := exporter.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sleeps != 3 || remote.headCalls != 5 || summary.PendingBundles != 0 || summary.ExportedBundles != 1 {
		t.Fatalf("summary=%#v sleeps=%d heads=%d", summary, sleeps, remote.headCalls)
	}
	status, err := ReadStatus(exporter.Config.StateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if status.ConsecutiveFailures != 0 || status.LastErrorCode != "" {
		t.Fatalf("status=%#v", status)
	}
}

func TestExporterBoundsRemoteOutageOncePerBatch(t *testing.T) {
	exporter, remote, now := exporterFixture(t)
	store := workerdiagnostics.Store{
		Directory: exporter.Config.SourceDirectory, Retention: 7 * 24 * time.Hour,
		MaxBundleBytes: exporter.Config.MaxBundleBytes, MaxTotalBytes: 1024 * 1024 * 1024,
		MaxArtifacts: workerdiagnostics.DefaultMaxArtifacts,
		Now:          func() time.Time { return now.Add(time.Second) },
		Random:       strings.NewReader(strings.Repeat("abcdef", 600)),
	}
	for index := 0; index < 2; index++ {
		_, err := store.Write(context.Background(), workerdiagnostics.Instance{
			Name: fmt.Sprintf("runner-outage-%d", index), ControllerID: "controller-test",
			PoolID: "pool-test", PoolName: "nddev-linux-standard", ScaleSet: "nddev-linux-standard",
			Repository: validConfig().Repositories[1], ImageFingerprint: strings.Repeat("a", 64),
			RunnerVersion: "v2.336.0", ProviderVersion: "v0.1.5-nddev.3",
			ProviderCommit: strings.Repeat("b", 40), State: "Stopped",
		}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	remote.headErrors = []error{
		errors.New("route absent"), errors.New("route absent"),
		errors.New("route absent"), errors.New("route absent"),
	}
	exporter.Sleep = func(context.Context, time.Duration) error { return nil }
	summary, err := exporter.Run(context.Background())
	var exportError ExportError
	if !errors.As(err, &exportError) || exportError.Code != "remote-head" || exportError.Failed != 1 {
		t.Fatalf("error=%v", err)
	}
	if summary.ScannedBundles != 3 || summary.PendingBundles != 3 || remote.headCalls != remoteHeadAttempts {
		t.Fatalf("summary=%#v heads=%d", summary, remote.headCalls)
	}
}

func TestExporterUploadsConfirmsAndUsesFreshJournal(t *testing.T) {
	exporter, remote, now := exporterFixture(t)
	summary, err := exporter.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.SourceBundles != 0 || summary.ExportedBundles != 1 || summary.PendingBundles != 0 ||
		remote.putCalls != 1 || remote.headCalls != 2 {
		t.Fatalf("first export = %#v, head=%d put=%d", summary, remote.headCalls, remote.putCalls)
	}
	status, err := ReadStatus(exporter.Config.StateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if status.LastSuccessAt != now.Format(time.RFC3339Nano) || status.LastErrorCode != "" ||
		status.ConsecutiveFailures != 0 || status.LastProgressAt != now.Format(time.RFC3339Nano) ||
		status.LastFullSyncAt != now.Format(time.RFC3339Nano) || status.ScannedBundles != 1 || status.DeletedBundles != 1 {
		t.Fatalf("status = %#v", status)
	}
	if names, err := ListBundles(exporter.Config); err != nil || len(names) != 0 {
		t.Fatalf("confirmed source was not drained: names=%v err=%v", names, err)
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
	if remote.headCalls != 2 {
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
	if summary.SourceBundles != 0 || summary.ExportedBundles != 1 || summary.PendingBundles != 0 || remote.putCalls != 1 {
		t.Fatalf("unassigned warm export = %#v, put=%d", summary, remote.putCalls)
	}
	for key := range remote.objects {
		if !strings.Contains(key, "/diagnostics/v1/unassigned-warm/") || strings.Contains(key, "/repository/") {
			t.Fatalf("unassigned warm remote key = %q", key)
		}
	}
}

func TestExporterCatchesAContinuousBacklogInBoundedBatches(t *testing.T) {
	exporter, remote, now := exporterFixture(t)
	config := exporter.Config
	capturedAt := now.Add(-time.Hour)
	store := workerdiagnostics.Store{
		Directory: config.SourceDirectory, Retention: 7 * 24 * time.Hour,
		MaxBundleBytes: config.MaxBundleBytes, MaxTotalBytes: 1024 * 1024 * 1024,
		MaxArtifacts: workerdiagnostics.DefaultMaxArtifacts,
		Now:          func() time.Time { return capturedAt },
		Random:       strings.NewReader(strings.Repeat("abcdef", 600)),
	}
	write := func(index int) {
		capturedAt = capturedAt.Add(time.Nanosecond)
		_, err := store.Write(context.Background(), workerdiagnostics.Instance{
			Name: fmt.Sprintf("runner-backlog-%03d", index), ControllerID: "controller-test",
			PoolID: "pool-test", PoolName: "nddev-linux-standard", ScaleSet: "nddev-linux-standard",
			Repository: validConfig().Repositories[1], ImageFingerprint: strings.Repeat("a", 64),
			RunnerVersion: "v2.336.0", ProviderVersion: "v0.1.5-nddev.3",
			ProviderCommit: strings.Repeat("b", 40), State: "Stopped",
		}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 512; index++ {
		write(index)
	}
	first, err := exporter.Run(context.Background())
	if err != nil || first.ScannedBundles != maxBundlesPerRun || first.DeletedBundles != maxBundlesPerRun || first.PendingBundles != 257 {
		t.Fatalf("first bounded drain = %#v err=%v", first, err)
	}
	write(512) // producer continues while the bounded drain catches up
	second, err := exporter.Run(context.Background())
	if err != nil || second.ScannedBundles != maxBundlesPerRun || second.PendingBundles != 2 {
		t.Fatalf("second bounded drain = %#v err=%v", second, err)
	}
	third, err := exporter.Run(context.Background())
	if err != nil || third.ScannedBundles != 2 || third.PendingBundles != 0 || len(remote.objects) != 514 {
		t.Fatalf("final bounded drain = %#v objects=%d err=%v", third, len(remote.objects), err)
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

func TestExporterRefusesToDeleteAReplacedConfirmedSource(t *testing.T) {
	exporter, remote, _ := exporterFixture(t)
	remote.putHook = func(bundle Bundle) {
		path := filepath.Join(exporter.Config.SourceDirectory, bundle.Name)
		replacement := path + ".replacement"
		if err := os.WriteFile(replacement, bundle.Content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
	}
	_, err := exporter.Run(context.Background())
	var exportError ExportError
	if !errors.As(err, &exportError) || exportError.Code != "source-remove" {
		t.Fatalf("replaced source error = %v", err)
	}
	status, statusErr := ReadStatus(exporter.Config.StateDirectory)
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if status.DeletedBundles != 0 || status.ScannedBundles != 1 {
		t.Fatalf("replaced source progress = %#v", status)
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
