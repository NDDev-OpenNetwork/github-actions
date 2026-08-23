package cachebroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCanonicalConfigLoads(t *testing.T) {
	config, err := Load(filepath.Join("..", "..", "config", "cache-broker.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := config.Identity("example-org/example-actions", "trusted-writer"); !ok {
		t.Fatal("trusted identity missing")
	}
}

func TestHealthChecksConfigAndJournal(t *testing.T) {
	directory := t.TempDir()
	config, err := Load(filepath.Join("..", "..", "config", "cache-broker.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	config.JournalFile = filepath.Join(directory, "claims.json")
	config.JournalLock = filepath.Join(directory, "claims.lock")
	handler := Handler{Config: config, Store: Store{Path: config.JournalFile, LockPath: config.JournalLock}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://broker"+HealthPath, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health=%d %s", response.Code, response.Body.String())
	}
}

func TestConcurrentClaimBindsExactlyOneRepository(t *testing.T) {
	directory := t.TempDir()
	store := Store{Path: filepath.Join(directory, "claims.json"), LockPath: filepath.Join(directory, "claims.lock")}
	token := bytes.Repeat([]byte{5}, ClaimTokenBytes)
	if err := store.Add(context.Background(), "runner-race", "pool", "trusted-writer", token); err != nil {
		t.Fatal(err)
	}
	repositories := []string{"example-org/one", "example-org/two"}
	results := make(chan string, 2)
	var wait sync.WaitGroup
	for _, repository := range repositories {
		repository := repository
		wait.Add(1)
		go func() {
			defer wait.Done()
			claim, err := store.Consume(context.Background(), "runner-race", token, repository)
			if err == nil {
				results <- claim.ClaimedRepository
			}
		}()
	}
	wait.Wait()
	close(results)
	winners := []string{}
	for winner := range results {
		winners = append(winners, winner)
	}
	if len(winners) != 1 {
		t.Fatalf("claim winners=%v, want exactly one repository", winners)
	}
}

func TestClaimIsAtomicOneTimeAndExpires(t *testing.T) {
	now := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	store := Store{Path: filepath.Join(directory, "claims.json"), LockPath: filepath.Join(directory, "claims.lock"), Now: func() time.Time { return now }}
	token := bytes.Repeat([]byte{7}, ClaimTokenBytes)
	if err := store.Add(context.Background(), "runner-one", "example-standard", "trusted-writer", token); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Verify(context.Background(), "runner-one", bytes.Repeat([]byte{8}, ClaimTokenBytes)); err == nil {
		t.Fatal("wrong token accepted")
	}
	claim, err := store.Consume(context.Background(), "runner-one", token, "example-org/example-actions")
	if err != nil || claim.Role != "trusted-writer" {
		t.Fatalf("consume=%+v err=%v", claim, err)
	}
	if replay, err := store.Consume(context.Background(), "runner-one", token, "example-org/example-actions"); err != nil || replay.ClaimedRepository != "example-org/example-actions" {
		t.Fatalf("idempotent claim replay failed: %+v %v", replay, err)
	}
	if err := store.Add(context.Background(), "runner-two", "example-standard", "trusted-writer", token); err != nil {
		t.Fatal(err)
	}
	now = now.Add(ClaimTTL + time.Second)
	if _, err := store.Verify(context.Background(), "runner-two", token); err == nil {
		t.Fatal("expired claim accepted")
	}
}

func TestUnclaimedCreateRetryRotatesToken(t *testing.T) {
	directory := t.TempDir()
	store := Store{Path: filepath.Join(directory, "claims.json"), LockPath: filepath.Join(directory, "claims.lock")}
	old := bytes.Repeat([]byte{1}, ClaimTokenBytes)
	fresh := bytes.Repeat([]byte{2}, ClaimTokenBytes)
	if err := store.Add(context.Background(), "runner-retry", "pool", "trusted-writer", old); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(context.Background(), "runner-retry", "pool", "trusted-writer", fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Verify(context.Background(), "runner-retry", old); err == nil {
		t.Fatal("rotated token remained valid")
	}
	if _, err := store.Verify(context.Background(), "runner-retry", fresh); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerReturnsExactScopedDeliveryOnce(t *testing.T) {
	directory := t.TempDir()
	write := func(name, value string) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ca := write("ca.pem", "-----BEGIN CERTIFICATE-----\nZXhhbXBsZQ==\n-----END CERTIFICATE-----")
	access := write("access", "AKIA0123456789ABCDEF")
	secret := write("secret", strings.Repeat("a", 64))
	config := Config{SchemaVersion: 1, ListenAddress: "192.0.2.2:9444", Endpoint: "https://192.0.2.1:9002", Region: "us-east-1", Bucket: "github-actions-cache", CAFile: ca, JournalFile: filepath.Join(directory, "claims.json"), JournalLock: filepath.Join(directory, "claims.lock"), Repositories: []Repository{{Name: "example-org/example-actions", Roles: []Identity{
		{Role: "trusted-writer", Mode: "read-write", Prefix: "example-org/example-actions/trust/trusted", AccessKeyFile: access, SecretKeyFile: secret},
		{Role: "untrusted-writer", Mode: "read-write", Prefix: "example-org/example-actions/trust/untrusted", AccessKeyFile: access, SecretKeyFile: secret},
		{Role: "release-reader", Mode: "read-only", Prefix: "example-org/example-actions/trust/promoted", AccessKeyFile: access, SecretKeyFile: secret},
	}}}}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	store := Store{Path: config.JournalFile, LockPath: config.JournalLock}
	token := bytes.Repeat([]byte{9}, ClaimTokenBytes)
	if err := store.Add(context.Background(), "runner-example", "example-standard", "trusted-writer", token); err != nil {
		t.Fatal(err)
	}
	requestBody, _ := json.Marshal(ClaimRequest{InstanceName: "runner-example", RunnerName: "runner-example", Repository: "example-org/example-actions", Token: base64.RawURLEncoding.EncodeToString(token)})
	handler := Handler{Config: config, Store: store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	request := httptest.NewRequest(http.MethodPost, "https://gateway.example"+ClaimPath, bytes.NewReader(requestBody))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var delivery Delivery
	if err := json.Unmarshal(response.Body.Bytes(), &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.Role != "trusted-writer" || delivery.PrefixRoot != "example-org/example-actions/trust/trusted" || delivery.AccessKey != "AKIA0123456789ABCDEF" {
		t.Fatalf("delivery=%+v", delivery)
	}
	request = httptest.NewRequest(http.MethodPost, "https://gateway.example"+ClaimPath, bytes.NewReader(requestBody))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("replay status=%d", response.Code)
	}
}

func TestUnknownRepositoryBindsClaimWithoutDeliveringSecret(t *testing.T) {
	directory := t.TempDir()
	store := Store{Path: filepath.Join(directory, "claims.json"), LockPath: filepath.Join(directory, "claims.lock")}
	token := bytes.Repeat([]byte{3}, ClaimTokenBytes)
	if err := store.Add(context.Background(), "runner-three", "pool", "trusted-writer", token); err != nil {
		t.Fatal(err)
	}
	config := Config{SchemaVersion: 1, ListenAddress: "192.0.2.2:9444", Endpoint: "https://192.0.2.1:9002", Region: "us-east-1", Bucket: "github-actions-cache", CAFile: "/tmp/ca", JournalFile: store.Path, JournalLock: store.LockPath, Repositories: []Repository{{Name: "example-org/example-actions", Roles: []Identity{{Role: "trusted-writer", Mode: "read-write", Prefix: "example-org/example-actions/trust/trusted", AccessKeyFile: "/tmp/a", SecretKeyFile: "/tmp/s"}, {Role: "untrusted-writer", Mode: "read-write", Prefix: "example-org/example-actions/trust/untrusted", AccessKeyFile: "/tmp/a", SecretKeyFile: "/tmp/s"}, {Role: "release-reader", Mode: "read-only", Prefix: "example-org/example-actions/trust/promoted", AccessKeyFile: "/tmp/a", SecretKeyFile: "/tmp/s"}}}}}
	body, _ := json.Marshal(ClaimRequest{
		InstanceName: "runner-three", RunnerName: "runner-three", Repository: "other/repo",
		RepositoryID: 123, WorkflowRunID: 456, RunAttempt: 1, JobName: "test",
		WorkflowRef: "other/repo/.github/workflows/ci.yml@refs/heads/main",
		CommitSHA:   strings.Repeat("a", 40), Token: base64.RawURLEncoding.EncodeToString(token),
	})
	response := httptest.NewRecorder()
	var logs bytes.Buffer
	Handler{Config: config, Store: store, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "https://x"+ClaimPath, bytes.NewReader(body)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d", response.Code)
	}
	journal, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Claims["runner-three"].ClaimedRepository != "other/repo" {
		t.Fatalf("optional miss did not bind exact repository: %+v", journal.Claims["runner-three"])
	}
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if len(entries) < 2 || entries[0]["msg"] != "job correlation accepted" ||
		entries[0]["github.repository"] != "other/repo" ||
		entries[0]["github.workflow_run_id"] != float64(456) ||
		entries[0]["runner.name"] != "runner-three" {
		t.Fatalf("correlation log=%v", entries)
	}
}

func TestJobCorrelationIsAllOrNothingAndBounded(t *testing.T) {
	legacy := ClaimRequest{}
	if err := validateJobCorrelation(legacy); err != nil {
		t.Fatalf("legacy request rejected: %v", err)
	}
	complete := ClaimRequest{
		RepositoryID: 123, WorkflowRunID: 456, RunAttempt: 1,
		JobName: "test", WorkflowRef: "example/repo/.github/workflows/ci.yml@refs/heads/main",
		CommitSHA: strings.Repeat("a", 40),
	}
	if err := validateJobCorrelation(complete); err != nil {
		t.Fatalf("complete correlation rejected: %v", err)
	}
	incomplete := complete
	incomplete.WorkflowRunID = 0
	if err := validateJobCorrelation(incomplete); err == nil {
		t.Fatal("incomplete correlation accepted")
	}
	invalidSHA := complete
	invalidSHA.CommitSHA = strings.Repeat("g", 40)
	if err := validateJobCorrelation(invalidSHA); err == nil {
		t.Fatal("non-hex commit accepted")
	}
	oversized := complete
	oversized.JobName = strings.Repeat("x", 1025)
	if err := validateJobCorrelation(oversized); err == nil {
		t.Fatal("oversized job identity accepted")
	}
}
