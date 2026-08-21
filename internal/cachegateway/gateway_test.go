package cachegateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestGatewayPreservesSignedHostAndBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "192.0.2.1:9003" || r.URL.Path != "/bucket/key" || r.URL.RawQuery != "versionId=one" || r.Header.Get("Authorization") != "AWS4-HMAC-SHA256 fixture" {
			t.Fatalf("request host=%q path=%q query=%q", r.Host, r.URL.Path, r.URL.RawQuery)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "payload" {
			t.Fatalf("body=%q", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	parsed, _ := url.Parse(upstream.URL)
	handler := newHandler(parsed, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodPut, "https://192.0.2.1:9003/bucket/key?versionId=one", strings.NewReader("payload"))
	request.Host = "192.0.2.1:9003"
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 fixture")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestBoundaries(t *testing.T) {
	for _, value := range []string{"0.0.0.0:9003", "cache.invalid:9003", "192.0.2.1:9002"} {
		if ValidateListen(value) == nil {
			t.Fatalf("listen %q accepted", value)
		}
	}
	if ValidateListen("192.0.2.1:9003") != nil {
		t.Fatal("valid listen rejected")
	}
	for _, raw := range []string{"https://10.110.0.5:9102", "http://10.110.0.5:9002", "http://host:9102/path"} {
		parsed, _ := url.Parse(raw)
		if ValidateUpstream(parsed) == nil {
			t.Fatalf("upstream %q accepted", raw)
		}
	}
}
