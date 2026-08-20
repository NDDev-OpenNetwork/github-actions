package pressurepublish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/pressuregate"
	"github.com/lxc/incus/v7/shared/api"
)

type Client interface {
	GetClusterMember(string) (*api.ClusterMember, string, error)
	UpdateClusterMember(string, api.ClusterMemberPut, string) error
}

type Options struct {
	MemberName       string
	StatePath        string
	Policy           pressuregate.Policy
	Sample           pressuregate.Sample
	Apply            bool
	ForceCloseReason string
	Now              time.Time
}

type Result struct {
	MemberName string             `json:"member_name"`
	State      pressuregate.State `json:"state"`
	Metadata   map[string]string  `json:"metadata"`
	Scheduler  string             `json:"scheduler_instance"`
	Changed    bool               `json:"changed"`
	Applied    bool               `json:"applied"`
}

func Reconcile(ctx context.Context, client Client, options Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if client == nil || options.MemberName == "" || !filepath.IsAbs(options.StatePath) {
		return Result{}, fmt.Errorf("pressure publisher requires a client, member name and absolute state path")
	}
	now := options.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	previous, err := loadState(options.StatePath)
	if err != nil {
		return Result{}, err
	}
	var state pressuregate.State
	if options.ForceCloseReason != "" {
		state, err = pressuregate.ForceClosed(previous, options.Sample, options.Policy, now, options.ForceCloseReason)
	} else {
		state, err = pressuregate.Evaluate(previous, options.Sample, options.Policy, now)
	}
	if err != nil {
		return Result{}, err
	}
	member, etag, err := client.GetClusterMember(options.MemberName)
	if err != nil {
		return Result{}, fmt.Errorf("read Incus cluster member %q: %w", options.MemberName, err)
	}
	if member.ServerName != options.MemberName {
		return Result{}, fmt.Errorf("Incus returned cluster member %q for %q", member.ServerName, options.MemberName)
	}
	writable := member.Writable()
	if writable.Config == nil {
		writable.Config = make(map[string]string)
	}
	scheduler := "manual"
	if state.State == pressuregate.StateOpen {
		scheduler = "all"
	}
	if current := writable.Config["scheduler.instance"]; current != "" && current != "all" && current != "manual" {
		return Result{}, fmt.Errorf("cluster member scheduler.instance=%q is outside pressure-gate ownership", current)
	}
	metadata := pressuregate.Metadata(state, options.Sample)
	lastPublished, publishedErr := time.Parse(time.RFC3339Nano, writable.Config[pressuregate.MetadataObservedAt])
	heartbeatDue := publishedErr != nil || now.Sub(lastPublished) >= time.Duration(options.Policy.HeartbeatSeconds)*time.Second
	stateChanged := writable.Config["scheduler.instance"] != scheduler ||
		writable.Config[pressuregate.MetadataSchema] != metadata[pressuregate.MetadataSchema] ||
		writable.Config[pressuregate.MetadataState] != metadata[pressuregate.MetadataState] ||
		writable.Config[pressuregate.MetadataReason] != metadata[pressuregate.MetadataReason]
	changed := stateChanged || heartbeatDue
	if changed {
		writable.Config["scheduler.instance"] = scheduler
		for key, value := range metadata {
			writable.Config[key] = value
		}
	}
	result := Result{MemberName: options.MemberName, State: state, Metadata: metadata, Scheduler: scheduler, Changed: changed}
	if !options.Apply {
		return result, nil
	}
	if changed {
		if err := client.UpdateClusterMember(options.MemberName, writable, etag); err != nil {
			return Result{}, fmt.Errorf("publish Incus pressure state for %q: %w", options.MemberName, err)
		}
	}
	if err := saveState(options.StatePath, state); err != nil {
		return Result{}, err
	}
	result.Applied = true
	return result, nil
}

func loadState(path string) (pressuregate.State, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return pressuregate.State{}, nil
	}
	if err != nil {
		return pressuregate.State{}, fmt.Errorf("inspect pressure state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return pressuregate.State{}, fmt.Errorf("pressure state must be a private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return pressuregate.State{}, fmt.Errorf("read pressure state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var state pressuregate.State
	if err := decoder.Decode(&state); err != nil {
		return pressuregate.State{}, fmt.Errorf("decode pressure state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return pressuregate.State{}, fmt.Errorf("pressure state has trailing data")
	}
	if err := state.Validate(); err != nil {
		return pressuregate.State{}, err
	}
	return state, nil
}

func saveState(path string, state pressuregate.State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create pressure state directory: %w", err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".pressure-state-*")
	if err != nil {
		return fmt.Errorf("create pressure state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(raw, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace pressure state: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
