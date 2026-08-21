package diagnosticstoreobserve

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticstore"
)

const MaxStaleness = 3 * time.Minute

type Collector func(context.Context) (diagnosticstore.Result, error)

type Snapshot struct {
	CapturedAt                time.Time
	Result                    diagnosticstore.Result
	Error                     string
	GrowthBytesPerSecond      float64
	ForecastExhaustionSeconds float64
}

type State struct {
	mu       sync.RWMutex
	snapshot Snapshot
}

func (s *State) Sample(ctx context.Context, collect Collector, now time.Time) {
	result, err := collect(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	next := Snapshot{CapturedAt: now.UTC(), Result: result, ForecastExhaustionSeconds: -1}
	if err != nil {
		next.Error = err.Error()
		s.snapshot = next
		return
	}
	previous := s.snapshot
	if previous.Error == "" && !previous.CapturedAt.IsZero() && now.After(previous.CapturedAt) &&
		result.CurrentUsageBytes >= previous.Result.CurrentUsageBytes {
		delta := result.CurrentUsageBytes - previous.Result.CurrentUsageBytes
		next.GrowthBytesPerSecond = float64(delta) / now.Sub(previous.CapturedAt).Seconds()
		if next.GrowthBytesPerSecond > 0 {
			next.ForecastExhaustionSeconds = float64(result.RemainingQuotaBytes) / next.GrowthBytesPerSecond
		}
	}
	s.snapshot = next
}

func (s *State) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

type Handler struct {
	State *State
	Now   func() time.Time
}

func (h Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if request.URL.RawQuery != "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now().UTC()
	}
	snapshot := h.State.Snapshot()
	healthy := Healthy(snapshot, now)
	switch request.URL.Path {
	case "/healthz":
		writer.Header().Set("Content-Type", "application/json")
		if !healthy {
			writer.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = fmt.Fprintf(writer, "{\"healthy\":%t,\"fresh\":%t}\n", healthy, fresh(snapshot, now))
	case "/metrics":
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = writer.Write([]byte(Render(snapshot, now)))
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func Healthy(snapshot Snapshot, now time.Time) bool {
	return fresh(snapshot, now) && snapshot.Error == "" && snapshot.Result.StateAfter == "managed" &&
		snapshot.Result.HeadroomState == "sufficient"
}

func fresh(snapshot Snapshot, now time.Time) bool {
	return !snapshot.CapturedAt.IsZero() && !snapshot.CapturedAt.After(now.Add(time.Second)) && now.Sub(snapshot.CapturedAt) <= MaxStaleness
}

func Render(snapshot Snapshot, now time.Time) string {
	var output strings.Builder
	gauge := func(name, help string, value float64) {
		fmt.Fprintf(&output, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", name, help, name, name, strconv.FormatFloat(value, 'f', -1, 64))
	}
	age := float64(-1)
	if fresh(snapshot, now) {
		age = max(0, now.Sub(snapshot.CapturedAt).Seconds())
	}
	gauge("gha_diagnostic_storage_up", "Whether the signed RustFS capacity snapshot is fresh, managed and above minimum headroom.", boolFloat(Healthy(snapshot, now)))
	gauge("gha_diagnostic_storage_sample_age_seconds", "Age of the latest signed RustFS capacity snapshot.", age)
	gauge("gha_diagnostic_storage_quota_limit_bytes", "Configured RustFS hard quota for the diagnostic bucket.", float64(snapshot.Result.QuotaBytes))
	gauge("gha_diagnostic_storage_current_usage_bytes", "RustFS data-usage accounting for the diagnostic bucket.", float64(snapshot.Result.CurrentUsageBytes))
	gauge("gha_diagnostic_storage_remaining_quota_bytes", "Remaining diagnostic bucket hard quota.", float64(snapshot.Result.RemainingQuotaBytes))
	gauge("gha_diagnostic_storage_minimum_headroom_bytes", "Minimum operational diagnostic bucket headroom.", float64(snapshot.Result.MinimumHeadroomBytes))
	gauge("gha_diagnostic_storage_usage_percent", "Percentage of the diagnostic bucket hard quota currently used.", snapshot.Result.UsagePercentage)
	gauge("gha_diagnostic_storage_growth_bytes_per_second", "Non-negative growth rate between the two latest successful signed snapshots.", snapshot.GrowthBytesPerSecond)
	gauge("gha_diagnostic_storage_forecast_exhaustion_seconds", "Seconds to hard-quota exhaustion at the latest positive growth rate, or -1 when not forecastable.", snapshot.ForecastExhaustionSeconds)
	return output.String()
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
