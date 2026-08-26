package diagnosticstoreobserve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticstore"
)

func TestSampleRendersHeadroomAndForecast(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	state := &State{}
	usage := int64(100)
	collect := func(context.Context) (diagnosticstore.Result, error) {
		return diagnosticstore.Result{StateAfter: "managed", QuotaBytes: 1000, CurrentUsageBytes: usage,
			RemainingQuotaBytes: 1000 - usage, MinimumHeadroomBytes: 200, HeadroomState: "sufficient"}, nil
	}
	state.Sample(t.Context(), collect, now)
	usage = 160
	state.Sample(t.Context(), collect, now.Add(time.Minute))
	metrics := Render(state.Snapshot(), now.Add(time.Minute))
	for _, wanted := range []string{
		"gha_diagnostic_storage_up 1\n",
		"gha_diagnostic_storage_growth_bytes_per_second 1\n",
		"gha_diagnostic_storage_forecast_exhaustion_seconds 840\n",
	} {
		if !strings.Contains(metrics, wanted) {
			t.Fatalf("metrics missing %q\n%s", wanted, metrics)
		}
	}
}

func TestRetentionMetricsExposeEligibleObjectsAndNextBoundary(t *testing.T) {
	now := time.Date(2026, 8, 26, 3, 0, 0, 0, time.UTC)
	snapshot := Snapshot{CapturedAt: now, Result: diagnosticstore.Result{
		StateAfter: "managed", HeadroomState: "sufficient", ObjectCount: 356,
		OldestObjectModified: time.Date(2026, 8, 19, 0, 36, 58, 0, time.UTC),
		ExpirationEligible:   0, NextExpirationAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC),
	}}
	metrics := Render(snapshot, now)
	for _, wanted := range []string{
		"gha_diagnostic_storage_objects 356\n",
		"gha_diagnostic_storage_oldest_object_age_seconds 613382\n",
		"gha_diagnostic_storage_expiration_eligible_objects 0\n",
		"gha_diagnostic_storage_next_expiration_seconds 75600\n",
	} {
		if !strings.Contains(metrics, wanted) {
			t.Fatalf("metrics missing %q\n%s", wanted, metrics)
		}
	}
}

func TestHealthFailsClosedForLowHeadroomAndStaleness(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	state := &State{snapshot: Snapshot{CapturedAt: now, Result: diagnosticstore.Result{StateAfter: "managed", HeadroomState: "below-minimum"}}}
	handler := Handler{State: state, Now: func() time.Time { return now }}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("low headroom health status=%d", response.Code)
	}
	state.snapshot.Result.HeadroomState = "sufficient"
	handler.Now = func() time.Time { return now.Add(MaxStaleness + time.Second) }
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale health status=%d", response.Code)
	}
}
