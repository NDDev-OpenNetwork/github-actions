package schedulerrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type CommandConfig struct {
	Checkpoint []string
	Restart    []string
	Progress   []string
	Timeout    time.Duration
}

type CommandExecutor struct {
	Config CommandConfig
}

type progressOutput struct {
	Progressed []string `json:"progressed"`
	Remaining  []string `json:"remaining"`
}

func (executor CommandExecutor) Validate() error {
	if executor.Config.Timeout <= 0 {
		return fmt.Errorf("command timeout must be positive")
	}
	for name, argv := range map[string][]string{
		"checkpoint": executor.Config.Checkpoint,
		"restart":    executor.Config.Restart,
		"progress":   executor.Config.Progress,
	} {
		if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
			return fmt.Errorf("%s command must use an absolute executable", name)
		}
		for _, argument := range argv {
			if strings.ContainsRune(argument, 0) {
				return fmt.Errorf("%s command contains a NUL argument", name)
			}
		}
	}
	return nil
}

func (executor CommandExecutor) Checkpoint(ctx context.Context, attempt Attempt) (string, error) {
	output, err := executor.run(ctx, executor.Config.Checkpoint, attempt)
	if err != nil {
		return "", err
	}
	checkpoint := strings.TrimSpace(string(output))
	if checkpoint == "" || strings.ContainsAny(checkpoint, "\r\n") {
		return "", fmt.Errorf("checkpoint command returned an invalid identity")
	}
	return checkpoint, nil
}

func (executor CommandExecutor) RestartDispatcher(ctx context.Context, attempt Attempt) error {
	_, err := executor.run(ctx, executor.Config.Restart, attempt)
	return err
}

func (executor CommandExecutor) AwaitProgress(ctx context.Context, attempt Attempt) ([]string, []string, error) {
	output, err := executor.run(ctx, executor.Config.Progress, attempt)
	if err != nil {
		return nil, nil, err
	}
	var progress progressOutput
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&progress); err != nil {
		return nil, nil, fmt.Errorf("decode progress output: %w", err)
	}
	return progress.Progressed, progress.Remaining, nil
}

func (executor CommandExecutor) run(ctx context.Context, argv []string, attempt Attempt) ([]byte, error) {
	if err := executor.Validate(); err != nil {
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, executor.Config.Timeout)
	defer cancel()
	var output []byte
	var err error
	for executionAttempt := 0; executionAttempt < 3; executionAttempt++ {
		command := exec.CommandContext(bounded, argv[0], argv[1:]...)
		command.Env = append(command.Environ(),
			"GHA_SCHEDULER_RECOVERY_ATTEMPT="+attempt.ID,
			"GHA_SCHEDULER_RECOVERY_STUCK="+strings.Join(attempt.Stuck, ","),
		)
		output, err = command.Output()
		if !errors.Is(err, syscall.ETXTBSY) {
			break
		}
		select {
		case <-bounded.Done():
			break
		case <-time.After(5 * time.Millisecond):
		}
	}
	if bounded.Err() != nil {
		return nil, fmt.Errorf("command timed out: %w", bounded.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("command failed: %w", err)
	}
	return output, nil
}
