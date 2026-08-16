package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/stagetransaction"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gha-stage-transaction PLAN.json")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var plan stagetransaction.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// The whole transaction is bounded, not only each stage. A plan of stages
	// that each finish inside their own timeout can still outlive any window an
	// operator is prepared to wait through, and Run used to be handed a context
	// that could never expire.
	budget := plan.StageTimeout * time.Duration(len(plan.Stages))
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	// A terminal interrupt has to reach the stages, or the operator is left with
	// a transaction that keeps running after the thing that started it is gone.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := stagetransaction.Run(ctx, plan); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
