package workerdiagnostics

import (
	"os"
	"path/filepath"
	"time"
)

type SpoolStats struct {
	Bundles          int   `json:"bundles"`
	Bytes            int64 `json:"bytes"`
	OldestAgeSeconds int64 `json:"oldest_age_seconds"`
	NewestAgeSeconds int64 `json:"newest_age_seconds"`
}

// Inspect returns aggregate metadata for provider-owned diagnostic bundles.
// It uses the same private-directory and filename boundary as retention GC and
// never opens bundle contents or follows symlinks.
func Inspect(directory string, now time.Time) (SpoolStats, error) {
	if err := validatePrivateDirectory(directory); err != nil {
		return SpoolStats{}, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return SpoolStats{}, err
	}
	stats := SpoolStats{}
	var oldest time.Time
	var newest time.Time
	for _, entry := range entries {
		if !bundleNamePattern.MatchString(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return SpoolStats{}, err
		}
		if !info.Mode().IsRegular() || filepath.Base(entry.Name()) != entry.Name() {
			continue
		}
		stats.Bundles++
		stats.Bytes += info.Size()
		if oldest.IsZero() || info.ModTime().Before(oldest) {
			oldest = info.ModTime()
		}
		if newest.IsZero() || info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	now = now.UTC()
	if !oldest.IsZero() && now.After(oldest) {
		stats.OldestAgeSeconds = int64(now.Sub(oldest).Seconds())
	}
	if !newest.IsZero() && now.After(newest) {
		stats.NewestAgeSeconds = int64(now.Sub(newest).Seconds())
	}
	return stats, nil
}
