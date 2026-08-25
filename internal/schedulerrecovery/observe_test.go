package schedulerrecovery

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCommandObserverDecodesStrictSnapshot(t *testing.T) {
	t.Parallel()
	command, err := os.Executable()
	require.NoError(t, err)
	observation, err := (CommandObserver{Argv: []string{command, "-test.run=TestSchedulerRecoveryObserverHelper", "--", "valid"}, Timeout: 10 * time.Second}).Observe(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, observation.ActiveIntents)
	require.Equal(t, 10*time.Minute, observation.ManagerUptime)
	require.Equal(t, 2*time.Minute, observation.PendingCreates[0].Age)
}

func TestCommandObserverFailsClosed(t *testing.T) {
	t.Parallel()
	command, executableErr := os.Executable()
	require.NoError(t, executableErr)
	_, err := (CommandObserver{Argv: []string{command, "-test.run=TestSchedulerRecoveryObserverHelper", "--", "unknown"}, Timeout: 10 * time.Second}).Observe(context.Background())
	require.ErrorContains(t, err, "unknown field")
	_, err = (CommandObserver{Argv: []string{command, "-test.run=TestSchedulerRecoveryObserverHelper", "--", "invalid"}, Timeout: 10 * time.Second}).Observe(context.Background())
	require.ErrorContains(t, err, "invalid values")
}

func TestSchedulerRecoveryObserverHelper(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "valid":
		fmt.Print(`{"observed_at":"2026-08-24T10:00:00Z","active_intents":2,"pending_creates":[{"id":"instance-1","age_nanoseconds":120000000000,"create_attempt":0}],"manager_uptime_seconds":600,"last_recovery_at":"0001-01-01T00:00:00Z","recovery_running":false}`)
	case "unknown":
		fmt.Print(`{"observed_at":"2026-08-24T10:00:00Z","active_intents":1,"manager_uptime_seconds":1,"unexpected":true}`)
	case "invalid":
		fmt.Print(`{"observed_at":"2026-08-24T10:00:00Z","active_intents":-1,"manager_uptime_seconds":1}`)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}
