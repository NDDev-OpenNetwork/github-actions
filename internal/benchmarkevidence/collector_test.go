package benchmarkevidence

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testRunID = int64(12345)

var (
	testSHA       = strings.Repeat("a", 40)
	testCreatedAt = time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
)

type fixtureOptions struct {
	mixedCacheHit     bool
	digestMismatch    bool
	extraArchiveEntry bool
	reuseMachineID    bool
	reuseRunnerName   bool
	unknownRecordKey  bool
}

func TestCollectorBuildsStrictEvidenceWithoutLeakingAuthorization(t *testing.T) {
	server := newFixtureServer(t, fixtureOptions{})
	defer server.Close()

	evidence, err := (Collector{
		HTTPClient:            server.Client(),
		APIBaseURL:            server.URL,
		Token:                 "test-token",
		Now:                   func() time.Time { return testCreatedAt.Add(20 * time.Minute) },
		allowInsecureForTests: true,
	}).Collect(context.Background(), Options{Repository: "acme/repo", RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.SchemaVersion != 1 || evidence.Repository != "acme/repo" || evidence.Workflow.RunID != testRunID {
		t.Fatalf("unexpected evidence identity: %#v", evidence)
	}
	if evidence.Sample.Environment != "nddev" || evidence.Sample.CacheMode != "warm" || evidence.Sample.CacheHit != "true" || evidence.Sample.Iteration != "warm-01" {
		t.Fatalf("unexpected sample: %#v", evidence.Sample)
	}
	if len(evidence.Jobs) != 5 || evidence.Summary.JobCount != 5 || evidence.Summary.UniqueRunnerNames != 5 || evidence.Summary.UniqueMachineIDHashes != 5 {
		t.Fatalf("unexpected evidence summary: %#v", evidence.Summary)
	}
	for index, job := range evidence.Jobs {
		if job.Workload != workloads[index] || job.Metrics.Workload != job.Workload || job.Artifact.Digest == "" {
			t.Fatalf("job evidence is not canonically ordered: %#v", job)
		}
		if job.QueueToStartMS <= 0 || job.JobDurationMS <= 0 || len(job.Steps) == 0 {
			t.Fatalf("job timing is incomplete: %#v", job)
		}
	}
}

func TestCollectorRejectsUntrustedOrIncoherentEvidence(t *testing.T) {
	tests := []struct {
		name    string
		options fixtureOptions
		want    string
	}{
		{name: "mixed cache result", options: fixtureOptions{mixedCacheHit: true}, want: "coherent sample"},
		{name: "digest mismatch", options: fixtureOptions{digestMismatch: true}, want: "digest mismatch"},
		{name: "extra archive entry", options: fixtureOptions{extraArchiveEntry: true}, want: "exactly one file"},
		{name: "reused machine", options: fixtureOptions{reuseMachineID: true}, want: "reused a machine identity"},
		{name: "reused runner", options: fixtureOptions{reuseRunnerName: true}, want: "reused a runner identity"},
		{name: "unknown record field", options: fixtureOptions{unknownRecordKey: true}, want: "unknown field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newFixtureServer(t, test.options)
			defer server.Close()
			_, err := (Collector{
				HTTPClient:            server.Client(),
				APIBaseURL:            server.URL,
				Token:                 "test-token",
				allowInsecureForTests: true,
			}).Collect(context.Background(), Options{Repository: "acme/repo", RunID: testRunID})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestCollectorRejectsAmbiguousInputs(t *testing.T) {
	collector := Collector{Token: "test-token"}
	for _, options := range []Options{
		{Repository: "acme", RunID: 1},
		{Repository: "../repo", RunID: 1},
		{Repository: "acme/repo", RunID: 0},
	} {
		if _, _, _, _, err := collector.validate(options); err == nil {
			t.Fatalf("accepted invalid options: %#v", options)
		}
	}
	for _, token := range []string{"", "has whitespace"} {
		collector.Token = token
		if _, _, _, _, err := collector.validate(Options{Repository: "acme/repo", RunID: 1}); err == nil {
			t.Fatalf("accepted invalid token %q", token)
		}
	}
}

func TestAPIPathEscapesIndividualSegmentsWithoutEscapingSeparators(t *testing.T) {
	got := apiPath("acme/repo", "actions", "runs", int64(12345), "artifacts")
	if got != "/repos/acme/repo/actions/runs/12345/artifacts" {
		t.Fatalf("API path = %q", got)
	}
}

func newFixtureServer(t *testing.T, options fixtureOptions) *httptest.Server {
	t.Helper()
	type storedArtifact struct {
		metadata artifactResponse
		archive  []byte
	}
	stored := make(map[int64]storedArtifact, len(workloads))
	jobs := make([]jobResponse, 0, len(workloads))
	artifacts := make([]artifactResponse, 0, len(workloads))
	for index, workload := range workloads {
		record := BenchmarkRecord{
			SchemaVersion:   1,
			Workload:        workload,
			Environment:     "nddev",
			CacheMode:       "warm",
			Iteration:       "warm-01",
			CacheHit:        "true",
			Toolchain:       workload + " toolchain",
			Commit:          testSHA,
			RunID:           testRunID,
			RunAttempt:      1,
			MachineIDSHA256: fmt.Sprintf("%064x", index+1),
			StartTimeNS:     1_000_000_000,
			FinishTimeNS:    2_000_000_000,
			ElapsedNS:       1_000_000_000,
			NetworkRXBytes:  int64(index + 1),
		}
		if options.mixedCacheHit && index == len(workloads)-1 {
			record.CacheHit = "false"
		}
		if options.reuseMachineID && index == len(workloads)-1 {
			record.MachineIDSHA256 = fmt.Sprintf("%064x", 1)
		}
		archive := recordArchive(t, record, options.extraArchiveEntry && index == 0, options.unknownRecordKey && index == 0)
		digest := sha256.Sum256(archive)
		artifact := artifactResponse{
			ID:          int64(100 + index),
			Name:        fmt.Sprintf("benchmark-nddev-warm-warm-01-%s", workload),
			SizeInBytes: int64(len(archive)),
			Digest:      "sha256:" + hex.EncodeToString(digest[:]),
			CreatedAt:   testCreatedAt.Add(11 * time.Minute),
			ExpiresAt:   testCreatedAt.Add(24 * time.Hour),
		}
		artifact.WorkflowRun.ID = testRunID
		artifact.WorkflowRun.HeadSHA = testSHA
		if options.digestMismatch && index == 0 {
			artifact.Digest = "sha256:" + strings.Repeat("0", 64)
		}
		stored[artifact.ID] = storedArtifact{metadata: artifact, archive: archive}
		artifacts = append(artifacts, artifact)

		started := testCreatedAt.Add(time.Duration(index+1) * time.Minute)
		runnerName := fmt.Sprintf("nddev-runner-%d", index+1)
		if options.reuseRunnerName && index == len(workloads)-1 {
			runnerName = "nddev-runner-1"
		}
		jobs = append(jobs, jobResponse{
			ID:          int64(200 + index),
			Name:        workloadJobNames[workload],
			Status:      "completed",
			Conclusion:  "success",
			RunnerName:  runnerName,
			Labels:      []string{expectedRunnerLabel("nddev", workload)},
			StartedAt:   started,
			CompletedAt: started.Add(time.Minute),
			Steps:       successfulSteps(started, "warm"),
		})
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/blob/") {
			if request.Header.Get("Authorization") != "" {
				http.Error(writer, "authorization leaked to artifact storage", http.StatusBadRequest)
				return
			}
			id, err := parseTrailingID(request.URL.Path)
			if err != nil {
				http.NotFound(writer, request)
				return
			}
			artifact, exists := stored[id]
			if !exists {
				http.NotFound(writer, request)
				return
			}
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(artifact.archive)
			return
		}
		if request.Header.Get("Authorization") != "Bearer test-token" || request.Header.Get("X-GitHub-Api-Version") != githubAPIVersion {
			http.Error(writer, "missing API authentication contract", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/repos/acme/repo/actions/runs/12345":
			response := runResponse{
				ID:         testRunID,
				Name:       workflowName,
				Path:       workflowPath,
				Event:      "workflow_dispatch",
				Status:     "completed",
				Conclusion: "success",
				HeadSHA:    testSHA,
				RunAttempt: 1,
				CreatedAt:  testCreatedAt,
				UpdatedAt:  testCreatedAt.Add(10 * time.Minute),
				HTMLURL:    "https://github.com/acme/repo/actions/runs/12345",
			}
			response.Repository.FullName = "acme/repo"
			writeFixtureJSON(t, writer, response)
		case "/repos/acme/repo/actions/runs/12345/jobs":
			writeFixtureJSON(t, writer, jobsResponse{TotalCount: len(jobs), Jobs: jobs})
		case "/repos/acme/repo/actions/runs/12345/artifacts":
			writeFixtureJSON(t, writer, artifactsResponse{TotalCount: len(artifacts), Artifacts: artifacts})
		default:
			id, err := parseArtifactDownloadID(request.URL.Path)
			if err != nil {
				http.NotFound(writer, request)
				return
			}
			if _, exists := stored[id]; !exists {
				http.NotFound(writer, request)
				return
			}
			writer.Header().Set("Location", fmt.Sprintf("%s/blob/%d", server.URL, id))
			writer.WriteHeader(http.StatusFound)
		}
	}))
	return server
}

func successfulSteps(start time.Time, cacheMode string) []stepResponse {
	names := append([]string{"Set up job"}, requiredPhases...)
	names = append(names, "Complete job")
	steps := make([]stepResponse, 0, len(names))
	for index, name := range names {
		conclusion := "success"
		if cacheMode == "cold" && name == "Restore dependency cache" {
			conclusion = "skipped"
		}
		stepStart := start.Add(time.Duration(index) * time.Second)
		steps = append(steps, stepResponse{
			Number:      index + 1,
			Name:        name,
			Status:      "completed",
			Conclusion:  conclusion,
			StartedAt:   stepStart,
			CompletedAt: stepStart.Add(time.Second),
		})
	}
	return steps
}

func recordArchive(t *testing.T, record BenchmarkRecord, extraEntry, unknownKey bool) []byte {
	t.Helper()
	recordJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if unknownKey {
		var object map[string]any
		if err := json.Unmarshal(recordJSON, &object); err != nil {
			t.Fatal(err)
		}
		object["unexpected"] = true
		recordJSON, err = json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	entry, err := archive.Create("result.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(recordJSON); err != nil {
		t.Fatal(err)
	}
	if extraEntry {
		extra, err := archive.Create("extra.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := extra.Write([]byte("unexpected")); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func parseArtifactDownloadID(rawPath string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(rawPath, "/repos/acme/repo/actions/artifacts/%d/zip", &id); err != nil {
		return 0, err
	}
	return id, nil
}

func parseTrailingID(rawPath string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(rawPath, "/blob/%d", &id); err != nil {
		return 0, err
	}
	return id, nil
}

func writeFixtureJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}
