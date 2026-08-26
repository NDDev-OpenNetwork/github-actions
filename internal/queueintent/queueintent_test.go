package queueintent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReaderReturnsOnlyActiveIntentsInDeterministicOrder(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	path := writeFixture(t, `{
  "schema_version": 1,
  "generation": 7,
  "updated_at": "2026-08-09T13:59:00Z",
  "intents": {
    "github-scale-set-job:v2:11:11111111-1111-4111-8111-111111111111": {
      "key": "github-scale-set-job:v2:11:11111111-1111-4111-8111-111111111111", "scale_set_id": 11,
      "job_id": "11111111-1111-4111-8111-111111111111", "runner_request_id": 101,
      "scale_set_name": "nddev-linux-standard", "repository": "owner/standard",
      "workflow_ref": "owner/standard/.github/workflows/ci.yml@refs/heads/main",
      "event_name": "push", "queue_time": "2026-08-09T13:58:00Z",
      "state": "queued", "priority": 1, "updated_at": "2026-08-09T13:59:00Z",
      "expires_at": "2026-08-09T14:10:00Z"
    },
    "github-scale-set-job:v2:12:22222222-2222-4222-8222-222222222222": {
      "key": "github-scale-set-job:v2:12:22222222-2222-4222-8222-222222222222", "scale_set_id": 12,
      "job_id": "22222222-2222-4222-8222-222222222222", "runner_request_id": 102,
      "scale_set_name": "nddev-linux-release", "repository": "owner/release",
      "workflow_ref": "owner/release/.github/workflows/release.yml@refs/tags/v1",
      "event_name": "push", "queue_time": "2026-08-09T13:59:00Z",
      "state": "acquired", "priority": 0, "updated_at": "2026-08-09T13:59:00Z",
      "expires_at": "2026-08-09T14:10:00Z"
    },
    "github-scale-set-job:v2:13:33333333-3333-4333-8333-333333333333": {
      "key": "github-scale-set-job:v2:13:33333333-3333-4333-8333-333333333333", "scale_set_id": 13,
      "job_id": "33333333-3333-4333-8333-333333333333", "runner_request_id": 103,
      "scale_set_name": "nddev-linux-integration", "repository": "owner/expired",
      "workflow_ref": "workflow", "event_name": "pull_request",
      "queue_time": "2026-08-09T13:50:00Z", "state": "queued", "priority": 2,
      "updated_at": "2026-08-09T13:50:00Z", "expires_at": "2026-08-09T13:59:59Z"
    }
  },
  "terminal_jobs": {},
  "repositories": {
    "owner/standard": {"repository": "owner/standard", "weight": 1, "pass": 10},
    "owner/release": {"repository": "owner/release", "weight": 2, "pass": 5}
  }
}`)
	reader := Reader{Path: path, Now: func() time.Time { return now }}
	snapshot, err := reader.ReadActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Generation != 7 || snapshot.Stored != 3 || snapshot.Expired != 1 || len(snapshot.Active) != 2 ||
		snapshot.Active[0].Key != "github-scale-set-job:v2:12:22222222-2222-4222-8222-222222222222" ||
		snapshot.Active[1].Key != "github-scale-set-job:v2:11:11111111-1111-4111-8111-111111111111" {
		t.Fatalf("unexpected active snapshot: %#v", snapshot)
	}
	active, err := reader.ActiveForScaleSet(context.Background(), "example-org", "nddev-linux-standard")
	if err != nil || active {
		t.Fatalf("standard intent = %t, %v", active, err)
	}
	active, err = reader.ActiveForScaleSet(context.Background(), "example-org", "nddev-linux-release")
	if err != nil || !active {
		t.Fatalf("release intent = %t, %v", active, err)
	}
	active, err = reader.ActiveForScaleSet(context.Background(), "example-org", "nddev-linux-integration")
	if err != nil || active {
		t.Fatalf("expired integration intent = %t, %v", active, err)
	}
}

func TestReaderRejectsFinalComponentSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "real-journal.json")
	body := `{"schema_version":1,"generation":0,"updated_at":"0001-01-01T00:00:00Z","intents":{},"repositories":{}}`
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "queue-intents.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (Reader{Path: link}).ReadActive(context.Background()); err == nil {
		t.Fatal("final-component symlink was accepted")
	}
}

func TestReaderAuthorizesAssignedUUIDWithoutRunnerRequestID(t *testing.T) {
	now := time.Date(2026, 8, 9, 15, 20, 46, 0, time.UTC)
	path := writeFixture(t, `{
  "schema_version": 1,
  "generation": 2,
  "updated_at": "2026-08-09T15:20:46Z",
  "intents": {
    "github-scale-set-job:v2:11:5c3077ba-3664-5824-b2cf-e22a31b25f43": {
      "key": "github-scale-set-job:v2:11:5c3077ba-3664-5824-b2cf-e22a31b25f43",
      "scale_set_id": 11, "job_id": "5c3077ba-3664-5824-b2cf-e22a31b25f43",
      "runner_request_id": 0, "scale_set_name": "nddev-linux-standard",
      "repository": "example-user/github-actions", "workflow_ref": "workflow@refs/heads/main",
      "event_name": "workflow_dispatch", "queue_time": "2026-08-09T15:20:45Z",
      "state": "assigned", "priority": 1, "updated_at": "2026-08-09T15:20:46Z",
      "expires_at": "2026-08-10T15:20:46Z"
    }
  },
  "terminal_jobs": {},
  "repositories": {
    "example-user/github-actions": {"repository": "example-user/github-actions", "weight": 1, "pass": 1000000}
  }
}`)
	reader := Reader{Path: path, Now: func() time.Time { return now }}
	authorized, err := reader.ActiveForScaleSet(context.Background(), "example-org", "nddev-linux-standard")
	if err != nil || !authorized {
		t.Fatalf("assigned live UUID authorized=%t err=%v", authorized, err)
	}
}

func TestReaderFailsClosedOnUnknownOrUnsafeJournal(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		mode    os.FileMode
		message string
	}{
		{name: "unknown field", body: `{"schema_version":1,"generation":0,"updated_at":"0001-01-01T00:00:00Z","intents":{},"repositories":{},"extra":true}`, mode: 0o600, message: "unknown field"},
		{name: "null maps", body: `{"schema_version":1,"generation":0,"updated_at":"0001-01-01T00:00:00Z","intents":null,"repositories":null}`, mode: 0o600, message: "must not be null"},
		{name: "public mode", body: `{"schema_version":1,"generation":0,"updated_at":"0001-01-01T00:00:00Z","intents":{},"repositories":{}}`, mode: 0o644, message: "private regular file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "queue-intents.json")
			if err := os.WriteFile(path, []byte(test.body), test.mode); err != nil {
				t.Fatal(err)
			}
			// WriteFile only requests a mode; the process umask masks it. A
			// worker runs the suite under umask 077, which would silently turn
			// this public fixture private and stop exercising the rejection.
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}
			_, err := (Reader{Path: path}).ReadActive(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want %q", err, test.message)
			}
		})
	}
}

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue-intents.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
