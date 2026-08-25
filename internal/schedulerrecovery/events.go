package schedulerrecovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var lifecycleStates = []string{"healthy", "unhealthy", "recovering", "recovered", "failed"}

type MultiSink []EventSink

func (sinks MultiSink) Emit(ctx context.Context, event Event) error {
	for _, sink := range sinks {
		if err := sink.Emit(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

type MetricsFileSink struct{ Path string }

func (sink MetricsFileSink) Emit(_ context.Context, event Event) error {
	if !filepath.IsAbs(sink.Path) {
		return fmt.Errorf("metrics path must be absolute")
	}
	known := false
	var output strings.Builder
	output.WriteString("# HELP gha_scheduler_recovery_state Current scheduler recovery lifecycle state.\n")
	output.WriteString("# TYPE gha_scheduler_recovery_state gauge\n")
	for _, state := range lifecycleStates {
		value := 0
		if event.State == state {
			known = true
			value = 1
		}
		fmt.Fprintf(&output, "gha_scheduler_recovery_state{state=%q} %d\n", state, value)
	}
	if !known {
		return fmt.Errorf("unknown scheduler recovery lifecycle state %q", event.State)
	}
	output.WriteString("# HELP gha_scheduler_recovery_stuck_instances Stuck instance count in the latest lifecycle event.\n")
	output.WriteString("# TYPE gha_scheduler_recovery_stuck_instances gauge\n")
	fmt.Fprintf(&output, "gha_scheduler_recovery_stuck_instances %d\n", len(event.Stuck))
	output.WriteString("# HELP gha_scheduler_recovery_last_event_timestamp_seconds Unix timestamp of the latest lifecycle event.\n")
	output.WriteString("# TYPE gha_scheduler_recovery_last_event_timestamp_seconds gauge\n")
	fmt.Fprintf(&output, "gha_scheduler_recovery_last_event_timestamp_seconds %d\n", event.At.Unix())
	return atomicTextFile(sink.Path, []byte(output.String()))
}

func atomicTextFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create metrics directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".scheduler-recovery-metrics-")
	if err != nil {
		return fmt.Errorf("create temporary metrics: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return fmt.Errorf("chmod temporary metrics: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary metrics: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary metrics: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary metrics: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish metrics: %w", err)
	}
	return nil
}
