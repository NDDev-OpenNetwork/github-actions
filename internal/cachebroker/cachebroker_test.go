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

	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
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

func TestBoundClaimRetainsOnlyRepositoryCorrelationAfterTokenExpiry(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	store := Store{
		Path: filepath.Join(directory, "claims.json"), LockPath: filepath.Join(directory, "claims.lock"),
		Now: func() time.Time { return now },
	}
	token := bytes.Repeat([]byte{7}, ClaimTokenBytes)
	if err := store.Add(context.Background(), "runner-correlation", "example-standard", "correlation-only", token); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := store.Consume(context.Background(), "runner-correlation", token, "example-org/example-repo"); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	now = now.Add(ClaimTTL + time.Second)
	if _, err := store.Verify(context.Background(), "runner-correlation", token); err == nil {
		t.Fatal("expired credential token remained valid")
	}
	if _, err := store.Consume(context.Background(), "runner-correlation", token, "example-org/example-repo"); err == nil {
		t.Fatal("expired credential token was consumed")
	}
	repository, err := store.ClaimedRepository(context.Background(), "runner-correlation")
	if err != nil || repository != "example-org/example-repo" {
		t.Fatalf("ClaimedRepository = %q, %v", repository, err)
	}

	now = now.Add(CorrelationTTL)
	if _, err := store.ClaimedRepository(context.Background(), "runner-correlation"); err == nil {
		t.Fatal("expired diagnostic correlation remained readable")
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
	var logs bytes.Buffer
	handler := Handler{Config: config, Store: store, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
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
	logged := logs.String()
	if !strings.Contains(logged, `"msg":"cache claim delivered"`) || !strings.Contains(logged, `"repository":"example-org/example-actions"`) ||
		!strings.Contains(logged, delivery.DeliveryID) ||
		strings.Contains(logged, delivery.AccessKey) || strings.Contains(logged, delivery.SecretKeyB64) {
		t.Fatalf("delivery evidence is missing or secret-bearing: %s", logged)
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
	if err := store.Add(context.Background(), "runner-three", "pool", "correlation-only", token); err != nil {
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
	if journal.Claims["runner-three"].Role != "correlation-only" {
		t.Fatalf("correlation-only claim changed role: %+v", journal.Claims["runner-three"])
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

func TestAuthenticatedClaimBindsExactRunningQueueCorrelation(t *testing.T) {
	now := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	store := Store{Path: filepath.Join(directory, "claims.json"), LockPath: filepath.Join(directory, "claims.lock")}
	token := bytes.Repeat([]byte{4}, ClaimTokenBytes)
	if err := store.Add(context.Background(), "runner-exact", "example-standard", "trusted-writer", token); err != nil {
		t.Fatal(err)
	}
	queuePath := filepath.Join(directory, "queue-intents.json")
	queueLock := filepath.Join(directory, "queue-intents.lock")
	key := "github-scale-set-job:v2:42:job-1"
	queue := queueintent.Journal{
		SchemaVersion: queueintent.SchemaVersion, Generation: 4, UpdatedAt: now,
		Intents: map[string]queueintent.Intent{key: {
			Key: key, ScaleSetID: 42, JobID: "job-1", RunnerRequestID: 99,
			ScaleSetName: "example-standard", JobDisplayName: "quality / go (1.26)", Owner: "example-org",
			Repository: "example-org", WorkflowRef: "unavailable-before-job-available", EventName: "push",
			QueueTime: now.Add(-time.Minute), State: queueintent.StateAcquired, Priority: 1,
			StateEnteredAt: now.Add(-30 * time.Second), UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
		}}, Repositories: map[string]queueintent.RepositoryState{}, TerminalJobs: map[string]time.Time{},
	}
	raw, err := json.Marshal(queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queuePath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	config := Config{SchemaVersion: 1, ListenAddress: "192.0.2.2:9444", Endpoint: "https://192.0.2.1:9002", Region: "us-east-1", Bucket: "github-actions-cache", CAFile: "/tmp/ca", JournalFile: store.Path, JournalLock: store.LockPath, QueueJournalFile: queuePath, QueueJournalLock: queueLock, Repositories: []Repository{{Name: "example-org/example-actions", Roles: []Identity{{Role: "trusted-writer", Mode: "read-write", Prefix: "example-org/example-actions/trust/trusted", AccessKeyFile: "/tmp/a", SecretKeyFile: "/tmp/s"}, {Role: "untrusted-writer", Mode: "read-write", Prefix: "example-org/example-actions/trust/untrusted", AccessKeyFile: "/tmp/a", SecretKeyFile: "/tmp/s"}, {Role: "release-reader", Mode: "read-only", Prefix: "example-org/example-actions/trust/promoted", AccessKeyFile: "/tmp/a", SecretKeyFile: "/tmp/s"}}}}}
	body, _ := json.Marshal(ClaimRequest{
		InstanceName: "runner-exact", RunnerName: "runner-exact", Repository: "example-org/example-repo",
		RepositoryID: 123, WorkflowRunID: 456, RunAttempt: 1, JobName: "quality",
		WorkflowRef: "example-org/example-repo/.github/workflows/ci.yml@refs/heads/main",
		CommitSHA:   strings.Repeat("a", 40), Token: base64.RawURLEncoding.EncodeToString(token),
	})
	var logs bytes.Buffer
	correlator := &queueintent.Correlator{Path: queuePath, LockPath: queueLock, Now: func() time.Time { return now.Add(time.Second) }, Attempts: 1}
	handler := Handler{Config: config, Store: store, QueueCorrelator: correlator, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "https://x"+ClaimPath, bytes.NewReader(body)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	snapshot, err := (queueintent.Reader{Path: queuePath, Now: func() time.Time { return now.Add(time.Second) }}).ReadActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	intent := snapshot.Active[0]
	if intent.Repository != "example-org/example-repo" || intent.WorkflowRunID != 456 || intent.JobDisplayName != "quality / go (1.26)" ||
		intent.State != queueintent.StateRunning || intent.RunnerName != "runner-exact" {
		t.Fatalf("correlated intent=%+v", intent)
	}
	if !strings.Contains(logs.String(), `"msg":"queue running correlation bound"`) || !strings.Contains(logs.String(), `"queue_job_uuid":"`+key+`"`) {
		t.Fatalf("missing correlation evidence: %s", logs.String())
	}
}

func TestWarmClaimBindsProviderInstanceToExactRuntimeRunner(t *testing.T) {
	now := time.Date(2026, 8, 31, 2, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	store := Store{Path: filepath.Join(directory, "claims.json"), LockPath: filepath.Join(directory, "claims.lock")}
	token := bytes.Repeat([]byte{6}, ClaimTokenBytes)
	if err := store.Add(context.Background(), "warm-standard-example", "example-standard", "correlation-only", token); err != nil {
		t.Fatal(err)
	}
	queuePath := filepath.Join(directory, "queue-intents.json")
	queueLock := filepath.Join(directory, "queue-intents.lock")
	key := "github-scale-set-job:v2:42:job-warm"
	queue := queueintent.Journal{
		SchemaVersion: queueintent.SchemaVersion, Generation: 7, UpdatedAt: now,
		Intents: map[string]queueintent.Intent{key: {
			Key: key, ScaleSetID: 42, JobID: "job-warm", RunnerRequestID: 99,
			ScaleSetName: "example-standard", JobDisplayName: "quality / go (1.26)", Owner: "example-org",
			Repository: "example-org", WorkflowRef: "unavailable-before-job-available", EventName: "push",
			QueueTime: now.Add(-time.Minute), State: queueintent.StateAcquired, Priority: 1,
			StateEnteredAt: now.Add(-30 * time.Second), UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
		}}, Repositories: map[string]queueintent.RepositoryState{}, TerminalJobs: map[string]time.Time{},
	}
	raw, err := json.Marshal(queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queuePath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	config := Config{
		SchemaVersion: 1, ListenAddress: "192.0.2.2:9444", Endpoint: "https://192.0.2.1:9002",
		Region: "us-east-1", Bucket: "github-actions-cache", CAFile: "/tmp/ca",
		JournalFile: store.Path, JournalLock: store.LockPath, QueueJournalFile: queuePath, QueueJournalLock: queueLock,
	}
	body, _ := json.Marshal(ClaimRequest{
		InstanceName: "warm-standard-example", RunnerName: "runner-runtime", Repository: "example-org/example-repo",
		RepositoryID: 123, WorkflowRunID: 456, RunAttempt: 1, JobName: "quality",
		WorkflowRef: "example-org/example-repo/.github/workflows/ci.yml@refs/heads/main",
		CommitSHA:   strings.Repeat("a", 40), Token: base64.RawURLEncoding.EncodeToString(token),
	})
	correlator := &queueintent.Correlator{
		Path: queuePath, LockPath: queueLock, Now: func() time.Time { return now.Add(time.Second) }, Attempts: 1,
	}
	var logs bytes.Buffer
	response := httptest.NewRecorder()
	Handler{Config: config, Store: store, QueueCorrelator: correlator, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}.
		ServeHTTP(response, httptest.NewRequest(http.MethodPost, "https://x"+ClaimPath, bytes.NewReader(body)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s logs=%s", response.Code, response.Body.String(), logs.String())
	}
	snapshot, err := (queueintent.Reader{Path: queuePath, Now: func() time.Time { return now.Add(time.Second) }}).ReadActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	intent := snapshot.Active[0]
	if intent.RunnerName != "runner-runtime" || intent.Repository != "example-org/example-repo" || intent.State != queueintent.StateRunning {
		t.Fatalf("warm runtime correlation=%+v", intent)
	}
	journal, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Claims["warm-standard-example"].ClaimedRepository != "example-org/example-repo" {
		t.Fatalf("warm claim was not bound to the exact repository: %+v", journal.Claims["warm-standard-example"])
	}
	if !strings.Contains(logs.String(), `"msg":"warm runner correlation authorized"`) ||
		!strings.Contains(logs.String(), `"runner.name":"runner-runtime"`) {
		t.Fatalf("warm correlation evidence is missing: %s", logs.String())
	}
}

func TestWarmClaimWithoutExactQueueCorrelationIsDenied(t *testing.T) {
	directory := t.TempDir()
	store := Store{Path: filepath.Join(directory, "claims.json"), LockPath: filepath.Join(directory, "claims.lock")}
	token := bytes.Repeat([]byte{8}, ClaimTokenBytes)
	if err := store.Add(context.Background(), "warm-standard-denied", "example-standard", "correlation-only", token); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ClaimRequest{
		InstanceName: "warm-standard-denied", RunnerName: "runner-unproved", Repository: "example-org/example-repo",
		RepositoryID: 123, WorkflowRunID: 456, RunAttempt: 1, JobName: "quality",
		WorkflowRef: "example-org/example-repo/.github/workflows/ci.yml@refs/heads/main",
		CommitSHA:   strings.Repeat("a", 40), Token: base64.RawURLEncoding.EncodeToString(token),
	})
	response := httptest.NewRecorder()
	Handler{Store: store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}.
		ServeHTTP(response, httptest.NewRequest(http.MethodPost, "https://x"+ClaimPath, bytes.NewReader(body)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	journal, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Claims["warm-standard-denied"].ClaimedRepository != "" {
		t.Fatal("uncorrelated warm claim consumed the one-job token")
	}
}

func TestAsyncCorrelationUsesExactRunnerAfterHookReturns(t *testing.T) {
	now := time.Date(2026, 8, 26, 20, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	queuePath := filepath.Join(directory, "queue-intents.json")
	queueLock := filepath.Join(directory, "queue-intents.lock")
	key := "github-scale-set-job:v2:42:job-async"
	queue := queueintent.Journal{
		SchemaVersion: queueintent.SchemaVersion, Generation: 8, UpdatedAt: now,
		Intents: map[string]queueintent.Intent{key: {
			Key: key, ScaleSetID: 42, JobID: "job-async", RunnerRequestID: 99,
			ScaleSetName: "example-standard", RunnerName: "runner-async", Owner: "example-org",
			Repository: "example-org/example-repo", JobDisplayName: "arbitrary display title",
			WorkflowRef: "example/ref", EventName: "push", QueueTime: now.Add(-time.Minute),
			State: queueintent.StateRunning, Priority: 1, StateEnteredAt: now.Add(-time.Second),
			UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
		}}, Repositories: map[string]queueintent.RepositoryState{}, TerminalJobs: map[string]time.Time{},
	}
	raw, err := json.Marshal(queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queuePath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		QueueCorrelator:          &queueintent.Correlator{Path: queuePath, LockPath: queueLock, Now: func() time.Time { return now.Add(time.Second) }},
		CorrelationRetryInterval: time.Millisecond, CorrelationRetryWindow: time.Second,
	}
	handler.retryQueueCorrelation(queueintent.RunningCorrelation{
		RunnerName: "runner-async", PoolName: "example-standard", Repository: "example-org/example-repo",
		WorkflowRunID: 789, JobDisplayName: "job-key-does-not-match-title", WorkflowRef: "example/ref",
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot, readErr := (queueintent.Reader{Path: queuePath, Now: func() time.Time { return now.Add(time.Second) }}).ReadActive(context.Background())
		if readErr == nil && len(snapshot.Active) == 1 && snapshot.Active[0].WorkflowRunID == 789 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("async exact-runner correlation did not converge")
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

// A matrix job's display name is what GitHub puts in the scale-set message --
// `Analyze (python)` -- while GITHUB_JOB is the workflow's job id, `analyze`.
// Requiring those to match refused the cache to every matrix job on a warm
// runner: 19 refusals against 7 binds in one live hour. Authorization asks the
// tighter question instead, whether this scale set is serving that repository's
// exact workflow run.
func TestWarmClaimIsAuthorizedForAMatrixJobDisplayName(t *testing.T) {
	now := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	store := Store{Path: filepath.Join(directory, "claims.json"), LockPath: filepath.Join(directory, "claims.lock")}
	token := bytes.Repeat([]byte{9}, ClaimTokenBytes)
	if err := store.Add(context.Background(), "warm-standard-matrix", "example-standard", "correlation-only", token); err != nil {
		t.Fatal(err)
	}
	queuePath := filepath.Join(directory, "queue-intents.json")
	queueLock := filepath.Join(directory, "queue-intents.lock")
	intents := map[string]queueintent.Intent{}
	for index, language := range []string{"python", "go", "rust"} {
		key := "github-scale-set-job:v2:42:job-matrix-" + language
		intents[key] = queueintent.Intent{
			Key: key, ScaleSetID: 42, JobID: "job-matrix-" + language, RunnerRequestID: int64(700 + index),
			ScaleSetName: "example-standard", JobDisplayName: "Analyze (" + language + ")", Owner: "example-org",
			Repository: "example-org/example-repo", WorkflowRef: "unavailable-before-job-available", EventName: "push",
			QueueTime: now.Add(-time.Minute), State: queueintent.StateAssigned, Priority: 1, WorkflowRunID: 456,
			StateEnteredAt: now.Add(-30 * time.Second), UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
		}
	}
	queue := queueintent.Journal{
		SchemaVersion: queueintent.SchemaVersion, Generation: 11, UpdatedAt: now, Intents: intents,
		Repositories: map[string]queueintent.RepositoryState{}, TerminalJobs: map[string]time.Time{},
	}
	raw, err := json.Marshal(queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queuePath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	config := Config{
		SchemaVersion: 1, ListenAddress: "192.0.2.2:9444", Endpoint: "https://192.0.2.1:9002",
		Region: "us-east-1", Bucket: "github-actions-cache", CAFile: "/tmp/ca",
		JournalFile: store.Path, JournalLock: store.LockPath, QueueJournalFile: queuePath, QueueJournalLock: queueLock,
	}
	body, _ := json.Marshal(ClaimRequest{
		InstanceName: "warm-standard-matrix", RunnerName: "runner-runtime-matrix", Repository: "example-org/example-repo",
		RepositoryID: 123, WorkflowRunID: 456, RunAttempt: 1, JobName: "analyze",
		WorkflowRef: "example-org/example-repo/.github/workflows/codeql.yml@refs/heads/main",
		CommitSHA:   strings.Repeat("b", 40), Token: base64.RawURLEncoding.EncodeToString(token),
	})
	correlator := &queueintent.Correlator{
		Path: queuePath, LockPath: queueLock, Now: func() time.Time { return now.Add(time.Second) }, Attempts: 1,
	}
	var logs bytes.Buffer
	response := httptest.NewRecorder()
	Handler{Config: config, Store: store, QueueCorrelator: correlator, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}.
		ServeHTTP(response, httptest.NewRequest(http.MethodPost, "https://x"+ClaimPath, bytes.NewReader(body)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("a matrix job was refused its cache: status=%d body=%s logs=%s", response.Code, response.Body.String(), logs.String())
	}
	journal, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Claims["warm-standard-matrix"].ClaimedRepository != "example-org/example-repo" {
		t.Fatalf("warm claim was not bound to the exact repository: %+v", journal.Claims["warm-standard-matrix"])
	}
	// Three sibling intents share this run, so no single one can be resolved and
	// the journal must be left for the asynchronous binder rather than guessed at.
	if !strings.Contains(logs.String(), `"msg":"warm runner correlation authorized"`) ||
		!strings.Contains(logs.String(), `"msg":"queue running correlation deferred"`) {
		t.Fatalf("authorization evidence is missing: %s", logs.String())
	}
}

// Authorization is not a weakening: a warm runner presenting a repository this
// scale set holds no active work for is still refused, and its one-job token is
// not consumed.
func TestWarmClaimForAnotherRepositoryIsStillRefused(t *testing.T) {
	now := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	store := Store{Path: filepath.Join(directory, "claims.json"), LockPath: filepath.Join(directory, "claims.lock")}
	token := bytes.Repeat([]byte{10}, ClaimTokenBytes)
	if err := store.Add(context.Background(), "warm-standard-foreign", "example-standard", "correlation-only", token); err != nil {
		t.Fatal(err)
	}
	queuePath := filepath.Join(directory, "queue-intents.json")
	queueLock := filepath.Join(directory, "queue-intents.lock")
	key := "github-scale-set-job:v2:42:job-owned"
	queue := queueintent.Journal{
		SchemaVersion: queueintent.SchemaVersion, Generation: 3, UpdatedAt: now,
		Intents: map[string]queueintent.Intent{key: {
			Key: key, ScaleSetID: 42, JobID: "job-owned", RunnerRequestID: 71,
			ScaleSetName: "example-standard", JobDisplayName: "Analyze (python)", Owner: "example-org",
			Repository: "example-org/example-repo", WorkflowRef: "unavailable-before-job-available", EventName: "push",
			QueueTime: now.Add(-time.Minute), State: queueintent.StateAssigned, Priority: 1, WorkflowRunID: 456,
			StateEnteredAt: now.Add(-30 * time.Second), UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
		}}, Repositories: map[string]queueintent.RepositoryState{}, TerminalJobs: map[string]time.Time{},
	}
	raw, err := json.Marshal(queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queuePath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ClaimRequest{
		InstanceName: "warm-standard-foreign", RunnerName: "runner-runtime-foreign", Repository: "other-org/other-repo",
		RepositoryID: 321, WorkflowRunID: 456, RunAttempt: 1, JobName: "analyze",
		WorkflowRef: "other-org/other-repo/.github/workflows/codeql.yml@refs/heads/main",
		CommitSHA:   strings.Repeat("c", 40), Token: base64.RawURLEncoding.EncodeToString(token),
	})
	correlator := &queueintent.Correlator{
		Path: queuePath, LockPath: queueLock, Now: func() time.Time { return now.Add(time.Second) }, Attempts: 1,
	}
	var logs bytes.Buffer
	response := httptest.NewRecorder()
	Handler{Store: store, QueueCorrelator: correlator, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}.
		ServeHTTP(response, httptest.NewRequest(http.MethodPost, "https://x"+ClaimPath, bytes.NewReader(body)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("a foreign repository was authorized: status=%d body=%s", response.Code, response.Body.String())
	}
	journal, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Claims["warm-standard-foreign"].ClaimedRepository != "" {
		t.Fatal("a refused warm claim consumed the one-job token")
	}
	// The refusal used to log nothing at all, which made the one failure that
	// costs every warm runner its cache the only one that could not be read.
	if !strings.Contains(logs.String(), `"msg":"warm runner correlation refused"`) ||
		!strings.Contains(logs.String(), `"github.repository":"other-org/other-repo"`) ||
		!strings.Contains(logs.String(), `"github.job_name":"analyze"`) {
		t.Fatalf("refusal is not diagnosable from its own log: %s", logs.String())
	}
}
