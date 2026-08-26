package vanishedjob

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const recoveryHistoryLimit = 128
const maximumStateBytes = 1024 * 1024

type Result struct {
	Key                string    `json:"key"`
	OriginalAttempt    int       `json:"original_attempt"`
	ReplacementAttempt int       `json:"replacement_attempt"`
	Conclusion         string    `json:"conclusion"`
	FinishedAt         time.Time `json:"finished_at"`
}

type fileState struct {
	SchemaVersion int               `json:"schema_version"`
	Generation    uint64            `json:"generation"`
	Records       map[string]Record `json:"records"`
	Finished      []Result          `json:"finished"`
}

type FileStore struct {
	Path     string
	LockPath string
}

func RecordKey(repository string, runID int64, attempt int) string {
	return repository + "/runs/" + strconv.FormatInt(runID, 10) + "/attempts/" + strconv.Itoa(attempt)
}

func (store FileStore) Get(key string) (*Record, error) {
	var found *Record
	err := store.readLocked(func(state fileState) error {
		if record, exists := state.Records[key]; exists {
			copy := record
			found = &copy
		}
		return nil
	})
	return found, err
}

func (store FileStore) ForRun(repository string, runID int64) (*Record, string, error) {
	var found *Record
	var key string
	err := store.readLocked(func(state fileState) error {
		for candidateKey, record := range state.Records {
			if record.Repository != repository || record.RunID != runID {
				continue
			}
			if found != nil {
				return fmt.Errorf("multiple vanished-runner recoveries exist for one run")
			}
			copy := record
			found, key = &copy, candidateKey
		}
		return nil
	})
	return found, key, err
}

func (store FileStore) Begin(record Record) (bool, error) {
	if err := record.Validate(); err != nil {
		return false, err
	}
	key := RecordKey(record.Repository, record.RunID, record.OriginalAttempt)
	created := false
	err := store.locked(func(state *fileState) (bool, error) {
		if _, exists := state.Records[key]; exists {
			return false, nil
		}
		for _, result := range state.Finished {
			if result.Key == key {
				return false, nil
			}
		}
		state.Records[key] = record
		created = true
		return true, nil
	})
	return created, err
}

func (store FileStore) Advance(key string, expected, next Stage, at time.Time) error {
	if at.IsZero() || expected == StageDetected && next != StageCancelRequested || expected == StageCancelRequested && next != StageRerunRequested || expected == StageRerunRequested {
		return fmt.Errorf("vanished-runner recovery transition is invalid")
	}
	return store.locked(func(state *fileState) (bool, error) {
		record, exists := state.Records[key]
		if !exists || record.Stage != expected {
			return false, fmt.Errorf("vanished-runner recovery stage changed")
		}
		record.Stage, record.UpdatedAt = next, at.UTC()
		state.Records[key] = record
		return true, nil
	})
}

func (store FileStore) Finish(key string, expected Stage, result Result) error {
	if result.ReplacementAttempt <= result.OriginalAttempt || result.Conclusion == "" {
		return fmt.Errorf("vanished-runner recovery result is invalid")
	}
	return store.locked(func(state *fileState) (bool, error) {
		record, exists := state.Records[key]
		if !exists || record.Stage != expected || result.Key != key || result.OriginalAttempt != record.OriginalAttempt || result.FinishedAt.IsZero() {
			return false, fmt.Errorf("vanished-runner recovery finish identity changed")
		}
		delete(state.Records, key)
		state.Finished = append(state.Finished, result)
		if len(state.Finished) > recoveryHistoryLimit {
			state.Finished = slices.Clone(state.Finished[len(state.Finished)-recoveryHistoryLimit:])
		}
		return true, nil
	})
}

func (store FileStore) locked(update func(*fileState) (bool, error)) error {
	if !filepath.IsAbs(store.Path) || !filepath.IsAbs(store.LockPath) || filepath.Clean(store.Path) == string(filepath.Separator) || filepath.Clean(store.LockPath) == string(filepath.Separator) || filepath.Clean(store.Path) == filepath.Clean(store.LockPath) {
		return fmt.Errorf("vanished-runner recovery paths are unsafe")
	}
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(store.LockPath), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(store.LockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck
	state, err := readState(store.Path)
	if err != nil {
		return err
	}
	changed, err := update(&state)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	state.Generation++
	return writeState(store.Path, state)
}

func (store FileStore) readLocked(read func(fileState) error) error {
	if !filepath.IsAbs(store.Path) || !filepath.IsAbs(store.LockPath) || filepath.Clean(store.Path) == filepath.Clean(store.LockPath) {
		return fmt.Errorf("vanished-runner recovery paths are unsafe")
	}
	if _, err := os.Stat(store.Path); os.IsNotExist(err) {
		return read(fileState{SchemaVersion: 1, Records: map[string]Record{}})
	} else if err != nil {
		return err
	}
	lock, err := os.OpenFile(store.LockPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_SH); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck
	state, err := readState(store.Path)
	if err != nil {
		return err
	}
	return read(state)
}

func readState(path string) (fileState, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return fileState{SchemaVersion: 1, Records: map[string]Record{}}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumStateBytes+1))
	if err != nil || len(data) > maximumStateBytes {
		return fileState{}, fmt.Errorf("vanished-runner recovery state exceeds its bounded size")
	}
	var state fileState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || state.SchemaVersion != 1 || state.Records == nil {
		return fileState{}, fmt.Errorf("vanished-runner recovery state is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fileState{}, fmt.Errorf("vanished-runner recovery state has trailing content")
	}
	for key, record := range state.Records {
		if err := record.Validate(); err != nil || key != RecordKey(record.Repository, record.RunID, record.OriginalAttempt) {
			return fileState{}, fmt.Errorf("vanished-runner recovery record %q is invalid", key)
		}
	}
	return state, nil
}

func (record Record) Validate() error {
	parts := strings.Split(record.Repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || record.RunID <= 0 || record.JobID <= 0 || record.RunnerID <= 0 || strings.TrimSpace(record.RunnerName) == "" || strings.TrimSpace(record.ScaleSet) == "" || record.OriginalAttempt <= 0 || record.UpdatedAt.IsZero() || record.Stage != StageDetected && record.Stage != StageCancelRequested && record.Stage != StageRerunRequested {
		return fmt.Errorf("vanished-runner recovery record identity is invalid")
	}
	return nil
}

func writeState(path string, state fileState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".vanished-job-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
