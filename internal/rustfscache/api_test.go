package rustfscache

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPRequesterPinsTLSAndSignsWithoutRedirects(t *testing.T) {
	t.Parallel()

	var authorization string
	var contentSHA256 string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		contentSHA256 = request.Header.Get("X-Amz-Content-Sha256")
		if request.URL.Path != "/bucket/object" || request.URL.RawQuery != "version=1" {
			t.Errorf("unexpected request target: %s", request.URL.String())
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()

	requester := requesterForTLSServer(t, server)
	response, err := requester.Do(
		context.Background(),
		Credential{AccessKey: "AKIATESTACCESSKEY0000", SecretKey: []byte("test-secret-material-with-at-least-thirty-two-bytes")},
		http.MethodPut,
		"/bucket/object?version=1",
		"application/octet-stream",
		[]byte("payload"),
	)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(response.Body) != "ok" {
		t.Fatalf("response = %+v", response)
	}
	if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 Credential=AKIATESTACCESSKEY0000/") {
		t.Fatalf("request was not SigV4-signed: %q", authorization)
	}
	if contentSHA256 != "239f59ed55e737c77147cf55ad0c1b030b6d7ee748a7426952f9b852d5a935e5" {
		t.Fatalf("x-amz-content-sha256 = %q", contentSHA256)
	}

	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Store(true)
	}))
	defer target.Close()
	redirecting := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()
	redirectRequester := requesterForTLSServer(t, redirecting)
	response, err = redirectRequester.Do(context.Background(), Credential{
		AccessKey: "AKIATESTACCESSKEY0000", SecretKey: []byte("test-secret-material-with-at-least-thirty-two-bytes"),
	}, http.MethodGet, "/redirect", "", nil)
	if err != nil {
		t.Fatalf("redirect response: %v", err)
	}
	if response.StatusCode != http.StatusTemporaryRedirect || redirected.Load() {
		t.Fatalf("redirect was followed: status=%d target_hit=%v", response.StatusCode, redirected.Load())
	}
}

func TestHTTPRequesterRejectsInvalidPathAndOversizedResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(make([]byte, maximumResponseBytes+1))
	}))
	defer server.Close()
	requester := requesterForTLSServer(t, server)
	credential := Credential{AccessKey: "AKIATESTACCESSKEY0000", SecretKey: []byte("test-secret-material-with-at-least-thirty-two-bytes")}

	if _, err := requester.Do(context.Background(), credential, http.MethodGet, "https://example.invalid/object", "", nil); err == nil {
		t.Fatal("Do accepted an absolute URL")
	}
	if _, err := requester.Do(context.Background(), credential, http.MethodGet, "/large", "", nil); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func requesterForTLSServer(t *testing.T, server *httptest.Server) *HTTPRequester {
	t.Helper()
	directory := t.TempDir()
	caFile := filepath.Join(directory, "ca.pem")
	certificate := server.TLS.Certificates[0].Certificate[0]
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), 0o600); err != nil {
		t.Fatalf("write CA: %v", err)
	}
	requester, err := NewHTTPRequester(Config{Endpoint: server.URL, Region: "us-east-1", CAFile: caFile})
	if err != nil {
		t.Fatalf("NewHTTPRequester: %v", err)
	}
	return requester
}
