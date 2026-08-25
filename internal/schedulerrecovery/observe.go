package schedulerrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type CommandObserver struct {
	Argv    []string
	Timeout time.Duration
}

type observationOutput struct {
	ObservedAt           time.Time       `json:"observed_at"`
	ActiveIntents        int             `json:"active_intents"`
	PendingCreates       []PendingCreate `json:"pending_creates"`
	ManagerUptimeSeconds int64           `json:"manager_uptime_seconds"`
	LastRecoveryAt       time.Time       `json:"last_recovery_at"`
	RecoveryRunning      bool            `json:"recovery_running"`
}

func (observer CommandObserver) Validate() error {
	if observer.Timeout <= 0 {
		return fmt.Errorf("observation timeout must be positive")
	}
	if len(observer.Argv) == 0 || !filepath.IsAbs(observer.Argv[0]) {
		return fmt.Errorf("observation command must use an absolute executable")
	}
	for _, argument := range observer.Argv {
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf("observation command contains a NUL argument")
		}
	}
	return nil
}

func (observer CommandObserver) Observe(ctx context.Context) (Observation, error) {
	if err := observer.Validate(); err != nil {
		return Observation{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, observer.Timeout)
	defer cancel()
	command := exec.CommandContext(bounded, observer.Argv[0], observer.Argv[1:]...)
	output, err := command.Output()
	if bounded.Err() != nil {
		return Observation{}, fmt.Errorf("observation command timed out: %w", bounded.Err())
	}
	if err != nil {
		return Observation{}, fmt.Errorf("observation command failed: %w", err)
	}
	var decoded observationOutput
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return Observation{}, fmt.Errorf("decode scheduler observation: %w", err)
	}
	if decoded.ObservedAt.IsZero() || decoded.ActiveIntents < 0 || decoded.ManagerUptimeSeconds < 0 {
		return Observation{}, fmt.Errorf("scheduler observation contains invalid values")
	}
	for _, pending := range decoded.PendingCreates {
		if pending.ID == "" || pending.Age < 0 || pending.CreateAttempt < 0 {
			return Observation{}, fmt.Errorf("scheduler observation contains an invalid pending create")
		}
	}
	return Observation{
		ObservedAt: decoded.ObservedAt, ActiveIntents: decoded.ActiveIntents,
		PendingCreates: decoded.PendingCreates, ManagerUptime: time.Duration(decoded.ManagerUptimeSeconds) * time.Second,
		LastRecoveryAt: decoded.LastRecoveryAt, RecoveryRunning: decoded.RecoveryRunning,
	}, nil
}
