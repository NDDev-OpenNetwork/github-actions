package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticstore"
	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticstoreobserve"
	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
)

const listenAddress = "127.0.0.1:9470"

var version = "dev"
var commit = "unknown"

func main() {
	configPath := flag.String("config", diagnosticstore.DefaultConfigPath, "diagnostic storage configuration")
	credentialDirectory := flag.String("credential-directory", "", "systemd credential directory override")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "positional arguments are not accepted")
		os.Exit(2)
	}
	if *showVersion {
		fmt.Printf("gha-diagnostic-storage-observer %s (%s)\n", version, commit)
		return
	}
	config, err := diagnosticstore.Load(*configPath)
	if err != nil {
		slog.Error("load diagnostic storage observer config", "error", err)
		os.Exit(1)
	}
	if *credentialDirectory != "" {
		config, err = diagnosticstore.WithCredentialDirectory(config, *credentialDirectory)
		if err != nil {
			slog.Error("bind diagnostic storage credentials", "error", err)
			os.Exit(1)
		}
	}
	requester, err := rustfscache.NewHTTPRequester(rustfscache.Config{Endpoint: config.Endpoint, Region: config.Region, CAFile: config.CAFile})
	if err != nil {
		slog.Error("initialize RustFS client", "error", err)
		os.Exit(1)
	}
	collect := func(ctx context.Context) (diagnosticstore.Result, error) {
		return (diagnosticstore.Runner{Requester: requester}).Run(ctx, config, false)
	}
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	state := &diagnosticstoreobserve.State{}
	sample := func() {
		ctx, cancel := context.WithTimeout(root, 20*time.Second)
		defer cancel()
		now := time.Now().UTC()
		state.Sample(ctx, collect, now)
		snapshot := state.Snapshot()
		if snapshot.Error != "" {
			slog.Warn("diagnostic storage capacity sample failed", "error", snapshot.Error)
		} else if !diagnosticstoreobserve.Healthy(snapshot, now) {
			slog.Warn("diagnostic storage capacity sample is unhealthy",
				"state", snapshot.Result.StateAfter, "headroom_state", snapshot.Result.HeadroomState)
		}
	}
	sample()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-root.Done():
				return
			case <-ticker.C:
				sample()
			}
		}
	}()
	server := &http.Server{Addr: listenAddress, Handler: diagnosticstoreobserve.Handler{State: state},
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-root.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("diagnostic storage observer stopped", "error", err)
		os.Exit(1)
	}
}
