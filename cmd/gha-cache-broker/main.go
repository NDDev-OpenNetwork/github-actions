package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/cachebroker"
)

var version = "dev"
var commit = "unknown"

func main() {
	configPath := flag.String("config", cachebroker.DefaultConfigPath, "cache broker configuration")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("gha-cache-broker %s (%s)\n", version, commit)
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	config, err := cachebroker.Load(*configPath)
	if err != nil {
		logger.Error("load cache broker config", "error", err)
		os.Exit(1)
	}
	handler := cachebroker.Handler{Config: config, Store: cachebroker.Store{Path: config.JournalFile, LockPath: config.JournalLock}, Logger: logger}
	server := &http.Server{Addr: config.ListenAddress, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	logger.Info("cache broker starting", "listen_address", config.ListenAddress, "version", version, "commit", commit)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("cache broker stopped", "error", err)
		os.Exit(1)
	}
}
