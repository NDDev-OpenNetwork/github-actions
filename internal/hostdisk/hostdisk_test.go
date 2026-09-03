package hostdisk

import "testing"

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
