package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"github.com/NDDev-OpenNetwork/github-actions/internal/cachegateway"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var version = "dev"
var commit = "unknown"

func main() {
	listen := flag.String("listen", "", "TLS listener")
	upstreamRaw := flag.String("upstream", "", "local distributed RustFS origin")
	cert := flag.String("tls-cert", "/etc/gha-fleet/pki/cache/rustfs-cert.pem", "TLS certificate")
	key := flag.String("tls-key", "/etc/gha-fleet/pki/cache/rustfs-key.pem", "TLS key")
	show := flag.Bool("version", false, "print version")
	flag.Parse()
	if *show {
		fmt.Printf("gha-cache-s3-gateway %s (%s)\n", version, commit)
		return
	}
	if err := cachegateway.ValidateListen(*listen); err != nil {
		panic(err)
	}
	upstream, err := url.Parse(*upstreamRaw)
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	handler, err := cachegateway.New(upstream, logger)
	if err != nil {
		panic(err)
	}
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 5 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("cache S3 gateway starting", "listen", *listen, "upstream", upstream.String(), "version", version, "commit", commit)
	if err := server.ListenAndServeTLS(*cert, *key); err != nil && err != http.ErrServerClosed {
		logger.Error("cache S3 gateway stopped", "error", err)
		os.Exit(1)
	}
}
