package workerdiagnostics

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion           = 1
	DefaultMaxArtifacts     = 32
	DefaultMaxArtifactBytes = 2 * 1024 * 1024
)

var (
	instanceNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	bundleNamePattern   = regexp.MustCompile(`^runner-diagnostics-v1-[a-z0-9][a-z0-9-]{0,62}-[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f]{12}\.tar\.gz$`)
)

// IsBundleName reports whether name belongs to the versioned diagnostic spool
// grammar. Exporters and observers use the same boundary as retention cleanup.
func IsBundleName(name string) bool {
	return bundleNamePattern.MatchString(name)
}

type Instance struct {
	Name             string `json:"name"`
	ControllerID     string `json:"controller_id"`
	PoolID           string `json:"pool_id"`
	PoolName         string `json:"pool_name"`
	ScaleSet         string `json:"scale_set"`
	Repository       string `json:"repository"`
	ImageFingerprint string `json:"image_fingerprint"`
	RunnerVersion    string `json:"runner_version"`
	ProviderVersion  string `json:"provider_version"`
	ProviderCommit   string `json:"provider_commit"`
	State            string `json:"state"`
}

type Artifact struct {
	Path      string
	Source    string
	Content   []byte
	Truncated bool
}

type ArtifactRecord struct {
	Path      string `json:"path"`
	Source    string `json:"source"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
	Truncated bool   `json:"truncated"`
}

type Manifest struct {
	SchemaVersion    int              `json:"schema_version"`
	CapturedAt       string           `json:"captured_at"`
	Instance         Instance         `json:"instance"`
	Artifacts        []ArtifactRecord `json:"artifacts"`
	CollectionErrors []string         `json:"collection_errors"`
}

type Result struct {
	Path               string `json:"path"`
	ArtifactCount      int    `json:"artifact_count"`
	UncompressedBytes  int64  `json:"uncompressed_bytes"`
	CollectionFailures int    `json:"collection_failures"`
}

type Store struct {
	Directory      string
	Retention      time.Duration
	MaxBundleBytes int64
	MaxTotalBytes  int64
	MaxArtifacts   int
	Now            func() time.Time
	Random         io.Reader
}

type storedArtifact struct {
	record  ArtifactRecord
	content []byte
}

func (s Store) Write(
	ctx context.Context,
	instance Instance,
	artifacts []Artifact,
	collectionErrors []string,
) (Result, error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	if err := validateInstance(instance); err != nil {
		return Result{}, err
	}
	if err := validatePrivateDirectory(s.Directory); err != nil {
		return Result{}, fmt.Errorf("validate diagnostics directory: %w", err)
	}
	now := s.now()
	if err := s.prune(now, ""); err != nil {
		return Result{}, fmt.Errorf("prune diagnostics before capture: %w", err)
	}

	stored, total, normalizedErrors, err := s.prepare(ctx, artifacts, collectionErrors)
	if err != nil {
		return Result{}, err
	}
	manifest := Manifest{
		SchemaVersion:    SchemaVersion,
		CapturedAt:       now.Format(time.RFC3339Nano),
		Instance:         instance,
		Artifacts:        make([]ArtifactRecord, 0, len(stored)),
		CollectionErrors: normalizedErrors,
	}
	for _, artifact := range stored {
		manifest.Artifacts = append(manifest.Artifacts, artifact.record)
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("encode diagnostic manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')

	name, err := s.bundleName(instance.Name, now)
	if err != nil {
		return Result{}, err
	}
	finalPath := filepath.Join(s.Directory, name)
	if err := s.writeBundle(ctx, finalPath, now, manifestBytes, stored); err != nil {
		return Result{}, err
	}
	if err := s.prune(now, finalPath); err != nil {
		return Result{}, fmt.Errorf("prune diagnostics after capture: %w", err)
	}
	return Result{
		Path:               finalPath,
		ArtifactCount:      len(stored),
		UncompressedBytes:  total,
		CollectionFailures: len(normalizedErrors),
	}, nil
}

func (s Store) prepare(
	ctx context.Context,
	artifacts []Artifact,
	collectionErrors []string,
) ([]storedArtifact, int64, []string, error) {
	maxArtifacts := s.MaxArtifacts
	if maxArtifacts == 0 {
		maxArtifacts = DefaultMaxArtifacts
	}
	if len(artifacts) > maxArtifacts {
		collectionErrors = append(collectionErrors, fmt.Sprintf(
			"artifact count %d exceeded limit %d; excess artifacts were omitted",
			len(artifacts), maxArtifacts,
		))
		artifacts = artifacts[:maxArtifacts]
	}
	sort.SliceStable(artifacts, func(i, j int) bool {
		if artifacts[i].Path == artifacts[j].Path {
			return artifacts[i].Source < artifacts[j].Source
		}
		return artifacts[i].Path < artifacts[j].Path
	})

	seen := make(map[string]struct{}, len(artifacts))
	stored := make([]storedArtifact, 0, len(artifacts))
	var total int64
	for _, artifact := range artifacts {
		if err := ctx.Err(); err != nil {
			return nil, 0, nil, fmt.Errorf("diagnostic collection interrupted: %w", err)
		}
		if err := validateArtifactPath(artifact.Path); err != nil {
			return nil, 0, nil, err
		}
		if _, exists := seen[artifact.Path]; exists {
			return nil, 0, nil, fmt.Errorf("duplicate diagnostic artifact path %q", artifact.Path)
		}
		seen[artifact.Path] = struct{}{}
		content := Redact(artifact.Content)
		truncated := artifact.Truncated
		if len(content) > DefaultMaxArtifactBytes {
			content = content[:DefaultMaxArtifactBytes]
			truncated = true
		}
		remaining := s.MaxBundleBytes - total
		if remaining <= 0 {
			collectionErrors = append(collectionErrors, "bundle byte limit reached; remaining artifacts were omitted")
			break
		}
		if int64(len(content)) > remaining {
			content = content[:remaining]
			truncated = true
		}
		digest := sha256.Sum256(content)
		stored = append(stored, storedArtifact{
			record: ArtifactRecord{
				Path:      artifact.Path,
				Source:    sanitizeDiagnosticText(artifact.Source, 512),
				Bytes:     len(content),
				SHA256:    hex.EncodeToString(digest[:]),
				Truncated: truncated,
			},
			content: content,
		})
		total += int64(len(content))
	}
	return stored, total, normalizeErrors(collectionErrors), nil
}

func (s Store) writeBundle(
	ctx context.Context,
	finalPath string,
	capturedAt time.Time,
	manifest []byte,
	artifacts []storedArtifact,
) error {
	temporary, err := os.CreateTemp(s.Directory, ".runner-diagnostics-*")
	if err != nil {
		return fmt.Errorf("create temporary diagnostic bundle: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set diagnostic bundle permissions: %w", err)
	}

	compressor := gzip.NewWriter(temporary)
	compressor.Header.ModTime = capturedAt
	archive := tar.NewWriter(compressor)
	write := func(name string, content []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		header := &tar.Header{
			Name:       name,
			Mode:       0o600,
			Size:       int64(len(content)),
			ModTime:    capturedAt,
			AccessTime: capturedAt,
			ChangeTime: capturedAt,
			Format:     tar.FormatPAX,
		}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		_, err := archive.Write(content)
		return err
	}
	if err := write("manifest.json", manifest); err != nil {
		return fmt.Errorf("write diagnostic manifest: %w", err)
	}
	for _, artifact := range artifacts {
		if err := write(artifact.record.Path, artifact.content); err != nil {
			return fmt.Errorf("write diagnostic artifact %q: %w", artifact.record.Path, err)
		}
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("close diagnostic archive: %w", err)
	}
	if err := compressor.Close(); err != nil {
		return fmt.Errorf("close diagnostic compressor: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync diagnostic bundle: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close diagnostic bundle: %w", err)
	}
	closed = true
	if err := os.Chtimes(temporaryPath, capturedAt, capturedAt); err != nil {
		return fmt.Errorf("set diagnostic bundle timestamp: %w", err)
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return fmt.Errorf("publish diagnostic bundle: %w", err)
	}
	return syncDirectory(s.Directory)
}

func (s Store) prune(now time.Time, preserve string) error {
	entries, err := os.ReadDir(s.Directory)
	if err != nil {
		return err
	}
	type candidate struct {
		path    string
		modTime time.Time
		size    int64
	}
	candidates := make([]candidate, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if !IsBundleName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		candidatePath := filepath.Join(s.Directory, entry.Name())
		if candidatePath != preserve && now.Sub(info.ModTime()) > s.Retention {
			if err := os.Remove(candidatePath); err != nil {
				return err
			}
			continue
		}
		candidates = append(candidates, candidate{path: candidatePath, modTime: info.ModTime(), size: info.Size()})
		total += info.Size()
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].modTime.Equal(candidates[j].modTime) {
			return candidates[i].path < candidates[j].path
		}
		return candidates[i].modTime.Before(candidates[j].modTime)
	})
	for _, candidate := range candidates {
		if total <= s.MaxTotalBytes {
			break
		}
		if candidate.path == preserve {
			continue
		}
		if err := os.Remove(candidate.path); err != nil {
			return err
		}
		total -= candidate.size
	}
	if total > s.MaxTotalBytes {
		return fmt.Errorf("diagnostic spool exceeds %d bytes after bounded pruning", s.MaxTotalBytes)
	}
	return syncDirectory(s.Directory)
}

func (s Store) bundleName(instance string, capturedAt time.Time) (string, error) {
	random := s.Random
	if random == nil {
		random = rand.Reader
	}
	suffix := make([]byte, 6)
	if _, err := io.ReadFull(random, suffix); err != nil {
		return "", fmt.Errorf("generate diagnostic bundle identity: %w", err)
	}
	return fmt.Sprintf(
		"runner-diagnostics-v1-%s-%s-%s.tar.gz",
		instance,
		capturedAt.Format("20060102T150405.000000000Z"),
		hex.EncodeToString(suffix),
	), nil
}

func (s Store) validate() error {
	if !filepath.IsAbs(s.Directory) || filepath.Clean(s.Directory) == string(filepath.Separator) {
		return errors.New("diagnostics directory must be absolute and bounded")
	}
	if s.Retention < time.Hour || s.Retention > 30*24*time.Hour {
		return errors.New("diagnostics retention must be between one hour and 30 days")
	}
	if s.MaxBundleBytes < 1024 || s.MaxBundleBytes > 64*1024*1024 {
		return errors.New("diagnostic bundle limit must be between 1 KiB and 64 MiB")
	}
	if s.MaxTotalBytes < s.MaxBundleBytes || s.MaxTotalBytes > 16*1024*1024*1024 {
		return errors.New("diagnostic total limit must fit one bundle and not exceed 16 GiB")
	}
	if s.MaxArtifacts < 0 || s.MaxArtifacts > 128 {
		return errors.New("diagnostic artifact limit must not exceed 128")
	}
	return nil
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func validatePrivateDirectory(directory string) error {
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	if filepath.Clean(resolved) != filepath.Clean(directory) {
		return errors.New("diagnostics directory must not traverse symlinks")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("diagnostics directory must be a real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("diagnostics directory must not grant group or other access")
	}
	return nil
}

func validateInstance(instance Instance) error {
	if !instanceNamePattern.MatchString(instance.Name) {
		return fmt.Errorf("invalid diagnostic instance name %q", instance.Name)
	}
	for name, value := range map[string]string{
		"controller ID": instance.ControllerID,
		"pool ID":       instance.PoolID,
		"pool name":     instance.PoolName,
		"image digest":  instance.ImageFingerprint,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("diagnostic %s is missing or unsafe", name)
		}
	}
	return nil
}

func validateArtifactPath(value string) error {
	if value == "" || value == "." || path.IsAbs(value) || path.Clean(value) != value || strings.Contains(value, `\`) ||
		strings.HasPrefix(value, "../") || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("unsafe diagnostic artifact path %q", value)
	}
	return nil
}

func normalizeErrors(values []string) []string {
	if len(values) > 32 {
		values = append(values[:31], "additional collection failures were omitted")
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = sanitizeDiagnosticText(value, 1024)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func sanitizeDiagnosticText(value string, limit int) string {
	value = strings.TrimSpace(string(Redact([]byte(value))))
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\x00' {
			return ' '
		}
		return character
	}, value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
