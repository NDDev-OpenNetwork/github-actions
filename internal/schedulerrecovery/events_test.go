package schedulerrecovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMetricsFileSinkPublishesBoundedIdentityFreeState(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "scheduler.prom")
	event := Event{At: time.Unix(1234, 0).UTC(), State: "recovering", AttemptID: "secret-attempt", Stuck: []string{"private-instance"}}
	require.NoError(t, (MetricsFileSink{Path: path}).Emit(context.Background(), event))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)
	require.Contains(t, text, `gha_scheduler_recovery_state{state="recovering"} 1`)
	require.Contains(t, text, "gha_scheduler_recovery_stuck_instances 1")
	require.Contains(t, text, "gha_scheduler_recovery_last_event_timestamp_seconds 1234")
	require.NotContains(t, text, event.AttemptID)
	require.NotContains(t, text, event.Stuck[0])
}

func TestMetricsFileSinkRejectsUnknownState(t *testing.T) {
	t.Parallel()
	err := (MetricsFileSink{Path: filepath.Join(t.TempDir(), "scheduler.prom")}).Emit(context.Background(), Event{State: "unknown"})
	require.ErrorContains(t, err, "unknown scheduler recovery lifecycle state")
}
