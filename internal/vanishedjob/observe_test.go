package vanishedjob

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCommandObserverReadsBatch(t *testing.T) {
	directory := t.TempDir()
	command := filepath.Join(directory, "observe")
	require.NoError(t, os.WriteFile(command, []byte("#!/bin/sh\nprintf '%s\\n' '{\"jobs\":[{\"repository\":\"org/repo\",\"run_id\":42,\"job_id\":7}]}'\n"), 0o700))
	observation, err := (CommandObserver{Argv: []string{command}, Timeout: time.Second}).Observe(context.Background())
	require.NoError(t, err)
	require.Len(t, observation.Jobs, 1)
	require.Equal(t, int64(42), observation.Jobs[0].RunID)
}

func TestCommandObserverRejectsDuplicates(t *testing.T) {
	directory := t.TempDir()
	command := filepath.Join(directory, "observe")
	payload := "{\"jobs\":[{\"repository\":\"org/repo\",\"run_id\":42,\"job_id\":7},{\"repository\":\"org/repo\",\"run_id\":42,\"job_id\":7}]}"
	require.NoError(t, os.WriteFile(command, []byte("#!/bin/sh\nprintf '%s\\n' '"+payload+"'\n"), 0o700))
	_, err := (CommandObserver{Argv: []string{command}, Timeout: time.Second}).Observe(context.Background())
	require.ErrorContains(t, err, "duplicate job")
}
