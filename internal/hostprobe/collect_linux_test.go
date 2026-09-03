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
