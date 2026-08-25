package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/schedulerrecovery"
)

type config struct {
	StateFile string `json:"state_file"`
	LockFile  string `json:"lock_file"`
	Policy    struct {
		MinimumStuckAgeSeconds int `json:"minimum_stuck_age_seconds"`
		MinimumUptimeSeconds   int `json:"minimum_uptime_seconds"`
		CooldownSeconds        int `json:"cooldown_seconds"`
		HeartbeatStaleSeconds  int `json:"heartbeat_stale_seconds"`
	} `json:"policy"`
	Observation struct {
		Argv           []string `json:"argv"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	} `json:"observation"`
	Recovery struct {
		Checkpoint     []string `json:"checkpoint"`
		Restart        []string `json:"restart"`
		Progress       []string `json:"progress"`
		TimeoutSeconds int      `json:"timeout_seconds"`
	} `json:"recovery"`
}

type jsonEvents struct{ encoder *json.Encoder }

func (sink jsonEvents) Emit(_ context.Context, event schedulerrecovery.Event) error {
	return sink.encoder.Encode(event)
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(arguments []string) int {
	flags := flag.NewFlagSet("gha-scheduler-recovery", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "path to scheduler recovery JSON config")
	progress := flags.String("progress", "", "dispatcher progress identity for heartbeat mode")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *configPath == "" || flags.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: gha-scheduler-recovery --config PATH [--progress ID] <heartbeat|check|apply>")
		return 2
	}
	configuration, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	store := schedulerrecovery.FileStore{Path: configuration.StateFile, LockPath: configuration.LockFile}
	ctx := context.Background()
	switch flags.Arg(0) {
	case "heartbeat":
		if *progress == "" {
			fmt.Fprintln(os.Stderr, "heartbeat requires --progress")
			return 2
		}
		if err := store.RecordHeartbeat(ctx, schedulerrecovery.Heartbeat{At: time.Now().UTC(), Progress: *progress}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case "check":
		observer := commandObserver(configuration)
		observation, err := observer.Observe(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		heartbeat, err := store.ReadHeartbeat(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		observation.HeartbeatAt = heartbeat.At
		if err := json.NewEncoder(os.Stdout).Encode(schedulerrecovery.Evaluate(policy(configuration), observation)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	case "apply":
		controller := schedulerrecovery.Controller{
			Policy: policy(configuration), Observer: commandObserver(configuration), Heartbeat: store,
			Attempts: store, Executor: schedulerrecovery.CommandExecutor{Config: schedulerrecovery.CommandConfig{
				Checkpoint: configuration.Recovery.Checkpoint, Restart: configuration.Recovery.Restart,
				Progress: configuration.Recovery.Progress, Timeout: time.Duration(configuration.Recovery.TimeoutSeconds) * time.Second,
			}}, Events: jsonEvents{encoder: json.NewEncoder(os.Stdout)}, Now: time.Now,
		}
		_, _, err := controller.Tick(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(os.Stderr, "mode must be heartbeat, check or apply")
		return 2
	}
}

func loadConfig(path string) (config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config{}, fmt.Errorf("read scheduler recovery config: %w", err)
	}
	var configuration config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return config{}, fmt.Errorf("decode scheduler recovery config: %w", err)
	}
	if !filepath.IsAbs(configuration.StateFile) || !filepath.IsAbs(configuration.LockFile) ||
		configuration.Policy.MinimumStuckAgeSeconds <= 0 || configuration.Policy.MinimumUptimeSeconds <= 0 ||
		configuration.Policy.CooldownSeconds <= 0 || configuration.Policy.HeartbeatStaleSeconds <= 0 ||
		configuration.Observation.TimeoutSeconds <= 0 || configuration.Recovery.TimeoutSeconds <= 0 {
		return config{}, fmt.Errorf("scheduler recovery config is incomplete")
	}
	if err := commandObserver(configuration).Validate(); err != nil {
		return config{}, err
	}
	return configuration, (schedulerrecovery.CommandExecutor{Config: schedulerrecovery.CommandConfig{
		Checkpoint: configuration.Recovery.Checkpoint, Restart: configuration.Recovery.Restart,
		Progress: configuration.Recovery.Progress, Timeout: time.Duration(configuration.Recovery.TimeoutSeconds) * time.Second,
	}}).Validate()
}

func policy(configuration config) schedulerrecovery.Policy {
	return schedulerrecovery.Policy{
		MinimumStuckAge: time.Duration(configuration.Policy.MinimumStuckAgeSeconds) * time.Second,
		MinimumUptime:   time.Duration(configuration.Policy.MinimumUptimeSeconds) * time.Second,
		Cooldown:        time.Duration(configuration.Policy.CooldownSeconds) * time.Second,
		HeartbeatStale:  time.Duration(configuration.Policy.HeartbeatStaleSeconds) * time.Second,
	}
}

func commandObserver(configuration config) schedulerrecovery.CommandObserver {
	return schedulerrecovery.CommandObserver{Argv: configuration.Observation.Argv, Timeout: time.Duration(configuration.Observation.TimeoutSeconds) * time.Second}
}
