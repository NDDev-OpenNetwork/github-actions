package schedulerrecovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/unix"
)

const historyLimit = 64

type fileState struct {
	SchemaVersion int                `json:"schema_version"`
	Heartbeat     Heartbeat          `json:"heartbeat"`
	Active        map[string]Attempt `json:"active"`
	Finished      []Result           `json:"finished"`
}

type FileStore struct {
	Path     string
	LockPath string
}

type Heartbeat struct {
	At       time.Time `json:"at"`
	Progress string    `json:"progress"`
}

func (store FileStore) RecordHeartbeat(_ context.Context, heartbeat Heartbeat) error {
	if heartbeat.At.IsZero() || heartbeat.Progress == "" {
		return fmt.Errorf("heartbeat time and progress identity are required")
	}
	return store.locked(func(state *fileState) error {
		if !state.Heartbeat.At.IsZero() && heartbeat.At.Before(state.Heartbeat.At) {
			return fmt.Errorf("heartbeat time moved backwards")
		}
		state.Heartbeat = heartbeat
		return nil
	})
}

func (store FileStore) ReadHeartbeat(_ context.Context) (Heartbeat, error) {
	var heartbeat Heartbeat
	err := store.locked(func(state *fileState) error {
		heartbeat = state.Heartbeat
		return nil
	})
	return heartbeat, err
}

func (store FileStore) Begin(_ context.Context, attempt Attempt) (bool, error) {
	acquired := false
	err := store.locked(func(state *fileState) error {
		if _, exists := state.Active[attempt.ID]; exists {
			return nil
		}
		for _, result := range state.Finished {
			if result.AttemptID == attempt.ID {
				return nil
			}
		}
		state.Active[attempt.ID] = attempt
		acquired = true
		return nil
	})
	return acquired, err
}

func (store FileStore) Finish(_ context.Context, result Result) error {
	return store.locked(func(state *fileState) error {
		if _, exists := state.Active[result.AttemptID]; !exists {
			return fmt.Errorf("attempt %s is not active", result.AttemptID)
		}
		delete(state.Active, result.AttemptID)
		state.Finished = append(state.Finished, result)
		if len(state.Finished) > historyLimit {
			state.Finished = slices.Clone(state.Finished[len(state.Finished)-historyLimit:])
		}
		return nil
	})
}

func (store FileStore) locked(update func(*fileState) error) error {
	if store.Path == "" || store.LockPath == "" {
		return fmt.Errorf("state and lock paths are required")
	}
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(store.LockPath), 0o700); err != nil {
		return fmt.Errorf("create lock directory: %w", err)
	}
	lock, err := os.OpenFile(store.LockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open state lock: %w", err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock state: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck
	state, err := readFileState(store.Path)
	if err != nil {
		return err
	}
	if err := update(&state); err != nil {
		return err
	}
	return writeFileState(store.Path, state)
}

func readFileState(path string) (fileState, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fileState{SchemaVersion: 1, Active: map[string]Attempt{}}, nil
	}
	if err != nil {
		return fileState{}, fmt.Errorf("read recovery state: %w", err)
	}
	var state fileState
	if err := json.Unmarshal(data, &state); err != nil {
		return fileState{}, fmt.Errorf("decode recovery state: %w", err)
	}
	if state.SchemaVersion != 1 || state.Active == nil {
		return fileState{}, fmt.Errorf("unsupported recovery state")
	}
	return state, nil
}

func writeFileState(path string, state fileState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode recovery state: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".scheduler-recovery-")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("chmod temporary state: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish recovery state: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open state directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}
