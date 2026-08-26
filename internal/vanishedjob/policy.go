package vanishedjob

import (
	"encoding/json"
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
	decoder := json.NewDecoder(io.LimitReader(reader, 64*1024+1))
	decoder.DisallowUnknownFields()
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("decode vanished-runner recovery policy: %w", err)
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
