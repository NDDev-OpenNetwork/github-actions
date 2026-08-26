package vanishedjob

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

type Mode string

const (
	ModeObserve   Mode = "observe"
	ModeFullRerun Mode = "force-cancel-full-rerun"
)

type Policy struct {
	SchemaVersion             int             `json:"schema_version"`
	MissingRunnerGraceSeconds int             `json:"missing_runner_grace_seconds"`
	ScaleSets                 map[string]Mode `json:"scale_sets"`
}

func DecodePolicy(reader io.Reader) (Policy, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return Policy{}, fmt.Errorf("read vanished-runner recovery policy: invalid bounded content")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("decode vanished-runner recovery policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Policy{}, fmt.Errorf("vanished-runner recovery policy has trailing content")
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func (policy Policy) Validate() error {
	if policy.SchemaVersion != 1 || policy.MissingRunnerGraceSeconds < 60 || policy.MissingRunnerGraceSeconds > 3600 || len(policy.ScaleSets) == 0 {
		return fmt.Errorf("vanished-runner recovery policy identity or grace is invalid")
	}
	for scaleSet, mode := range policy.ScaleSets {
		if scaleSet == "" || (mode != ModeObserve && mode != ModeFullRerun) {
			return fmt.Errorf("vanished-runner recovery policy for scale set %q is invalid", scaleSet)
		}
	}
	return nil
}

func (policy Policy) Grace() time.Duration {
	return time.Duration(policy.MissingRunnerGraceSeconds) * time.Second
}
