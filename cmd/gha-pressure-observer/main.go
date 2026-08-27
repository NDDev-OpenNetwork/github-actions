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

	"github.com/NDDev-OpenNetwork/github-actions/internal/pressureobserve"
)

var version = "dev"
var commit = "unknown"

func main() {
	statePath := flag.String("state-path", "/var/lib/gha-fleet/pressure-gate.json", "pressure state JSON path")
	listen := flag.String("listen", "127.0.0.1:9471", "loopback listen address")
	maxStaleness := flag.Duration("max-staleness", pressureobserve.DefaultMaxStaleness, "maximum pressure state age")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "positional arguments are not accepted")
		os.Exit(2)
	}
	if *showVersion {
		fmt.Printf("gha-pressure-observer %s (%s)\n", version, commit)
		return
	}
	if *listen == "" || *maxStaleness <= 0 {
		slog.Error("invalid pressure observer configuration")
		os.Exit(1)
	}
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: *listen, Handler: pressureobserve.Handler{StatePath: *statePath, MaxStaleness: *maxStaleness},
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-root.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("pressure observer stopped", "error", err)
		os.Exit(1)
	}
}
