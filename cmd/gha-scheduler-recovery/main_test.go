package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHeartbeatPersistsThroughCLI(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	state := filepath.Join(directory, "state.json")
	configuration := filepath.Join(directory, "config.json")
	require.NoError(t, os.WriteFile(configuration, []byte(fmt.Sprintf(`{
  "state_file": %q,
  "lock_file": %q,
  "policy": {"minimum_stuck_age_seconds":90,"minimum_uptime_seconds":120,"cooldown_seconds":600,"heartbeat_stale_seconds":60},
  "observation": {"argv":["/bin/true"],"timeout_seconds":1},
  "recovery": {"checkpoint":["/bin/true"],"restart":["/bin/true"],"progress":["/bin/true"],"timeout_seconds":1}
}
`, state, filepath.Join(directory, "state.lock"))), 0o600))
	require.Equal(t, 0, run([]string{"--config", configuration, "--progress", "job-1", "heartbeat"}))
	data, err := os.ReadFile(state)
	require.NoError(t, err)
	require.Contains(t, string(data), `"progress": "job-1"`)
}

func TestConfigFailsClosedOnUnknownOrRelativeState(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"unknown":true}`), 0o600))
	_, err := loadConfig(path)
	require.ErrorContains(t, err, "unknown field")
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(`{
  "state_file":"state.json","lock_file":"lock","policy":{"minimum_stuck_age_seconds":1,"minimum_uptime_seconds":1,"cooldown_seconds":1,"heartbeat_stale_seconds":1},
  "observation":{"argv":["/bin/true"],"timeout_seconds":1},"recovery":{"checkpoint":["/bin/true"],"restart":["/bin/true"],"progress":["/bin/true"],"timeout_seconds":1}}
`, "\n", "")), 0o600))
	_, err = loadConfig(path)
	require.ErrorContains(t, err, "incomplete")
}
