package diagnosticexport

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
)

const remoteRevalidationInterval = time.Hour

var ErrRemoteObjectExists = errors.New("remote object already exists")

type RemoteObject struct {
	Exists        bool
	Bytes         int64
	SHA256        string
	SchemaVersion string
}

type ObjectStore interface {
	Head(context.Context, string, string) (RemoteObject, error)
	Put(context.Context, string, Bundle) error
}

type Summary struct {
	SourceBundles   int   `json:"source_bundles"`
	ExportedBundles int   `json:"exported_bundles"`
	PendingBundles  int   `json:"pending_bundles"`
	SourceBytes     int64 `json:"source_bytes"`
	ExportedBytes   int64 `json:"exported_bytes"`
	ScannedBundles  int   `json:"scanned_bundles"`
	DeletedBundles  int   `json:"deleted_bundles"`
}

type Exporter struct {
	Config Config
	Store  ObjectStore
	State  StateStore
	Now    func() time.Time
}

type ExportError struct {
	Code   string
	Failed int
}

func (e ExportError) Error() string {
	return fmt.Sprintf("diagnostic export failed with reason %s for %d bundle(s)", e.Code, e.Failed)
}

func (e Exporter) Run(ctx context.Context) (Summary, error) {
	if err := e.Config.Validate(); err != nil {
		return Summary{}, err
	}
	if e.Store == nil {
		return Summary{}, errors.New("diagnostic object store is required")
	}
	if e.State.Directory != e.Config.StateDirectory {
		return Summary{}, errors.New("diagnostic exporter state store does not match config")
	}
	now := time.Now().UTC()
	if e.Now != nil {
		now = e.Now().UTC()
	}
	state, err := e.State.Load()
	if err != nil {
		return Summary{}, err
	}
	progress, err := e.State.LoadProgress()
	if err != nil {
		return Summary{}, err
	}
	names, _, _, err := ListBundleBatch(ctx, e.Config, maxBundlesPerRun)
	if err != nil {
		return Summary{}, e.saveFailure(state, now, Summary{}, "source-list", 1, err)
	}
	// Confirmation records are crash markers, not an archive index. Never let
	// historical exports make state grow with the lifetime of the fleet.
	if len(state.Exports) > maxBundlesPerRun {
		state.Exports = make(map[string]ExportRecord)
	}

	summary := Summary{ScannedBundles: len(names)}
	firstFailure := ""
	var firstFailureCause error
	failures := 0
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			if firstFailure == "" {
				firstFailure = "cancelled"
			}
			failures++
			break
		}
		bundle, err := ReadBundle(ctx, e.Config, name)
		if err != nil {
			if firstFailure == "" {
				firstFailure = "bundle-verify"
				firstFailureCause = fmt.Errorf("verify diagnostic bundle %q: %w", name, err)
			}
			failures++
			continue
		}
		bundleBytes := int64(len(bundle.Content))
		summary.SourceBytes += bundleBytes
		record, recorded := state.Exports[name]
		verifiedAt, _ := time.Parse(time.RFC3339Nano, record.LastVerifiedAt)
		if recorded && record.SHA256 == bundle.SHA256 && record.ObjectKey == bundle.ObjectKey &&
			record.Bytes == bundleBytes && !verifiedAt.IsZero() && !verifiedAt.After(now) &&
			now.Sub(verifiedAt) < remoteRevalidationInterval {
			summary.ExportedBundles++
			summary.ExportedBytes += bundleBytes
			if err := RemoveBundle(e.Config, bundle); err != nil {
				if firstFailure == "" {
					firstFailure = "source-remove"
					firstFailureCause = err
				}
				failures++
				continue
			}
			delete(state.Exports, name)
			summary.DeletedBundles++
			continue
		}
		remote, err := e.Store.Head(ctx, e.Config.Bucket, bundle.ObjectKey)
		if err != nil {
			if firstFailure == "" {
				firstFailure = "remote-head"
			}
			failures++
			continue
		}
		if remote.Exists {
			if !remoteMatches(remote, bundle) {
				if firstFailure == "" {
					firstFailure = "remote-collision"
				}
				failures++
				continue
			}
		} else {
			if err := e.Store.Put(ctx, e.Config.Bucket, bundle); err != nil && !errors.Is(err, ErrRemoteObjectExists) {
				if firstFailure == "" {
					firstFailure = "remote-put"
				}
				failures++
				continue
			}
			remote, err = e.Store.Head(ctx, e.Config.Bucket, bundle.ObjectKey)
			if err != nil || !remoteMatches(remote, bundle) {
				if firstFailure == "" {
					firstFailure = "remote-confirm"
				}
				failures++
				continue
			}
		}
		exportedAt := now.Format(time.RFC3339Nano)
		if recorded && record.ExportedAt != "" {
			exportedAt = record.ExportedAt
		}
		state.Exports[name] = ExportRecord{
			SHA256: bundle.SHA256, ObjectKey: bundle.ObjectKey, Bytes: bundleBytes,
			ExportedAt: exportedAt, LastVerifiedAt: now.Format(time.RFC3339Nano),
		}
		// Persist confirmation before unlink. If the process crashes after this
		// save, the next invocation verifies the record and finishes removal.
		state.Status.ObservedAt = now.Format(time.RFC3339Nano)
		if err := e.State.Save(state); err != nil {
			return summary, err
		}
		if err := RemoveBundle(e.Config, bundle); err != nil {
			if firstFailure == "" {
				firstFailure = "source-remove"
				firstFailureCause = err
			}
			failures++
			continue
		}
		delete(state.Exports, name)
		summary.DeletedBundles++
		summary.ExportedBundles++
		summary.ExportedBytes += bundleBytes
	}
	_, remainingBundles, remainingBytes, scanErr := ListBundleBatch(ctx, e.Config, 1)
	if scanErr != nil {
		return summary, e.saveFailure(state, now, summary, "source-rescan", 1, scanErr)
	}
	summary.SourceBundles = remainingBundles
	summary.PendingBundles = remainingBundles
	summary.SourceBytes = remainingBytes
	status := Status{
		SchemaVersion: stateSchemaVersion, DeploymentStage: e.Config.DeploymentStage,
		ObservedAt: now.Format(time.RFC3339Nano), LastSuccessAt: state.Status.LastSuccessAt,
		SourceBundles: summary.SourceBundles, ExportedBundles: summary.ExportedBundles,
		PendingBundles: summary.PendingBundles, SourceBytes: summary.SourceBytes,
		ExportedBytes: summary.ExportedBytes,
	}
	if failures == 0 && summary.PendingBundles == 0 {
		status.LastSuccessAt = now.Format(time.RFC3339Nano)
	} else if failures != 0 {
		status.LastErrorCode = firstFailure
		status.ConsecutiveFailures = state.Status.ConsecutiveFailures + 1
	}
	state.Status = status
	if err := e.State.Save(state); err != nil {
		return summary, err
	}
	progress.SchemaVersion = progressSchemaVersion
	progress.ObservedAt = now.Format(time.RFC3339Nano)
	progress.ScannedBundles = summary.ScannedBundles
	progress.ExportedBundles = summary.ExportedBundles
	progress.DeletedBundles = summary.DeletedBundles
	progress.FailedBundles = failures
	progress.BacklogBundles = summary.PendingBundles
	progress.BacklogBytes = summary.SourceBytes
	if summary.DeletedBundles > 0 {
		progress.LastProgressAt = now.Format(time.RFC3339Nano)
	}
	if failures == 0 && summary.PendingBundles == 0 {
		progress.LastFullSyncAt = now.Format(time.RFC3339Nano)
	}
	if err := e.State.SaveProgress(progress); err != nil {
		return summary, err
	}
	if status.LastErrorCode != "" {
		exportError := ExportError{Code: status.LastErrorCode, Failed: failures}
		if firstFailureCause != nil {
			return summary, errors.Join(exportError, firstFailureCause)
		}
		return summary, exportError
	}
	return summary, nil
}

func (e Exporter) saveFailure(
	state State,
	now time.Time,
	summary Summary,
	code string,
	failed int,
	cause error,
) error {
	state.Status = Status{
		SchemaVersion: stateSchemaVersion, DeploymentStage: e.Config.DeploymentStage,
		ObservedAt: now.Format(time.RFC3339Nano), LastSuccessAt: state.Status.LastSuccessAt,
		LastErrorCode: code, ConsecutiveFailures: state.Status.ConsecutiveFailures + 1,
		SourceBundles: summary.SourceBundles, ExportedBundles: summary.ExportedBundles,
		PendingBundles: summary.PendingBundles, SourceBytes: summary.SourceBytes,
		ExportedBytes: summary.ExportedBytes,
	}
	if err := e.State.Save(state); err != nil {
		return errors.Join(cause, err)
	}
	return errors.Join(cause, ExportError{Code: code, Failed: failed})
}

func remoteMatches(remote RemoteObject, bundle Bundle) bool {
	return remote.Exists && remote.Bytes == int64(len(bundle.Content)) && remote.SHA256 == bundle.SHA256 &&
		remote.SchemaVersion == fmt.Sprint(workerdiagnostics.SchemaVersion)
}
