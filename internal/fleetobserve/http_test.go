package fleetobserve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandlerServesOnlyLoopbackCollectorRoutes(t *testing.T) {
	snapshot := healthyCollector(t).Collect(t.Context())
	state := &State{}
	state.Set(snapshot)
	handler := Handler{State: state, MaxStaleness: 45 * time.Second, Now: func() time.Time {
		return observationTime.Add(5 * time.Second)
	}}

	for route, contentType := range map[string]string{
		"/metrics":  "text/plain",
		"/snapshot": "application/json",
		"/healthz":  "application/json",
	} {
		request := httptest.NewRequest(http.MethodGet, route, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), contentType) {
			t.Fatalf("%s response = %d %q", route, response.Code, response.Header().Get("Content-Type"))
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s is cacheable", route)
		}
	}

	for name, test := range map[string]struct {
		method string
		route  string
		status int
	}{
		"post":  {http.MethodPost, "/metrics", http.StatusMethodNotAllowed},
		"query": {http.MethodGet, "/metrics?token=forbidden", http.StatusBadRequest},
		"route": {http.MethodGet, "/admin", http.StatusNotFound},
	} {
		request := httptest.NewRequest(test.method, test.route, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s response = %d, want %d", name, response.Code, test.status)
		}
	}
}

func TestHandlerFailsHealthForStaleOrMissingSample(t *testing.T) {
	state := &State{}
	handler := Handler{State: state, MaxStaleness: 45 * time.Second, Now: func() time.Time {
		return observationTime.Add(time.Minute)
	}}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing-sample response = %d", response.Code)
	}

	state.Set(healthyCollector(t).Collect(t.Context()))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"fresh":false`) {
		t.Fatalf("stale response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerFailsHealthForFutureSample(t *testing.T) {
	state := &State{}
	state.Set(healthyCollector(t).Collect(t.Context()))
	handler := Handler{State: state, MaxStaleness: 45 * time.Second, Now: func() time.Time {
		return observationTime.Add(-time.Second)
	}}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"fresh":false`) {
		t.Fatalf("future response = %d %s", response.Code, response.Body.String())
	}
}
