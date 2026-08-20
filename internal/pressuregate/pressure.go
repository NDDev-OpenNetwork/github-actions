// Package pressuregate turns local Linux PSI and OOM observations into a
// bounded, hysteretic Incus cluster-member admission signal.
package pressuregate

import (
	"fmt"
	"strconv"
	"time"
)

const (
	SchemaVersion = 1
	StateOpen     = "open"
	StateClosed   = "closed"

	MetadataSchema     = "user.gha_pressure.schema"
	MetadataState      = "user.gha_pressure.state"
	MetadataReason     = "user.gha_pressure.reason"
	MetadataObservedAt = "user.gha_pressure.observed_at"
	MetadataStateSince = "user.gha_pressure.state_since"
	MetadataCPUSome    = "user.gha_pressure.cpu_some_avg10"
	MetadataMemoryFull = "user.gha_pressure.memory_full_avg10"
	MetadataIOFull     = "user.gha_pressure.io_full_avg10"
	MetadataOOMKills   = "user.gha_pressure.oom_kills_total"
)

type Policy struct {
	Required              bool    `json:"required" yaml:"required"`
	StaleAfterSeconds     int     `json:"stale_after_seconds" yaml:"stale_after_seconds"`
	HeartbeatSeconds      int     `json:"heartbeat_seconds" yaml:"heartbeat_seconds"`
	MinimumClosedSeconds  int     `json:"minimum_closed_seconds" yaml:"minimum_closed_seconds"`
	CPUSomeClose          float64 `json:"cpu_some_close" yaml:"cpu_some_close"`
	CPUSomeReopen         float64 `json:"cpu_some_reopen" yaml:"cpu_some_reopen"`
	MemoryFullClose       float64 `json:"memory_full_close" yaml:"memory_full_close"`
	MemoryFullReopen      float64 `json:"memory_full_reopen" yaml:"memory_full_reopen"`
	IOFullClose           float64 `json:"io_full_close" yaml:"io_full_close"`
	IOFullReopen          float64 `json:"io_full_reopen" yaml:"io_full_reopen"`
	MaximumRecentOOMKills uint64  `json:"maximum_recent_oom_kills" yaml:"maximum_recent_oom_kills"`
}

func (p Policy) Validate() error {
	if !p.Required {
		if p != (Policy{}) {
			return fmt.Errorf("disabled pressure admission must not carry thresholds")
		}
		return nil
	}
	if p.StaleAfterSeconds < 15 || p.StaleAfterSeconds > 300 {
		return fmt.Errorf("stale_after_seconds must be in 15..300")
	}
	if p.HeartbeatSeconds < 10 || p.HeartbeatSeconds >= p.StaleAfterSeconds {
		return fmt.Errorf("heartbeat_seconds must be at least 10 and below stale_after_seconds")
	}
	if p.MinimumClosedSeconds < 15 || p.MinimumClosedSeconds > 600 {
		return fmt.Errorf("minimum_closed_seconds must be in 15..600")
	}
	if p.MaximumRecentOOMKills > 10 {
		return fmt.Errorf("maximum_recent_oom_kills must be in 0..10")
	}
	for name, pair := range map[string][2]float64{
		"cpu_some":    {p.CPUSomeReopen, p.CPUSomeClose},
		"memory_full": {p.MemoryFullReopen, p.MemoryFullClose},
		"io_full":     {p.IOFullReopen, p.IOFullClose},
	} {
		if pair[0] < 0 || pair[1] <= pair[0] || pair[1] > 100 {
			return fmt.Errorf("%s thresholds must satisfy 0 <= reopen < close <= 100", name)
		}
	}
	return nil
}

type Sample struct {
	ObservedAt      time.Time `json:"observed_at"`
	CPUSomeAvg10    float64   `json:"cpu_some_avg10"`
	MemoryFullAvg10 float64   `json:"memory_full_avg10"`
	IOFullAvg10     float64   `json:"io_full_avg10"`
	OOMKillsTotal   uint64    `json:"oom_kills_total"`
}

func (s Sample) Validate(now time.Time, staleAfter time.Duration) error {
	if s.ObservedAt.IsZero() || s.ObservedAt.After(now.Add(time.Second)) {
		return fmt.Errorf("pressure sample timestamp is missing or in the future")
	}
	if now.Sub(s.ObservedAt) > staleAfter {
		return fmt.Errorf("pressure sample is stale")
	}
	for name, value := range map[string]float64{
		"cpu_some_avg10":    s.CPUSomeAvg10,
		"memory_full_avg10": s.MemoryFullAvg10,
		"io_full_avg10":     s.IOFullAvg10,
	} {
		if value < 0 || value > 100 {
			return fmt.Errorf("%s must be in 0..100", name)
		}
	}
	return nil
}

type State struct {
	SchemaVersion int       `json:"schema_version"`
	State         string    `json:"state"`
	Reason        string    `json:"reason"`
	StateSince    time.Time `json:"state_since"`
	ObservedAt    time.Time `json:"observed_at"`
	OOMKillsTotal uint64    `json:"oom_kills_total"`
}

func (s State) Validate() error {
	if s.SchemaVersion != SchemaVersion || (s.State != StateOpen && s.State != StateClosed) {
		return fmt.Errorf("pressure state schema or state is invalid")
	}
	if s.Reason == "" || s.StateSince.IsZero() || s.ObservedAt.IsZero() || s.StateSince.After(s.ObservedAt.Add(time.Second)) {
		return fmt.Errorf("pressure state timestamps or reason are invalid")
	}
	return nil
}

func Evaluate(previous State, sample Sample, policy Policy, now time.Time) (State, error) {
	if err := policy.Validate(); err != nil {
		return State{}, err
	}
	if err := sample.Validate(now, time.Duration(policy.StaleAfterSeconds)*time.Second); err != nil {
		return State{}, err
	}
	recentOOMKills := uint64(0)
	previousValid := previous.State == StateOpen || previous.State == StateClosed
	if previousValid && sample.OOMKillsTotal >= previous.OOMKillsTotal {
		recentOOMKills = sample.OOMKillsTotal - previous.OOMKillsTotal
	}
	if previous.State != StateOpen && previous.State != StateClosed {
		previous = State{SchemaVersion: SchemaVersion, State: StateClosed, Reason: "initializing", StateSince: now, OOMKillsTotal: sample.OOMKillsTotal}
	}
	reason := closeReason(sample, recentOOMKills, policy)
	if reason != "" {
		if previous.State != StateClosed {
			previous.StateSince = now
		}
		previous.SchemaVersion = SchemaVersion
		previous.State = StateClosed
		previous.Reason = reason
		previous.ObservedAt = sample.ObservedAt
		previous.OOMKillsTotal = sample.OOMKillsTotal
		return previous, nil
	}
	if previous.State == StateClosed {
		if !belowReopen(sample, policy) {
			previous.Reason = "reopen-hysteresis"
			previous.ObservedAt = sample.ObservedAt
			previous.OOMKillsTotal = sample.OOMKillsTotal
			return previous, nil
		}
		if now.Sub(previous.StateSince) < time.Duration(policy.MinimumClosedSeconds)*time.Second {
			previous.Reason = "minimum-closed-period"
			previous.ObservedAt = sample.ObservedAt
			previous.OOMKillsTotal = sample.OOMKillsTotal
			return previous, nil
		}
		previous.State = StateOpen
		previous.StateSince = now
	}
	previous.SchemaVersion = SchemaVersion
	previous.Reason = "healthy"
	previous.ObservedAt = sample.ObservedAt
	previous.OOMKillsTotal = sample.OOMKillsTotal
	return previous, nil
}

func ForceClosed(previous State, sample Sample, policy Policy, now time.Time, reason string) (State, error) {
	if err := policy.Validate(); err != nil {
		return State{}, err
	}
	if err := sample.Validate(now, time.Duration(policy.StaleAfterSeconds)*time.Second); err != nil {
		return State{}, err
	}
	if reason == "" || len(reason) > 64 {
		return State{}, fmt.Errorf("forced-close reason is invalid")
	}
	if previous.State != StateClosed {
		previous.StateSince = now
	}
	previous.SchemaVersion = SchemaVersion
	previous.State = StateClosed
	previous.Reason = reason
	previous.ObservedAt = sample.ObservedAt
	previous.OOMKillsTotal = sample.OOMKillsTotal
	return previous, nil
}

func closeReason(sample Sample, recentOOMKills uint64, policy Policy) string {
	switch {
	case recentOOMKills > policy.MaximumRecentOOMKills:
		return "recent-oom"
	case sample.MemoryFullAvg10 >= policy.MemoryFullClose:
		return "memory-pressure"
	case sample.CPUSomeAvg10 >= policy.CPUSomeClose:
		return "cpu-pressure"
	case sample.IOFullAvg10 >= policy.IOFullClose:
		return "io-pressure"
	default:
		return ""
	}
}

func belowReopen(sample Sample, policy Policy) bool {
	return sample.CPUSomeAvg10 <= policy.CPUSomeReopen &&
		sample.MemoryFullAvg10 <= policy.MemoryFullReopen &&
		sample.IOFullAvg10 <= policy.IOFullReopen
}

func Metadata(state State, sample Sample) map[string]string {
	return map[string]string{
		MetadataSchema: strconv.Itoa(SchemaVersion), MetadataState: state.State,
		MetadataReason: state.Reason, MetadataObservedAt: sample.ObservedAt.UTC().Format(time.RFC3339Nano),
		MetadataStateSince: state.StateSince.UTC().Format(time.RFC3339Nano),
		MetadataCPUSome:    strconv.FormatFloat(sample.CPUSomeAvg10, 'f', -1, 64),
		MetadataMemoryFull: strconv.FormatFloat(sample.MemoryFullAvg10, 'f', -1, 64),
		MetadataIOFull:     strconv.FormatFloat(sample.IOFullAvg10, 'f', -1, 64),
		MetadataOOMKills:   strconv.FormatUint(sample.OOMKillsTotal, 10),
	}
}

func ParseMetadata(values map[string]string, policy Policy, now time.Time) (State, Sample, error) {
	if values[MetadataSchema] != strconv.Itoa(SchemaVersion) {
		return State{}, Sample{}, fmt.Errorf("pressure metadata schema is missing or unsupported")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, values[MetadataObservedAt])
	if err != nil {
		return State{}, Sample{}, fmt.Errorf("parse pressure observed_at: %w", err)
	}
	stateSince, err := time.Parse(time.RFC3339Nano, values[MetadataStateSince])
	if err != nil {
		return State{}, Sample{}, fmt.Errorf("parse pressure state_since: %w", err)
	}
	parseFloat := func(key string) (float64, error) {
		value, parseErr := strconv.ParseFloat(values[key], 64)
		if parseErr != nil {
			return 0, fmt.Errorf("parse %s: %w", key, parseErr)
		}
		return value, nil
	}
	cpu, err := parseFloat(MetadataCPUSome)
	if err != nil {
		return State{}, Sample{}, err
	}
	memory, err := parseFloat(MetadataMemoryFull)
	if err != nil {
		return State{}, Sample{}, err
	}
	ioFull, err := parseFloat(MetadataIOFull)
	if err != nil {
		return State{}, Sample{}, err
	}
	oom, err := strconv.ParseUint(values[MetadataOOMKills], 10, 64)
	if err != nil {
		return State{}, Sample{}, fmt.Errorf("parse %s: %w", MetadataOOMKills, err)
	}
	sample := Sample{ObservedAt: observedAt, CPUSomeAvg10: cpu, MemoryFullAvg10: memory, IOFullAvg10: ioFull, OOMKillsTotal: oom}
	if err := sample.Validate(now, time.Duration(policy.StaleAfterSeconds)*time.Second); err != nil {
		return State{}, Sample{}, err
	}
	state := State{SchemaVersion: SchemaVersion, State: values[MetadataState], Reason: values[MetadataReason], StateSince: stateSince, ObservedAt: observedAt, OOMKillsTotal: oom}
	if err := state.Validate(); err != nil {
		return State{}, Sample{}, err
	}
	return state, sample, nil
}
