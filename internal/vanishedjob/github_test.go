package vanishedjob

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestGitHubClientUsesExactRunOperations(t *testing.T) {
	requests := []string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret-token" || request.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Fatal("request omitted bounded authentication or API version")
		}
		requests = append(requests, request.URL.Path)
		if request.URL.Path == "/repos/example-org/example-repo/actions/runs/42/force-cancel" {
			writer.WriteHeader(http.StatusAccepted)
			return
		}
		if request.URL.Path == "/repos/example-org/example-repo/actions/runs/42/rerun" {
			writer.WriteHeader(http.StatusCreated)
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := GitHubClient{Endpoint: endpoint, Token: "secret-token", HTTP: server.Client()}
	if err := client.ForceCancel(context.Background(), "example-org/example-repo", 42); err != nil {
		t.Fatal(err)
	}
	if err := client.FullRerun(context.Background(), "example-org/example-repo", 42); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%v", requests)
	}
}

func TestGitHubClientTreatsTerminalCancelRaceAsProgress(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client := GitHubClient{Endpoint: endpoint, Token: "secret-token", HTTP: server.Client()}
	if err := client.ForceCancel(context.Background(), "example-org/example-repo", 42); err != nil {
		t.Fatal(err)
	}
}
