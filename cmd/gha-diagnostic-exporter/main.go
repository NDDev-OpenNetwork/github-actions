package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticexport"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gha-diagnostic-exporter: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("gha-diagnostic-exporter", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "/run/credentials/gha-diagnostic-exporter.service/exporter-config", "strict exporter config credential")
	showVersion := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("positional arguments are not accepted")
	}
	if *showVersion {
		_, err := fmt.Fprintf(stdout, "gha-diagnostic-exporter %s (%s)\n", version, commit)
		return err
	}
	config, err := diagnosticexport.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	store, err := diagnosticexport.NewS3Store(config)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	budget := time.Duration(config.RunTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	exporter := diagnosticexport.Exporter{
		Config: config,
		Store:  store,
		State:  diagnosticexport.StateStore{Directory: config.StateDirectory},
	}
	summary, err := exporter.Run(ctx)
	if encodeErr := json.NewEncoder(stdout).Encode(summary); encodeErr != nil {
		return encodeErr
	}
	if err != nil {
		logger.Error("diagnostic export incomplete", "error", err)
		return err
	}
	for _, observed := range summary.ToolCacheEvents {
		logger.Info(
			"nddev tool cache event",
			"event_type", "nddev_tool_cache_event",
			"repository", observed.Repository,
			"runner", observed.Runner,
			"captured_at", observed.CapturedAt,
			"source", observed.Event.Source,
			"cache_result", observed.Event.CacheResult,
			"sha256", observed.Event.SHA256,
			"bytes", observed.Event.Bytes,
			"duration_ms", observed.Event.DurationMS,
		)
	}
	logger.Info(
		"diagnostic export complete",
		"source_bundles", summary.SourceBundles,
		"exported_bundles", summary.ExportedBundles,
		"pending_bundles", summary.PendingBundles,
		"tool_cache_events", summary.ToolCacheEventCount,
		"rejected_tool_cache_events", summary.RejectedToolCacheEventCount,
	)
	return nil
}
