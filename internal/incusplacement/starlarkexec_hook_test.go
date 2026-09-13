package incusplacement

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestStarlarkExecutionHarness runs the nested module. `go test ./...` from
// the repository root skips nested modules, and that skip is what would let a
// packing regression land without executing Starlark.
func TestStarlarkExecutionHarness(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(file), "starlarkexec")
	args := []string{"test", "-count=1", "."}
	if raceEnabled {
		args = []string{"test", "-race", "-count=1", "."}
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("starlarkexec tests: %v\n%s", err, output)
	}
}

func TestStarlarkExecutionHarnessVet(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(file), "starlarkexec")
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "vet", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("starlarkexec vet: %v\n%s", err, output)
	}
}
