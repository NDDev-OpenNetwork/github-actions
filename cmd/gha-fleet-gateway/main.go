package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/workergateway"
)

const (
	defaultCertificateFile = "/etc/gha-fleet/pki/worker-gateway.crt"
	defaultPrivateKeyFile  = "/etc/gha-fleet/pki/worker-gateway.key"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	certificateFile := flag.String("tls-cert", defaultCertificateFile, "worker gateway TLS certificate")
	privateKeyFile := flag.String("tls-key", defaultPrivateKeyFile, "worker gateway TLS private key")
	listenAddress := flag.String(
		"listen", workergateway.ExpectedListenAddress,
		"address the workers reach this gateway on, as a literal IP and port",
	)
	upstreamURL := flag.String(
		"upstream", workergateway.ExpectedUpstreamURL,
		"GARM origin this gateway forwards to, as a literal IP and port",
	)
	showVersion := flag.Bool("version", false, "print version information and exit")
	flag.Parse()
	if *showVersion {
		_, _ = fmt.Printf("gha-fleet-gateway %s (%s)\n", version, commit)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := workergateway.ValidateListenAddress(*listenAddress); err != nil {
		logger.Error("invalid listen address", "address", *listenAddress, "error", err)
		os.Exit(1)
	}

	upstream, err := url.Parse(*upstreamURL)
	if err != nil {
		logger.Error("invalid upstream", "upstream", *upstreamURL, "error", err)
		os.Exit(1)
	}
	gateway, err := workergateway.New(upstream, logger)
	if err != nil {
		logger.Error("initialize worker gateway", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           gateway,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       1 * time.Minute,
		MaxHeaderBytes:    32 << 10,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("worker gateway shutdown failed", "error", err)
		}
	}()

	logger.Info(
		"worker gateway starting",
		"listen_address", server.Addr,
		"upstream", upstream.String(),
		"version", version,
		"commit", commit,
	)
	if err := server.ListenAndServeTLS(*certificateFile, *privateKeyFile); err != nil && err != http.ErrServerClosed {
		logger.Error("worker gateway stopped", "error", err)
		os.Exit(1)
	}
}
