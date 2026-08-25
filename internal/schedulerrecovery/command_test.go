package schedulerrecovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCommandExecutorRunsExactArgvWithoutShell(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	checkpoint := writeExecutable(t, directory, "checkpoint", "#!/bin/sh\nprintf 'checkpoint-1\\n'\n")
	restart := writeExecutable(t, directory, "restart", "#!/bin/sh\ntest \"$GHA_SCHEDULER_RECOVERY_ATTEMPT\" = attempt-1\ntest \"$GHA_SCHEDULER_RECOVERY_STUCK\" = instance-1,instance-2\n")
	progress := writeExecutable(t, directory, "progress", "#!/bin/sh\nprintf '{\"progressed\":[\"instance-1\",\"instance-2\"],\"remaining\":[]}\\n'\n")
	executor := CommandExecutor{Config: CommandConfig{
		Checkpoint: []string{checkpoint}, Restart: []string{restart}, Progress: []string{progress}, Timeout: time.Second,
	}}
	attempt := Attempt{ID: "attempt-1", Stuck: []string{"instance-1", "instance-2"}}
	identity, err := executor.Checkpoint(context.Background(), attempt)
	require.NoError(t, err)
	require.Equal(t, "checkpoint-1", identity)
	require.NoError(t, executor.RestartDispatcher(context.Background(), attempt))
	progressed, remaining, err := executor.AwaitProgress(context.Background(), attempt)
	require.NoError(t, err)
	require.Equal(t, attempt.Stuck, progressed)
	require.Empty(t, remaining)
}

func TestCommandExecutorRejectsShellAndTimeouts(t *testing.T) {
	t.Parallel()
	executor := CommandExecutor{Config: CommandConfig{
		Checkpoint: []string{"relative"}, Restart: []string{"/bin/true"}, Progress: []string{"/bin/true"}, Timeout: time.Second,
	}}
	require.ErrorContains(t, executor.Validate(), "absolute executable")
	directory := t.TempDir()
	slow := writeExecutable(t, directory, "slow", "#!/bin/sh\nsleep 2\n")
	executor.Config.Checkpoint = []string{slow}
	executor.Config.Timeout = 10 * time.Millisecond
	_, err := executor.Checkpoint(context.Background(), Attempt{ID: "attempt-1"})
	require.ErrorContains(t, err, "timed out")
}

func writeExecutable(t *testing.T, directory, name, content string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	require.NoError(t, err)
	_, err = file.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, file.Sync())
	require.NoError(t, file.Close())
	return path
}
