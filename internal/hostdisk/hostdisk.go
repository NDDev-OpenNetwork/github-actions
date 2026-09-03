// Package hostdisk computes the host-usable root free percent, excluding the
// Incus loop-backed thin-pool file that lives on the same filesystem.
//
// Placement already reads the thin pool. The OTel host filesystem series and
// raw statvfs count that file as ordinary used space, so compute_root_disk_low
// fired at 17–18% free of a 309 GiB root while pool data_percent was ~20%.
// Compute members publish the result from the pressure observer; the central
// fleet observer only sees gha-services.
package hostdisk

import (
	"math/bits"
	"path/filepath"
	"syscall"

	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
)

const mebibyte = 1024 * 1024

// ApplyUsableRootPercent rewrites snapshot.RootFilesystem.FreePercent so
// metrics and alerts describe remaining host space, not the loop reservation.
func ApplyUsableRootPercent(snapshot *hostprobe.Snapshot) {
	if snapshot == nil {
		return
	}
	total := snapshot.RootFilesystem.TotalMiB * mebibyte
	available := snapshot.RootFilesystem.AvailableMiB * mebibyte
	snapshot.RootFilesystem.FreePercent = UsableFreePercent(total, available, LoopBackingAllocatedOn("/"))
}

// UsableFreePercent is the free share of root after subtracting a loop file
// that is already reserved for Incus. A missing or oversize loop falls back
// to the raw statvfs ratio.
func UsableFreePercent(total, available, loopAllocated uint64) int {
	if loopAllocated == 0 || loopAllocated >= total {
		return percent(available, total)
	}
	usableTotal := total - loopAllocated
	if available >= usableTotal {
		return 100
	}
	return percent(available, usableTotal)
}

// Observe is the live usable free percent of root, excluding loop-backed
// Incus pool files on the same device. A stat error is returned so the
// publisher can fail closed instead of omitting the series.
func Observe(root string) (int, error) {
	if root == "" {
		root = "/"
	}
	var stats syscall.Statfs_t
	if err := syscall.Statfs(root, &stats); err != nil {
		return 0, err
	}
	total := uint64(stats.Blocks) * uint64(stats.Bsize)
	available := uint64(stats.Bavail) * uint64(stats.Bsize)
	return UsableFreePercent(total, available, LoopBackingAllocatedOn(root)), nil
}

// LoopBackingAllocatedOn sums allocated bytes of Incus pool images on the
// same device as root. Sparse holes are not counted (st_blocks).
func LoopBackingAllocatedOn(root string) uint64 {
	var rootStat syscall.Stat_t
	if syscall.Stat(root, &rootStat) != nil {
		return 0
	}
	matches, err := filepath.Glob(filepath.Join(root, "var", "lib", "incus", "disks", "*.img"))
	if err != nil {
		return 0
	}
	var allocated uint64
	rootDev := uint64(rootStat.Dev)
	for _, path := range matches {
		var st syscall.Stat_t
		if syscall.Stat(path, &st) != nil || uint64(st.Dev) != rootDev {
			continue
		}
		allocated += uint64(st.Blocks) * 512
	}
	return allocated
}

func percent(part, total uint64) int {
	if total == 0 {
		return 0
	}
	whole := part / total
	remainder := part % total
	high, low := bits.Mul64(remainder, 100)
	fraction, leftover := bits.Div64(high, low, total)
	if leftover > 0 {
		fraction++
	}
	return int(whole*100 + fraction)
}
