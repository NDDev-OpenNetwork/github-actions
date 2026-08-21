package queueintent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// jsonFieldNames is the wire shape of a type, in declaration order.
func jsonFieldNames(t *testing.T, value any) []string {
	t.Helper()
	structType := reflect.TypeOf(value)
	names := make([]string, 0, structType.NumField())
	for index := range structType.NumField() {
		tag := structType.Field(index).Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Fatalf("%s.%s has no json tag; the wire shape must be explicit",
				structType.Name(), structType.Field(index).Name)
		}
		name, _, _ := strings.Cut(tag, ",")
		names = append(names, name)
	}
	return names
}

// GARM writes this journal and the provider and observer read it. The reader
// refuses unknown fields and accepts exactly one schema version, so a field
// added on the writing side is not a compatible extension: every older reader
// rejects the whole document. That is not hypothetical -- #220 records both
// serving hosts reporting `decode queue-intent journal: json: unknown field
// "owner"` while GARM was creating and running workers, which made the
// fail-closed observer red and its queue and instance snapshot unreliable
// during live work.
//
// This test pins the wire shape so the next field cannot be added silently. If
// it fails, the change is a schema change: increment the version on both sides,
// decide how a reader of the previous version is meant to behave, and record
// that decision -- internal/providerjournal already carries a worked example of
// a version ladder that upgrades in memory.
func TestQueueIntentWireShapeIsPinnedToItsSchemaVersion(t *testing.T) {
	if SchemaVersion != 3 {
		t.Fatalf("SchemaVersion = %d; update the golden field sets below with it", SchemaVersion)
	}
	for _, testCase := range []struct {
		name   string
		value  any
		wanted []string
	}{
		{
			name:  "Journal",
			value: Journal{},
			wanted: []string{
				"schema_version", "generation", "updated_at", "intents", "repositories",
			},
		},
		{
			name:  "Intent",
			value: Intent{},
			wanted: []string{
				"key", "scale_set_id", "job_id", "runner_request_id", "scale_set_name",
				"runner_name", "owner", "repository", "workflow_ref", "event_name", "queue_time",
				"state", "priority", "state_entered_at", "updated_at", "expires_at",
			},
		},
		{
			name:   "RepositoryState",
			value:  RepositoryState{},
			wanted: []string{"repository", "weight", "pass"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := jsonFieldNames(t, testCase.value); !reflect.DeepEqual(got, testCase.wanted) {
				t.Fatalf("%s wire shape changed at schema version %d\n got: %v\nwant: %v",
					testCase.name, SchemaVersion, got, testCase.wanted)
			}
		})
	}
}

// The cost of the strict decoder, stated as behaviour rather than left to be
// rediscovered on a host. A reader one field behind the writer does not degrade:
// it refuses the entire journal, including every intent it does understand.
func TestUnknownFieldRejectsTheWholeJournalRatherThanOneIntent(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "queue-intents.json")
	now := time.Now().UTC().Truncate(time.Second)
	journal := map[string]any{
		"schema_version": SchemaVersion,
		"generation":     uint64(7),
		"updated_at":     now,
		"intents": map[string]any{
			"github-scale-set-job:v2:11:job-1": map[string]any{
				"key":               "github-scale-set-job:v2:11:job-1",
				"scale_set_id":      11,
				"job_id":            "job-1",
				"runner_request_id": 3,
				"scale_set_name":    "nddev-linux-standard",
				"repository":        "example-org/example-actions",
				"workflow_ref":      "example-org/example-actions/.github/workflows/ci.yml@refs/heads/main",
				"event_name":        "push",
				"queue_time":        now,
				"state":             string(StateRunning),
				"priority":          1,
				"state_entered_at":  now,
				"updated_at":        now,
				"expires_at":        now.Add(time.Hour),
				// One field this reader has never heard of, exactly as `owner`
				// arrived on the deployed hosts.
				"tenant_generation": 2,
			},
		},
		"repositories": map[string]any{
			"example-org/example-actions": map[string]any{
				"repository": "example-org/example-actions", "weight": 1, "pass": 0,
			},
		},
	}
	raw, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = Reader{Path: path}.ReadActive(context.Background())
	if err == nil {
		t.Fatal("an unknown field was accepted; the strict decoder no longer holds and this test must be restated")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want the unknown-field rejection this test characterises", err)
	}

	// The same journal without the unknown field is fully readable, which is
	// what makes the rejection above a compatibility property rather than a
	// malformed fixture.
	delete(journal["intents"].(map[string]any)["github-scale-set-job:v2:11:job-1"].(map[string]any), "tenant_generation")
	raw, err = json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Reader{Path: path, Now: func() time.Time { return now }}.ReadActive(context.Background())
	if err != nil {
		t.Fatalf("the same journal without the unknown field must decode: %v", err)
	}
	if len(snapshot.Active) != 1 || snapshot.Generation != 7 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}
