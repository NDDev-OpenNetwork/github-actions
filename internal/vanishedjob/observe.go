package vanishedjob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"
)

const maxObservationBytes = 4 << 20

type Observation struct {
	Jobs []Job `json:"jobs"`
}

type Observer interface {
	Observe(context.Context) (Observation, error)
}

type CommandObserver struct {
	Argv    []string
	Timeout time.Duration
}

func (observer CommandObserver) Observe(ctx context.Context) (Observation, error) {
	if len(observer.Argv) == 0 || !filepath.IsAbs(observer.Argv[0]) || observer.Timeout <= 0 || observer.Timeout > 5*time.Minute {
		return Observation{}, fmt.Errorf("vanished-runner observer command is invalid")
	}
	commandCtx, cancel := context.WithTimeout(ctx, observer.Timeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, observer.Argv[0], observer.Argv[1:]...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &boundedBuffer{buffer: &stdout, limit: maxObservationBytes}
	command.Stderr = &boundedBuffer{buffer: &stderr, limit: 64 << 10}
	if err := command.Run(); err != nil {
		return Observation{}, fmt.Errorf("observe vanished-runner jobs: %w: %s", err, stderr.String())
	}
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	var observation Observation
	if err := decoder.Decode(&observation); err != nil {
		return Observation{}, fmt.Errorf("decode vanished-runner observation: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Observation{}, fmt.Errorf("vanished-runner observation has trailing content")
	}
	seen := make(map[string]struct{}, len(observation.Jobs))
	for _, job := range observation.Jobs {
		key := fmt.Sprintf("%s/%d/%d", job.Repository, job.RunID, job.JobID)
		if _, exists := seen[key]; exists {
			return Observation{}, fmt.Errorf("vanished-runner observation contains duplicate job %s", key)
		}
		seen[key] = struct{}{}
	}
	return observation, nil
}

type boundedBuffer struct {
	buffer *bytes.Buffer
	limit  int
}

func (writer *boundedBuffer) Write(value []byte) (int, error) {
	if writer.buffer.Len()+len(value) > writer.limit {
		return 0, fmt.Errorf("command output exceeds %d bytes", writer.limit)
	}
	return writer.buffer.Write(value)
}
