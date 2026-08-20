package diagnosticexport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/unix"
)

const (
	stateSchemaVersion    = 1
	maxStateBytes         = 16 * 1024 * 1024
	stateFilename         = "state.json"
	progressSchemaVersion = 1
	progressFilename      = "progress.json"
	maxProgressBytes      = 64 * 1024
)

// StageCanary is the only stage this exporter may run in while RustFS is a
// release candidate.
const StageCanary = "canary"

// AcceptedStages is the closed set of deployment stages the fleet accepts for
// the diagnostic exporter, and it is deliberately one declaration rather than
// two equal literals.
//
// The exporter refuses to start outside this set, and the observer refuses to
// grade a status reported from outside it. Those are the same policy read by
// two processes, and they were two independent string comparisons: promoting
// the exporter would have relaxed its own validator while leaving the observer
// refusing the very status the promotion produced, turning a successful
// promotion into a fail-closed observer on every serving host.
//
// Promotion is therefore one edit here, and TestAcceptedStagesAreTheOnlyStageGate
// fails if a second literal reappears anywhere.
func AcceptedStages() []string {
	return []string{StageCanary}
}

// StageAccepted reports whether a stage may run and be graded.
func StageAccepted(stage string) bool {
	return slices.Contains(AcceptedStages(), stage)
}

type Status struct {
	SchemaVersion       int    `json:"schema_version"`
	DeploymentStage     string `json:"deployment_stage"`
	ObservedAt          string `json:"observed_at"`
	LastSuccessAt       string `json:"last_success_at,omitempty"`
	LastErrorCode       string `json:"last_error_code,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	SourceBundles       int    `json:"source_bundles"`
	ExportedBundles     int    `json:"exported_bundles"`
	PendingBundles      int    `json:"pending_bundles"`
	SourceBytes         int64  `json:"source_bytes"`
	ExportedBytes       int64  `json:"exported_bytes"`
	LastProgressAt      string `json:"-"`
	LastFullSyncAt      string `json:"-"`
	ScannedBundles      int    `json:"-"`
	DeletedBundles      int    `json:"-"`
}

type Progress struct {
	SchemaVersion   int    `json:"schema_version"`
	ObservedAt      string `json:"observed_at"`
	LastProgressAt  string `json:"last_progress_at,omitempty"`
	LastFullSyncAt  string `json:"last_full_sync_at,omitempty"`
	ScannedBundles  int    `json:"scanned_bundles"`
	ExportedBundles int    `json:"exported_bundles"`
	DeletedBundles  int    `json:"deleted_bundles"`
	FailedBundles   int    `json:"failed_bundles"`
	BacklogBundles  int    `json:"backlog_bundles"`
	BacklogBytes    int64  `json:"backlog_bytes"`
}

type ExportRecord struct {
	SHA256         string `json:"sha256"`
	ObjectKey      string `json:"object_key"`
	Bytes          int64  `json:"bytes"`
	ExportedAt     string `json:"exported_at"`
	LastVerifiedAt string `json:"last_verified_at"`
}

type State struct {
	SchemaVersion int                     `json:"schema_version"`
	Status        Status                  `json:"status"`
	Exports       map[string]ExportRecord `json:"exports"`
}

type StateStore struct {
	Directory string
}

func (s StateStore) Load() (State, error) {
	if err := validateStateDirectory(s.Directory); err != nil {
		return State{}, err
	}
	return readState(s.Directory)
}

func readState(directory string) (State, error) {
	directoryStat, err := stateDirectoryStat(directory)
	if err != nil {
		return State{}, err
	}
	filename := filepath.Join(directory, stateFilename)
	fd, err := unix.Open(filename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return State{SchemaVersion: stateSchemaVersion, Exports: make(map[string]ExportRecord)}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("open diagnostic export state: %w", err)
	}
	file := os.NewFile(uintptr(fd), filename)
	if file == nil {
		_ = unix.Close(fd)
		return State{}, errors.New("open diagnostic export state: invalid file descriptor")
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return State{}, fmt.Errorf("stat diagnostic export state: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != directoryStat.Uid ||
		stat.Gid != directoryStat.Gid || stat.Mode&0o777 != 0o640 || stat.Size < 1 || stat.Size > maxStateBytes {
		return State{}, errors.New("diagnostic export state ownership, type, link count, mode or size is unsafe")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil {
		return State{}, fmt.Errorf("read diagnostic export state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var state State
	if err := decoder.Decode(&state); err != nil {
		return State{}, fmt.Errorf("decode diagnostic export state: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return State{}, errors.New("diagnostic export state has trailing JSON")
	}
	if state.SchemaVersion != stateSchemaVersion || state.Exports == nil {
		return State{}, errors.New("diagnostic export state schema is unsupported")
	}
	return state, nil
}

func (s StateStore) Save(state State) error {
	if err := validateStateDirectory(s.Directory); err != nil {
		return err
	}
	state.SchemaVersion = stateSchemaVersion
	if state.Exports == nil {
		state.Exports = make(map[string]ExportRecord)
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode diagnostic export state: %w", err)
	}
	content = append(content, '\n')
	if len(content) > maxStateBytes {
		return errors.New("diagnostic export state exceeds its bounded size")
	}
	temporary, err := os.CreateTemp(s.Directory, ".state-*.json")
	if err != nil {
		return fmt.Errorf("create diagnostic export state: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o640); err != nil {
		return fmt.Errorf("set diagnostic export state mode: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write diagnostic export state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync diagnostic export state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close diagnostic export state: %w", err)
	}
	closed = true
	if err := os.Rename(temporaryPath, filepath.Join(s.Directory, stateFilename)); err != nil {
		return fmt.Errorf("publish diagnostic export state: %w", err)
	}
	return syncStateDirectory(s.Directory)
}

func (s StateStore) LoadProgress() (Progress, error) {
	if err := validateStateDirectory(s.Directory); err != nil {
		return Progress{}, err
	}
	return readProgress(s.Directory)
}

func readProgress(directory string) (Progress, error) {
	filename := filepath.Join(directory, progressFilename)
	content, err := readBoundedStateFile(filename, maxProgressBytes, "diagnostic export progress")
	if errors.Is(err, os.ErrNotExist) {
		return Progress{SchemaVersion: progressSchemaVersion}, nil
	}
	if err != nil {
		return Progress{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var progress Progress
	if err := decoder.Decode(&progress); err != nil {
		return Progress{}, fmt.Errorf("decode diagnostic export progress: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF || progress.SchemaVersion != progressSchemaVersion {
		return Progress{}, errors.New("diagnostic export progress schema is unsupported")
	}
	if err := progress.Validate(); err != nil {
		return Progress{}, err
	}
	return progress, nil
}

func (s StateStore) SaveProgress(progress Progress) error {
	if err := validateStateDirectory(s.Directory); err != nil {
		return err
	}
	progress.SchemaVersion = progressSchemaVersion
	if err := progress.Validate(); err != nil {
		return err
	}
	content, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		return fmt.Errorf("encode diagnostic export progress: %w", err)
	}
	content = append(content, '\n')
	if len(content) > maxProgressBytes {
		return errors.New("diagnostic export progress exceeds its bounded size")
	}
	return writeStateFile(s.Directory, progressFilename, content)
}

func (p Progress) Validate() error {
	if p.SchemaVersion != progressSchemaVersion || p.ScannedBundles < 0 || p.ExportedBundles < 0 ||
		p.DeletedBundles < 0 || p.FailedBundles < 0 || p.BacklogBundles < 0 || p.BacklogBytes < 0 ||
		p.DeletedBundles > p.ExportedBundles || p.ExportedBundles > p.ScannedBundles || p.FailedBundles > p.ScannedBundles {
		return errors.New("diagnostic export progress counters are invalid")
	}
	observedAt, err := parseCanonicalUTCTime(p.ObservedAt)
	if err != nil {
		return errors.New("diagnostic export progress observation is invalid")
	}
	for label, value := range map[string]string{"last_progress_at": p.LastProgressAt, "last_full_sync_at": p.LastFullSyncAt} {
		if value == "" {
			continue
		}
		parsed, err := parseCanonicalUTCTime(value)
		if err != nil || parsed.After(observedAt) {
			return fmt.Errorf("diagnostic export progress %s is invalid", label)
		}
	}
	return nil
}

func ReadStatus(directory string) (Status, error) {
	if err := validateReadableStateDirectory(directory); err != nil {
		return Status{}, err
	}
	state, err := readState(directory)
	if err != nil {
		return Status{}, err
	}
	if state.Status.SchemaVersion != stateSchemaVersion {
		return Status{}, errors.New("diagnostic export status is unavailable")
	}
	progress, err := readProgress(directory)
	if err != nil {
		return Status{}, err
	}
	state.Status.LastProgressAt = progress.LastProgressAt
	state.Status.LastFullSyncAt = progress.LastFullSyncAt
	state.Status.ScannedBundles = progress.ScannedBundles
	state.Status.DeletedBundles = progress.DeletedBundles
	return state.Status, nil
}

func readBoundedStateFile(filename string, limit int64, description string) ([]byte, error) {
	directoryStat, err := stateDirectoryStat(filepath.Dir(filename))
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(filename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filename)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open %s: invalid file descriptor", description)
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 ||
		stat.Uid != directoryStat.Uid || stat.Gid != directoryStat.Gid || stat.Mode&0o777 != 0o640 || stat.Size < 1 || stat.Size > limit {
		return nil, fmt.Errorf("%s mode, type or size is unsafe", description)
	}
	return io.ReadAll(io.LimitReader(file, limit+1))
}

func writeStateFile(directory, name string, content []byte) error {
	temporary, err := os.CreateTemp(directory, "."+name+"-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o640); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, filepath.Join(directory, name)); err != nil {
		return err
	}
	return syncStateDirectory(directory)
}

func (s Status) ObservedTime() (time.Time, error) {
	return parseCanonicalUTCTime(s.ObservedAt)
}

func (s Status) LastSuccessTime() (time.Time, error) {
	if s.LastSuccessAt == "" {
		return time.Time{}, nil
	}
	return parseCanonicalUTCTime(s.LastSuccessAt)
}

func (s Status) LastProgressTime() (time.Time, error) {
	if s.LastProgressAt == "" {
		return time.Time{}, nil
	}
	return parseCanonicalUTCTime(s.LastProgressAt)
}

func (s Status) LastFullSyncTime() (time.Time, error) {
	if s.LastFullSyncAt == "" {
		return time.Time{}, nil
	}
	return parseCanonicalUTCTime(s.LastFullSyncAt)
}

func parseCanonicalUTCTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("diagnostic export timestamp must be canonical UTC RFC3339")
	}
	return parsed, nil
}

func validateStateDirectory(directory string) error {
	stat, err := stateDirectoryStat(directory)
	if err != nil {
		return err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o700 != 0o700 {
		return errors.New("diagnostic export state directory ownership or owner mode is unsafe")
	}
	return nil
}

func validateReadableStateDirectory(directory string) error {
	_, err := stateDirectoryStat(directory)
	return err
}

func stateDirectoryStat(directory string) (unix.Stat_t, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) == string(filepath.Separator) {
		return unix.Stat_t{}, errors.New("diagnostic export state directory must be absolute and bounded")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return unix.Stat_t{}, fmt.Errorf("resolve diagnostic export state directory: %w", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(directory) {
		return unix.Stat_t{}, errors.New("diagnostic export state directory must not traverse symlinks")
	}
	var stat unix.Stat_t
	if err := unix.Lstat(directory, &stat); err != nil {
		return unix.Stat_t{}, fmt.Errorf("stat diagnostic export state directory: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o007 != 0 {
		return unix.Stat_t{}, errors.New("diagnostic export state directory type or mode is unsafe")
	}
	return stat, nil
}

func syncStateDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
