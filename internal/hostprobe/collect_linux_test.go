package hostprobe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunnerProcessesExcludeIncusGuests(t *testing.T) {
	proc := t.TempDir()
	writeProcess := func(pid, command, cgroup string) {
		directory := filepath.Join(proc, pid)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "comm"), []byte(command+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "cgroup"), []byte(cgroup+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeProcess("100", "Runner.Listener", "0::/system.slice/actions.runner.legacy.service")
	writeProcess("101", "Runner.Worker", "0::/system.slice/actions.runner.legacy.service")
	writeProcess("200", "Runner.Listener", "0::/lxc.payload.gha-fleet_worker/system.slice/actions.runner.one-job.service")
	writeProcess("201", "Runner.Worker", "0::/lxc.payload.gha-fleet_worker/system.slice/actions.runner.one-job.service")
	writeProcess("300", "Runner.Listener", "0::/machine.slice/machine-qemu\\x2dworker.scope")

	listeners, workers := runnerProcesses(proc)
	if listeners != 1 || workers != 1 {
		t.Fatalf("host listeners/workers = %d/%d, want 1/1", listeners, workers)
	}
}

func TestUsableFreePercentExcludesLoopBackingFile(t *testing.T) {
	t.Parallel()
	// Live members: 309 GiB root, 73 GiB available, 200 GiB fully allocated
	// loop file. Raw statvfs is 23% free and trips the 20% gate; the usable
	// share of the remaining 109 GiB is 67%.
	const gib = 1024 * 1024 * 1024
	got := usableFreePercent(309*gib, 73*gib, 200*gib)
	if got != 67 {
		t.Fatalf("usable free = %d, want 67", got)
	}
	if raw := usableFreePercent(309*gib, 73*gib, 0); raw != 24 {
		t.Fatalf("raw free = %d, want 24", raw)
	}
	if got := usableFreePercent(309*gib, 73*gib, 400*gib); got != 24 {
		t.Fatalf("oversize loop must not invert the percentage: %d", got)
	}
	if got := usableFreePercent(100*gib, 100*gib, 40*gib); got != 100 {
		t.Fatalf("available covering usable total = %d, want 100", got)
	}
}
