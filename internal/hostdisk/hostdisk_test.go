package hostdisk

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUsableFreePercentExcludesLoopBackingFile(t *testing.T) {
	t.Parallel()
	const gib = 1024 * 1024 * 1024
	got := UsableFreePercent(309*gib, 73*gib, 200*gib)
	if got != 67 {
		t.Fatalf("usable free = %d, want 67", got)
	}
	if raw := UsableFreePercent(309*gib, 73*gib, 0); raw != 24 {
		t.Fatalf("raw free = %d, want 24", raw)
	}
	if got := UsableFreePercent(309*gib, 73*gib, 400*gib); got != 24 {
		t.Fatalf("oversize loop must not invert the percentage: %d", got)
	}
	if got := UsableFreePercent(100*gib, 100*gib, 40*gib); got != 100 {
		t.Fatalf("available covering usable total = %d, want 100", got)
	}
}

func TestLoopBackingAllocatedOnCountsWrittenBlocks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "var", "lib", "incus", "disks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "gha-lvm.img")
	if err := os.WriteFile(path, make([]byte, 1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoopBackingAllocatedOn(root)
	if got < 1024*1024 {
		t.Fatalf("allocated %d, want at least 1MiB of written blocks", got)
	}
}

func TestObserveReturnsErrorForMissingRoot(t *testing.T) {
	if _, err := Observe(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing root must not look like a successful observation")
	}
}

func TestObserveWithoutLoopMatchesRawStatfsRatio(t *testing.T) {
	root := t.TempDir()
	got, err := Observe(root)
	if err != nil {
		t.Fatal(err)
	}
	if got < 0 || got > 100 {
		t.Fatalf("usable free percent %d is not a percentage", got)
	}
}
