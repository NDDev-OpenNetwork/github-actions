package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticexport"
	"github.com/NDDev-OpenNetwork/github-actions/internal/fleetobserve"
	"github.com/NDDev-OpenNetwork/github-actions/internal/fleettrace"
	providerconfig "github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/provider"
	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerretry"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queuephase"
	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
)

const (
	expectedListenAddress = "127.0.0.1:9464"
	sampleInterval        = 15 * time.Second
	sampleTimeout         = 10 * time.Second
	maxStaleness          = 45 * time.Second
	observerControllerID  = "nddev-loopback-observer"
	diagnosticExportState = "/var/lib/gha-diagnostic-exporter"
	pilotProfile          = "nddev-linux-standard"
)

var (
	version = "dev"
	commit  = "unknown"
)

type options struct {
	platformConfig string
	providerConfig string
	listen         string
	showVersion    bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	options, err := parseOptions(args, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "gha-fleet-observer: %v\n", err)
		return 2
	}
	if options.showVersion {
		_, _ = fmt.Fprintf(stdout, "gha-fleet-observer %s (%s)\n", version, commit)
		return 0
	}

	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	collector, err := buildCollector(options)
	if err != nil {
		logger.Error("initialize NDDev Drakkars observer", "error", err)
		return 1
	}
	if err := collector.Validate(); err != nil {
		logger.Error("validate NDDev Drakkars observer", "error", err)
		return 1
	}

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownTrace, err := fleettrace.Configure(rootContext, "gha-fleet-observer", version)
	if err != nil {
		logger.Error("configure NDDev Drakkars observer tracing", "error", err)
		return 1
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTrace(shutdownContext); err != nil {
			logger.Error("flush NDDev Drakkars observer tracing", "error", err)
		}
	}()
	providerConfiguration, err := providerconfig.NewConfig(options.providerConfig)
	if err != nil {
		logger.Error("load queue correlation source", "error", err)
		return 1
	}
	queueReader := queueintent.Reader{Path: providerConfiguration.QueueIntentFile}
	phaseEmitter := queuephase.New()
	initialQueue, err := queueReader.ReadActive(rootContext)
	if err != nil {
		logger.Error("seed queue phase tracing", "error", err)
		return 1
	}
	phaseEmitter.Observe(rootContext, initialQueue, time.Now().UTC())
	state := &fleetobserve.State{}
	state.Set(collect(rootContext, collector))

	handler := fleetobserve.Handler{State: state, MaxStaleness: maxStaleness}
	server := &http.Server{
		Addr:              options.listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	go sample(rootContext, collector, queueReader, phaseEmitter, state, logger)
	go func() {
		<-rootContext.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("NDDev Drakkars observer shutdown failed", "error", err)
		}
	}()

	logger.Info(
		"NDDev Drakkars observer starting",
		"listen_address", options.listen,
		"sample_interval", sampleInterval.String(),
		"max_staleness", maxStaleness.String(),
		"version", version,
		"commit", commit,
	)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("NDDev Drakkars observer stopped", "error", err)
		return 1
	}
	return 0
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	flags := flag.NewFlagSet("gha-fleet-observer", flag.ContinueOnError)
	flags.SetOutput(stderr)
	platformPath := flags.String("config", "/etc/gha-fleet/platform.yaml", "validated platform policy")
	providerPath := flags.String("provider-config", "/etc/garm/provider-incus.toml", "hardened Incus provider configuration")
	listen := flags.String("listen", expectedListenAddress, "loopback metrics and health address")
	showVersion := flags.Bool("version", false, "print version information and exit")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, fmt.Errorf("positional arguments are not accepted")
	}
	if *listen != expectedListenAddress {
		return options{}, fmt.Errorf("listen address must remain %s", expectedListenAddress)
	}
	return options{
		platformConfig: *platformPath,
		providerConfig: *providerPath,
		listen:         *listen,
		showVersion:    *showVersion,
	}, nil
}

func buildCollector(options options) (fleetobserve.Collector, error) {
	platform, err := config.Load(options.platformConfig)
	if err != nil {
		return fleetobserve.Collector{}, err
	}
	providerConfiguration, err := providerconfig.NewConfig(options.providerConfig)
	if err != nil {
		return fleetobserve.Collector{}, err
	}
	// The observer does not execute as the provider artifact, so its own linker
	// stamp must not impersonate that artifact. The root-owned provider config
	// carries the exact current identity, while NewIncusProvider still verifies
	// a real provider binary's independent stamp against the same pair.
	provider.Version = providerConfiguration.CurrentProviderIdentity.Version
	provider.Commit = providerConfiguration.CurrentProviderIdentity.Commit
	journalStore := providerjournal.Store{
		Path:     providerConfiguration.JournalFile,
		LockPath: providerConfiguration.JournalLockFile,
	}
	collector := fleetobserve.Collector{
		Config: platform,
		Host:   hostprobe.Collect,
		Journal: func(ctx context.Context) (providerjournal.Journal, error) {
			return journalStore.ReadOnly(ctx)
		},
		ProviderRetry: func(now time.Time) (providerretry.Snapshot, error) {
			return providerretry.Inspect("/var/lib/gha-fleet/create-retries.json", now)
		},
		Queue: func(ctx context.Context) (queueintent.Snapshot, error) {
			return (queueintent.Reader{Path: providerConfiguration.QueueIntentFile}).ReadActive(ctx)
		},
		Instances: func(ctx context.Context) ([]string, error) {
			incusProvider, err := provider.NewIncusProvider(options.providerConfig, observerControllerID)
			if err != nil {
				return nil, err
			}
			probe, err := incusProvider.Probe(ctx, pilotProfile)
			if err != nil {
				return nil, err
			}
			return probe.VisibleInstances, nil
		},
		Diagnostics: func(now time.Time) (workerdiagnostics.SpoolStats, error) {
			return workerdiagnostics.Inspect(providerConfiguration.DiagnosticsDirectory, now)
		},
		Service:            serviceState,
		DiagnosticMaxBytes: providerConfiguration.DiagnosticsMaxTotalBytes,
		CreatedVisibility:  fleetobserve.NewCreatedVisibilityTracker(),
	}
	if platformOwnsDiagnosticExporter(platform) {
		collector.Export = func() (diagnosticexport.Status, error) {
			return diagnosticexport.ReadStatus(diagnosticExportState)
		}
	}
	return collector, nil
}

func platformOwnsDiagnosticExporter(platform config.Config) bool {
	// The non-member services host owns the queue, provider process, local
	// diagnostic WAL and central exporter. Cluster members are capacity only.
	return !platform.Incus.Cluster.Enabled
}

func collect(parent context.Context, collector fleetobserve.Collector) fleetobserve.Snapshot {
	ctx, cancel := context.WithTimeout(parent, sampleTimeout)
	defer cancel()
	return collector.Collect(ctx)
}

func sample(ctx context.Context, collector fleetobserve.Collector, queueReader queueintent.Reader, phaseEmitter *queuephase.Emitter, state *fleetobserve.State, logger *slog.Logger) {
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshot := collect(ctx, collector)
			state.Set(snapshot)
			queueSnapshot, queueErr := queueReader.ReadActive(ctx)
			if queueErr != nil {
				logger.Warn("queue phase tracing read failed", "error", queueErr)
			} else {
				phaseEmitter.Observe(ctx, queueSnapshot, time.Now().UTC())
			}
			if !snapshot.Healthy {
				// Every input to snapshot.Healthy, because printing a subset
				// produced a page whose own diagnostic said nothing was wrong.
				// On 2026-08-28 at 23:24 this line reported collection_errors
				// 0, orphan_instances 0, missing_instances 0 and an empty error
				// list while the platform was unhealthy; the cause was
				// queue.uncovered_running = 1, which it did not print. An
				// inactive service was equally invisible.
				inactive := make([]string, 0, len(snapshot.Services))
				for _, service := range snapshot.Services {
					if !service.Active {
						inactive = append(inactive, service.Name+"="+service.State)
					}
				}
				logger.Warn(
					"NDDev Drakkars observer sample is unhealthy",
					"collection_errors", len(snapshot.CollectionErrors),
					"orphan_instances", snapshot.Incus.OrphanInstances,
					"missing_instances", snapshot.Incus.MissingInstances,
					"uncovered_running", snapshot.Queue.UncoveredRunning,
					"inactive_services", inactive,
					"errors", snapshot.CollectionErrors,
				)
			}
		}
	}
}

func serviceState(ctx context.Context, name string) (string, error) {
	if !serviceNameAllowed(name) {
		return "", fmt.Errorf("service %q is outside the fixed observer inventory", name)
	}
	output, err := exec.CommandContext(ctx, "systemctl", "is-active", systemdUnitName(name)).CombinedOutput()
	state := strings.TrimSpace(string(output))
	switch state {
	case "active", "activating", "deactivating", "failed", "inactive", "reloading":
		return state, nil
	default:
		if err != nil {
			return "", fmt.Errorf("query service %s: %w", name, err)
		}
		return "", fmt.Errorf("service %s returned unknown state %q", name, state)
	}
}

func serviceNameAllowed(name string) bool {
	for _, expected := range fleetobserve.ServiceNames() {
		if name == expected {
			return true
		}
	}
	return name == "gha-cache-broker" || name == "gha-services-rustfs-route.timer"
}

func systemdUnitName(name string) string {
	if strings.HasSuffix(name, ".service") || strings.HasSuffix(name, ".timer") {
		return name
	}
	return name + ".service"
}
