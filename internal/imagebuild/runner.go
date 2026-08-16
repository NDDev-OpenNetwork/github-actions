package imagebuild

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type CommandRunner interface {
	Run(context.Context, ...string) ([]byte, error)
}

type ExecRunner struct{ Path string }

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
		diagnostic := strings.TrimSpace(stderr.String())
		if diagnostic == "" {
			diagnostic = strings.TrimSpace(stdout.String())
		}
		if diagnostic == "" {
			diagnostic = "no diagnostic output"
		}
		return nil, fmt.Errorf("incus command failed: %w: %s", err, diagnostic)
	}
	return stdout.Bytes(), nil
}
