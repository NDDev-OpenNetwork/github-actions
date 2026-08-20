package diagnosticexport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
	"golang.org/x/sys/unix"
)

const (
	maxManifestBytes = 1024 * 1024
	// maxBundlesPerRun bounds remote work and state growth for one timer
	// invocation. The spool itself is a durable WAL and may legitimately be
	// larger while the object store is unavailable.
	maxBundlesPerRun = 256
)

type Bundle struct {
	Name       string
	Content    []byte
	SHA256     string
	Manifest   workerdiagnostics.Manifest
	CapturedAt time.Time
	ObjectKey  string
	device     uint64
	inode      uint64
}

type bundleScope uint8

const (
	bundleScopeRepository bundleScope = iota + 1
	bundleScopeAccount
	bundleScopeUnassignedWarm
)

func ListBundles(config Config) ([]string, error) {
	directory, _, _, err := openSourceDirectory(config.SourceDirectory)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	names := make([]string, 0)
	for {
		entries, readErr := directory.ReadDir(256)
		for _, entry := range entries {
			if workerdiagnostics.IsBundleName(entry.Name()) {
				names = append(names, entry.Name())
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read diagnostic spool: %w", readErr)
		}
	}
	sort.Strings(names)
	return names, nil
}

// ListBundleBatch returns the oldest bounded slice of the durable spool using
// the source inode modification time. Ordering by filename is not a global FIFO
// because the instance name precedes the capture timestamp. Invalid bundles
// remain visible and sort first so corruption cannot hide behind healthy traffic.
func ListBundleBatch(ctx context.Context, config Config, limit int) ([]string, int, int64, error) {
	if limit < 1 {
		return nil, 0, 0, errors.New("diagnostic export batch limit must be positive")
	}
	directory, ownerUID, ownerGID, err := openSourceDirectory(config.SourceDirectory)
	if err != nil {
		return nil, 0, 0, err
	}
	defer directory.Close()
	type candidate struct {
		name    string
		modTime time.Time
	}
	candidates := make([]candidate, 0)
	var totalBytes int64
	for {
		entries, readErr := directory.ReadDir(256)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, len(candidates), totalBytes, err
			}
			if !workerdiagnostics.IsBundleName(entry.Name()) {
				continue
			}
			var stat unix.Stat_t
			if err := unix.Fstatat(int(directory.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return nil, len(candidates), totalBytes, fmt.Errorf("stat diagnostic spool entry: %w", err)
			}
			if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != ownerUID ||
				stat.Gid != ownerGID || stat.Mode&0o777 != 0o600 || stat.Size < 1 || stat.Size > config.MaxBundleBytes {
				// Keep unsafe entries visible at the front of the batch. ReadBundle
				// will produce the precise fail-closed diagnostic.
				candidates = append(candidates, candidate{name: entry.Name()})
				continue
			}
			totalBytes += stat.Size
			candidates = append(candidates, candidate{name: entry.Name(), modTime: time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC()})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, len(candidates), totalBytes, fmt.Errorf("read diagnostic spool: %w", readErr)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].modTime.Equal(candidates[j].modTime) {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].modTime.Before(candidates[j].modTime)
	})
	total := len(candidates)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	batch := make([]string, len(candidates))
	for index := range candidates {
		batch[index] = candidates[index].name
	}
	return batch, total, totalBytes, nil
}

// RemoveBundle removes exactly the source inode verified by ReadBundle. A
// replacement or symlink race fails closed and remains in the WAL.
func RemoveBundle(config Config, bundle Bundle) error {
	directory, ownerUID, ownerGID, err := openSourceDirectory(config.SourceDirectory)
	if err != nil {
		return err
	}
	defer directory.Close()
	var stat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), bundle.Name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("stat confirmed diagnostic bundle: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != ownerUID ||
		stat.Gid != ownerGID || stat.Mode&0o777 != 0o600 || stat.Dev != bundle.device || stat.Ino != bundle.inode {
		return errors.New("confirmed diagnostic bundle identity changed before removal")
	}
	if err := unix.Unlinkat(int(directory.Fd()), bundle.Name, 0); err != nil {
		return fmt.Errorf("remove confirmed diagnostic bundle: %w", err)
	}
	return directory.Sync()
}

func ReadBundle(ctx context.Context, config Config, name string) (Bundle, error) {
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	if !workerdiagnostics.IsBundleName(name) || filepath.Base(name) != name {
		return Bundle{}, fmt.Errorf("unsafe diagnostic bundle name %q", name)
	}
	directory, ownerUID, ownerGID, err := openSourceDirectory(config.SourceDirectory)
	if err != nil {
		return Bundle{}, err
	}
	defer directory.Close()
	filename := filepath.Join(config.SourceDirectory, name)
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return Bundle{}, fmt.Errorf("open diagnostic bundle: %w", err)
	}
	file := os.NewFile(uintptr(fd), filename)
	if file == nil {
		_ = unix.Close(fd)
		return Bundle{}, errors.New("open diagnostic bundle: invalid file descriptor")
	}
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return Bundle{}, fmt.Errorf("stat diagnostic bundle: %w", err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Uid != ownerUID ||
		before.Gid != ownerGID ||
		before.Mode&0o777 != 0o600 {
		return Bundle{}, errors.New("diagnostic bundle ownership, type, link count or mode is unsafe")
	}
	if before.Size < 1 || before.Size > config.MaxBundleBytes {
		return Bundle{}, fmt.Errorf("diagnostic bundle size %d is outside the configured boundary", before.Size)
	}
	content, err := io.ReadAll(io.LimitReader(file, config.MaxBundleBytes+1))
	if err != nil {
		return Bundle{}, fmt.Errorf("read diagnostic bundle: %w", err)
	}
	if int64(len(content)) != before.Size {
		return Bundle{}, errors.New("diagnostic bundle size changed while reading")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return Bundle{}, fmt.Errorf("restat diagnostic bundle: %w", err)
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size ||
		before.Mode != after.Mode || before.Uid != after.Uid || before.Gid != after.Gid ||
		before.Mtim != after.Mtim || before.Ctim != after.Ctim || after.Nlink != 1 {
		return Bundle{}, errors.New("diagnostic bundle changed while reading")
	}
	digest := sha256.Sum256(content)
	manifest, capturedAt, err := verifyArchive(config, name, content)
	if err != nil {
		return Bundle{}, err
	}
	digestHex := hex.EncodeToString(digest[:])
	objectKey, err := objectKey(config, manifest.Instance, capturedAt, digestHex)
	if err != nil {
		return Bundle{}, fmt.Errorf("derive diagnostic object key: %w", err)
	}
	return Bundle{
		Name:       name,
		Content:    content,
		SHA256:     digestHex,
		Manifest:   manifest,
		CapturedAt: capturedAt,
		ObjectKey:  objectKey,
		device:     before.Dev,
		inode:      before.Ino,
	}, nil
}

func verifyArchive(config Config, name string, content []byte) (workerdiagnostics.Manifest, time.Time, error) {
	compressed := bytes.NewReader(content)
	decompressor, err := gzip.NewReader(compressed)
	if err != nil {
		return workerdiagnostics.Manifest{}, time.Time{}, fmt.Errorf("open diagnostic gzip: %w", err)
	}
	decompressor.Multistream(false)
	defer decompressor.Close()
	limited := &countingReader{reader: decompressor, remaining: config.MaxDecompressedBytes}
	archive := tar.NewReader(limited)
	entries := make(map[string][]byte)
	order := make([]string, 0, workerdiagnostics.DefaultMaxArtifacts+1)
	for {
		if limited.exceeded {
			return workerdiagnostics.Manifest{}, time.Time{}, errors.New("diagnostic archive exceeds decompressed byte limit")
		}
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return workerdiagnostics.Manifest{}, time.Time{}, fmt.Errorf("read diagnostic archive: %w", err)
		}
		if len(order) == workerdiagnostics.DefaultMaxArtifacts+1 {
			return workerdiagnostics.Manifest{}, time.Time{}, errors.New("diagnostic archive contains too many entries")
		}
		if header.Typeflag != tar.TypeReg || header.Mode != 0o600 ||
			header.Size < 0 || header.Size > config.MaxDecompressedBytes || !safeArchivePath(header.Name) {
			return workerdiagnostics.Manifest{}, time.Time{}, fmt.Errorf("unsafe diagnostic archive entry %q", header.Name)
		}
		if _, exists := entries[header.Name]; exists {
			return workerdiagnostics.Manifest{}, time.Time{}, fmt.Errorf("duplicate diagnostic archive entry %q", header.Name)
		}
		entry, err := io.ReadAll(io.LimitReader(archive, header.Size+1))
		if err != nil {
			return workerdiagnostics.Manifest{}, time.Time{}, fmt.Errorf("read diagnostic archive entry %q: %w", header.Name, err)
		}
		if int64(len(entry)) != header.Size {
			return workerdiagnostics.Manifest{}, time.Time{}, fmt.Errorf("diagnostic archive entry %q has inconsistent size", header.Name)
		}
		entries[header.Name] = entry
		order = append(order, header.Name)
	}
	var trailing [1]byte
	count, trailingErr := limited.Read(trailing[:])
	if count != 0 || (trailingErr != nil && !errors.Is(trailingErr, io.EOF)) || compressed.Len() != 0 {
		return workerdiagnostics.Manifest{}, time.Time{}, errors.New("diagnostic archive has trailing compressed or decompressed content")
	}
	if limited.exceeded {
		return workerdiagnostics.Manifest{}, time.Time{}, errors.New("diagnostic archive exceeds decompressed byte limit")
	}
	manifestBytes, exists := entries["manifest.json"]
	if !exists || len(manifestBytes) == 0 || len(manifestBytes) > maxManifestBytes || len(order) == 0 || order[0] != "manifest.json" {
		return workerdiagnostics.Manifest{}, time.Time{}, errors.New("diagnostic archive has no bounded leading manifest")
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	var manifest workerdiagnostics.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return workerdiagnostics.Manifest{}, time.Time{}, fmt.Errorf("decode diagnostic manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return workerdiagnostics.Manifest{}, time.Time{}, errors.New("diagnostic manifest has trailing JSON")
	}
	if manifest.SchemaVersion != workerdiagnostics.SchemaVersion {
		return workerdiagnostics.Manifest{}, time.Time{}, fmt.Errorf("unsupported diagnostic schema version %d", manifest.SchemaVersion)
	}
	capturedAt, err := time.Parse(time.RFC3339Nano, manifest.CapturedAt)
	if err != nil || capturedAt.Location() != time.UTC {
		return workerdiagnostics.Manifest{}, time.Time{}, errors.New("diagnostic manifest captured_at must be UTC RFC3339")
	}
	if _, err := classifyBundleScope(config, manifest.Instance); err != nil {
		return workerdiagnostics.Manifest{}, time.Time{}, errors.New("diagnostic manifest is outside the configured repository or pool")
	}
	wantedPrefix := "runner-diagnostics-v1-" + manifest.Instance.Name + "-" +
		capturedAt.UTC().Format("20060102T150405.000000000Z") + "-"
	if !strings.HasPrefix(name, wantedPrefix) {
		return workerdiagnostics.Manifest{}, time.Time{}, errors.New("diagnostic filename does not bind the manifest identity and timestamp")
	}
	if len(entries) != len(manifest.Artifacts)+1 {
		return workerdiagnostics.Manifest{}, time.Time{}, errors.New("diagnostic archive entries do not match the manifest")
	}
	previous := ""
	for _, record := range manifest.Artifacts {
		if !safeArchivePath(record.Path) || record.Path == "manifest.json" || record.Path <= previous {
			return workerdiagnostics.Manifest{}, time.Time{}, errors.New("diagnostic manifest artifact paths are unsafe or unstable")
		}
		previous = record.Path
		entry, exists := entries[record.Path]
		if !exists || len(entry) != record.Bytes {
			return workerdiagnostics.Manifest{}, time.Time{}, fmt.Errorf("diagnostic artifact %q does not match its manifest size", record.Path)
		}
		digest := sha256.Sum256(entry)
		if record.SHA256 != hex.EncodeToString(digest[:]) {
			return workerdiagnostics.Manifest{}, time.Time{}, fmt.Errorf("diagnostic artifact %q does not match its manifest digest", record.Path)
		}
	}
	return manifest, capturedAt.UTC(), nil
}

type countingReader struct {
	reader    io.Reader
	remaining int64
	exceeded  bool
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	if r.remaining <= 0 {
		r.exceeded = true
		return 0, errors.New("decompressed diagnostic byte limit exceeded")
	}
	if int64(len(buffer)) > r.remaining+1 {
		buffer = buffer[:r.remaining+1]
	}
	count, err := r.reader.Read(buffer)
	r.remaining -= int64(count)
	if r.remaining < 0 {
		r.exceeded = true
	}
	return count, err
}

func safeArchivePath(value string) bool {
	return value != "" && value != "." && !path.IsAbs(value) && path.Clean(value) == value &&
		!strings.HasPrefix(value, "../") && !strings.Contains(value, `\`) &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func classifyBundleScope(config Config, instance workerdiagnostics.Instance) (bundleScope, error) {
	if !config.AllowsTrust(resolvedTrust(instance)) {
		return 0, errors.New("trust identity is outside the configured allowlist")
	}
	if instance.PoolName != instance.ScaleSet || !config.AllowsPool(instance.PoolName) {
		return 0, errors.New("pool identity is outside the configured allowlist")
	}
	if config.AllowsRepository(instance.Repository) && !strings.HasPrefix(instance.PoolID, "warm/") {
		return bundleScopeRepository, nil
	}
	if config.AllowsAccount(instance.Repository) && !strings.HasPrefix(instance.PoolID, "warm/") {
		return bundleScopeAccount, nil
	}
	if instance.Repository == "" && instance.PoolID == "warm/"+instance.PoolName {
		return bundleScopeUnassignedWarm, nil
	}
	return 0, errors.New("repository and pool identity do not form a reviewed diagnostic scope")
}

func objectKey(config Config, instance workerdiagnostics.Instance, capturedAt time.Time, digest string) (string, error) {
	scope, err := classifyBundleScope(config, instance)
	if err != nil {
		return "", err
	}
	common := []string{
		"trust", resolvedTrust(instance),
		"platform", config.Platform, config.Architecture,
		"pool", instance.PoolName,
		capturedAt.UTC().Format("2006/01/02"),
		"sha256", digest[:2], digest + ".tar.gz",
	}
	if scope == bundleScopeUnassignedWarm {
		return path.Join(append([]string{config.Prefix, "unassigned-warm"}, common...)...), nil
	}
	if scope == bundleScopeAccount {
		return path.Join(append([]string{config.Prefix, "account", instance.Repository}, common...)...), nil
	}
	owner, repository, _ := strings.Cut(instance.Repository, "/")
	return path.Join(append([]string{config.Prefix, "repository", owner, repository}, common...)...), nil
}

func resolvedTrust(instance workerdiagnostics.Instance) string {
	// Bundles captured before trust was added to schema v1 came exclusively
	// from trusted pools. Preserve their exportability while requiring every
	// newly captured release bundle to carry its explicit domain.
	if instance.Trust == "" {
		return "trusted"
	}
	return instance.Trust
}

func openSourceDirectory(directory string) (*os.File, uint32, uint32, error) {
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("resolve diagnostic spool: %w", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(directory) {
		return nil, 0, 0, errors.New("diagnostic spool must not traverse symlinks")
	}
	fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("open diagnostic spool: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, 0, 0, fmt.Errorf("stat diagnostic spool: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid == 0 || stat.Gid != uint32(os.Getegid()) ||
		stat.Mode&0o777 != 0o700 {
		_ = unix.Close(fd)
		return nil, 0, 0, errors.New("diagnostic spool ownership, group, type or mode is unsafe")
	}
	handle := os.NewFile(uintptr(fd), directory)
	if handle == nil {
		_ = unix.Close(fd)
		return nil, 0, 0, errors.New("open diagnostic spool: invalid file descriptor")
	}
	return handle, stat.Uid, stat.Gid, nil
}
