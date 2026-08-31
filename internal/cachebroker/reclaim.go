package cachebroker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
)

// DefaultReclaimGrace is how long a running intent may hold a slot with no
// execution lease before it is treated as finished. The broker writes the
// running intent from the job-start hook and the provider records the lease
// around the same moment, so the two can disagree briefly; five minutes is far
// longer than that disagreement and far shorter than any real job.
const DefaultReclaimGrace = 5 * time.Minute

// DefaultReclaimInterval is how often the ledger is reconciled.
const DefaultReclaimInterval = time.Minute

// Reclaimer releases running queue intents whose runner holds no provider
// execution lease.
//
// A running intent leaves the journal only when GARM sees a completed
// lifecycle message for its exact job UUID. When that message never arrives the
// intent stays for the execution horizon of a whole day, and every non-queued
// intent counts against max_in_flight and the cross-repository share. Measured
// on the live fleet before this existed: 28 running intents against 12 worker
// instances, holding 16 of 32 slots for jobs that had finished hours earlier,
// and still growing.
type Reclaimer struct {
	Correlator      *queueintent.Correlator
	ProviderJournal providerjournal.Store
	Grace           time.Duration
	Interval        time.Duration
	Logger          *slog.Logger
}

func (r Reclaimer) grace() time.Duration {
	if r.Grace > 0 {
		return r.Grace
	}
	return DefaultReclaimGrace
}

func (r Reclaimer) interval() time.Duration {
	if r.Interval > 0 {
		return r.Interval
	}
	return DefaultReclaimInterval
}

// Once performs one reconciliation and reports the runner names it released.
func (r Reclaimer) Once(ctx context.Context) ([]string, error) {
	if r.Correlator == nil || r.ProviderJournal.Path == "" {
		return nil, nil
	}
	journal, err := r.ProviderJournal.ReadOnly(ctx)
	if err != nil {
		return nil, fmt.Errorf("read provider journal: %w", err)
	}
	// A lease in any live state covers its runner. Only a lease that is gone
	// entirely means the worker is gone, and the deliberately wide set here is
	// what keeps a starting or draining worker from being mistaken for one that
	// has finished.
	covered := make(map[string]struct{}, len(journal.Leases))
	for name := range journal.Leases {
		covered[name] = struct{}{}
	}
	for _, claim := range journal.Claims {
		if claim.JobName != "" {
			covered[claim.JobName] = struct{}{}
		}
		if claim.InstanceName != "" {
			covered[claim.InstanceName] = struct{}{}
		}
	}
	return r.Correlator.ReleaseUncoveredRunning(ctx, covered, r.grace())
}

// Run reconciles on an interval until the context is cancelled.
func (r Reclaimer) Run(ctx context.Context) {
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(r.interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		released, err := r.Once(ctx)
		if err != nil {
			logger.WarnContext(ctx, "running intent reclamation failed", "error", err)
			continue
		}
		if len(released) == 0 {
			continue
		}
		logger.InfoContext(ctx, "released running queue intents without an execution lease",
			"released", len(released), "runners", released)
	}
}
