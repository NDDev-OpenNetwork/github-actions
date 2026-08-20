package workergateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWorkerRoutesAreProxied(t *testing.T) {
	t.Parallel()

	requests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/callbacks/status"},
		{http.MethodPost, "/api/v1/callbacks/system-info/"},
		{http.MethodOptions, "/api/v1/callbacks/status"},
		{http.MethodGet, "/api/v1/metadata/runner-metadata"},
		{http.MethodGet, "/api/v1/metadata/runner-registration-token/"},
		{http.MethodGet, "/api/v1/metadata/credentials/runner"},
		{http.MethodGet, "/api/v1/metadata/credentials/credentials"},
		{http.MethodGet, "/api/v1/metadata/credentials/credentials_rsaparams"},
		{http.MethodGet, "/api/v1/metadata/system/service-name"},
		{http.MethodGet, "/api/v1/metadata/systemd/unit-file?runAsUser=runner"},
		{http.MethodGet, "/api/v1/metadata/system/cert-bundle"},
		{http.MethodGet, "/api/v1/metadata/tools/garm-agent"},
		{http.MethodGet, "/api/v1/metadata/tools/garm-agent/17"},
		{http.MethodGet, "/api/v1/metadata/tools/garm-agent/17/download"},
		{http.MethodGet, "/api/v1/metadata/install-script"},
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer instance-token" {
			t.Errorf("authorization header was not preserved")
		}
		if request.Header.Get("X-Forwarded-Proto") != "https" {
			t.Errorf("forwarded protocol was not sanitized")
		}
		if request.Header.Get("X-Forwarded-Host") != "gateway.invalid" {
			t.Errorf("forwarded host was not derived from the request")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	gateway := newTestGateway(t, upstream.URL)

	for _, test := range requests {
		test := test
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "https://gateway.invalid"+test.path, nil)
			request.Header.Set("Authorization", "Bearer instance-token")
			request.Header.Set("Forwarded", "for=attacker")
			request.Header.Set("X-Forwarded-For", "attacker")
			request.Header.Set("X-Forwarded-Host", "attacker")
			request.Header.Set("X-Forwarded-Proto", "http")
			response := httptest.NewRecorder()

			gateway.ServeHTTP(response, request)

			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d; body=%q", response.Code, http.StatusNoContent, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("missing response cache protection")
			}
		})
	}
}

func TestAdministrativeAndUnknownRoutesAreNotExposed(t *testing.T) {
	t.Parallel()

	paths := []string{
		"/",
		"/api/v1/auth/login",
		"/api/v1/controller",
		"/api/v1/instances",
		"/api/v1/metrics-token",
		"/api/v1/ws/agent/runner/shell",
		"/webhooks",
		"/metrics",
		"/debug/pprof/",
		"/agent",
		"/api/v1/metadata/not-a-worker-route",
		"/api/v1/metadata/credentials/not-allowed",
		"/api/v1/metadata/tools/garm-agent/not-a-number/download",
	}
	gateway := newTestGateway(t, uncalledUpstream(t))
	for _, requestPath := range paths {
		request := httptest.NewRequest(http.MethodGet, "https://gateway.invalid"+requestPath, nil)
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want %d", requestPath, response.Code, http.StatusNotFound)
		}
	}
}

func TestRejectsUnexpectedMethodsQueriesAndPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		target string
		status int
	}{
		{"callback get", http.MethodGet, "/api/v1/callbacks/status", http.StatusMethodNotAllowed},
		{"metadata post", http.MethodPost, "/api/v1/metadata/install-script", http.StatusMethodNotAllowed},
		{"metadata query", http.MethodGet, "/api/v1/metadata/install-script?admin=true", http.StatusBadRequest},
		{"systemd wrong user", http.MethodGet, "/api/v1/metadata/systemd/unit-file?runAsUser=root", http.StatusBadRequest},
		{"systemd duplicate", http.MethodGet, "/api/v1/metadata/systemd/unit-file?runAsUser=runner&runAsUser=runner", http.StatusBadRequest},
		{"systemd malformed", http.MethodGet, "/api/v1/metadata/systemd/unit-file?runAsUser=%ZZ", http.StatusBadRequest},
		{"encoded path", http.MethodGet, "/api/v1/metadata/%69nstall-script", http.StatusBadRequest},
		{"dot segment", http.MethodGet, "/api/v1/metadata/system/../install-script", http.StatusBadRequest},
		{"duplicate slash", http.MethodGet, "/api/v1/metadata//install-script", http.StatusBadRequest},
		{"backslash", http.MethodGet, "/api/v1/metadata/system\\service-name", http.StatusBadRequest},
	}

	gateway := newTestGateway(t, uncalledUpstream(t))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "https://gateway.invalid"+test.target, nil)
			response := httptest.NewRecorder()
			gateway.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
		})
	}
}

func TestRejectsDeclaredOversizedBody(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, uncalledUpstream(t))
	request := httptest.NewRequest(
		http.MethodPost,
		"https://gateway.invalid/api/v1/callbacks/status",
		strings.NewReader("small"),
	)
	request.ContentLength = MaxRequestBodyBytes + 1
	response := httptest.NewRecorder()

	gateway.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestUpstreamMustBeLoopback(t *testing.T) {
	t.Parallel()

	bad := []string{
		"https://127.0.0.1:9997",
		"http://garm.internal:9997",
		"http://127.0.0.1:9997/admin",
		"http://user:pass@127.0.0.1:9997",
	}
	for _, rawURL := range bad {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := New(parsed, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
			t.Errorf("New(%q) unexpectedly succeeded", rawURL)
		}
	}
	if upstream, err := ProductionUpstream(); err != nil || upstream.String() != ExpectedUpstreamURL {
		t.Fatalf("ProductionUpstream() = %v, %v", upstream, err)
	}
}

func newTestGateway(t *testing.T, rawURL string) *Gateway {
	t.Helper()
	upstream, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := New(upstream, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}

func uncalledUpstream(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("denied request reached upstream")
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestValidateListenAddressRequiresALiteralAddress(t *testing.T) {
	for _, address := range []string{
		"gateway.internal:9443", "198.51.100.1", "198.51.100.1:", ":9443", "",
	} {
		if err := ValidateListenAddress(address); err == nil {
			t.Fatalf("listen address %q was accepted", address)
		}
	}
	for _, address := range []string{"198.51.100.1:9443", "10.200.0.7:9443"} {
		if err := ValidateListenAddress(address); err != nil {
			t.Fatalf("listen address %q was refused: %v", address, err)
		}
	}
}
