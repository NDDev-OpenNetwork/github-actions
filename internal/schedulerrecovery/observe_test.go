package schedulerrecovery

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCommandObserverDecodesStrictSnapshot(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	command := writeExecutable(t, directory, "observe", `#!/bin/sh
printf '%s\n' '{"observed_at":"2026-08-24T10:00:00Z","active_intents":2,"pending_creates":[{"id":"instance-1","age_nanoseconds":120000000000,"create_attempt":0}],"manager_uptime_seconds":600,"last_recovery_at":"0001-01-01T00:00:00Z","recovery_running":false}'
`)
	observation, err := (CommandObserver{Argv: []string{command}, Timeout: time.Second}).Observe(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, observation.ActiveIntents)
	require.Equal(t, 10*time.Minute, observation.ManagerUptime)
	require.Equal(t, 2*time.Minute, observation.PendingCreates[0].Age)
}

func TestCommandObserverFailsClosed(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	unknown := writeExecutable(t, directory, "unknown", "#!/bin/sh\nprintf '%s\\n' '{\"observed_at\":\"2026-08-24T10:00:00Z\",\"active_intents\":1,\"manager_uptime_seconds\":1,\"unexpected\":true}'\n")
	_, err := (CommandObserver{Argv: []string{unknown}, Timeout: time.Second}).Observe(context.Background())
	require.ErrorContains(t, err, "unknown field")
	invalid := writeExecutable(t, directory, "invalid", "#!/bin/sh\nprintf '%s\\n' '{\"observed_at\":\"2026-08-24T10:00:00Z\",\"active_intents\":-1,\"manager_uptime_seconds\":1}'\n")
	_, err = (CommandObserver{Argv: []string{invalid}, Timeout: time.Second}).Observe(context.Background())
	require.ErrorContains(t, err, "invalid values")
}
