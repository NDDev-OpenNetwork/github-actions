package schedulerrecovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFileStorePersistsAttemptAndSuppressesReplay(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store := FileStore{Path: filepath.Join(directory, "state.json"), LockPath: filepath.Join(directory, "state.lock")}
	attempt := NewAttempt(time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC), []string{"instance-1"})
	acquired, err := store.Begin(context.Background(), attempt)
	require.NoError(t, err)
	require.True(t, acquired)

	reopened := FileStore{Path: store.Path, LockPath: store.LockPath}
	acquired, err = reopened.Begin(context.Background(), attempt)
	require.NoError(t, err)
	require.False(t, acquired)

	require.NoError(t, reopened.Finish(context.Background(), Result{AttemptID: attempt.ID, Recovered: true}))
	acquired, err = store.Begin(context.Background(), attempt)
	require.NoError(t, err)
	require.False(t, acquired)
	state, err := readFileState(store.Path)
	require.NoError(t, err)
	require.Empty(t, state.Active)
	require.Len(t, state.Finished, 1)
	require.True(t, state.Finished[0].Recovered)
}

func TestFileStoreRejectsMalformedState(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	store := FileStore{Path: path, LockPath: filepath.Join(directory, "state.lock")}
	_, err := store.Begin(context.Background(), NewAttempt(time.Now(), []string{"instance-1"}))
	require.ErrorContains(t, err, "unsupported recovery state")
}
