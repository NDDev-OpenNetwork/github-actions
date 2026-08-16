package incusreconcile

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner is the narrow command boundary used by the reconciler. Keeping the
// boundary at the Incus CLI preserves local Unix-socket authentication while
// making every API exchange testable without a daemon.
type Runner interface {
	Run(context.Context, ...string) ([]byte, error)
}

type ExecRunner struct {
	Path string
}

func (r ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	path := r.Path
	if path == "" {
		path = "incus"
	}
	command := exec.CommandContext(ctx, path, args...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if message == "" {
			message = "no diagnostic output"
		}
		return nil, fmt.Errorf("incus command failed: %w: %s", err, message)
	}
	return stdout.Bytes(), nil
}
