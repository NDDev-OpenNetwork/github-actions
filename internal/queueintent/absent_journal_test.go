package queueintent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A host that has never admitted a job has no journal, because GARM writes it
// at the first admission. The observer must still report that fleet as healthy,
// otherwise the rollout deadlocks: a scale set may only be enabled once the
// observer is healthy, and nothing can run to create the journal until it is.
func TestAbsentJournalReadsAsEmpty(t *testing.T) {
	reader := Reader{Path: filepath.Join(t.TempDir(), "queue-intents.json")}

	snapshot, err := reader.ReadActive(context.Background())
	if err != nil {
		t.Fatalf("absent journal was an error: %v", err)
	}
	if snapshot.Stored != 0 || snapshot.Expired != 0 || len(snapshot.Active) != 0 || snapshot.Generation != 0 {
		t.Fatalf("absent journal produced %#v, want an empty snapshot", snapshot)
	}

	active, err := reader.HasActive(context.Background())
	if err != nil {
		t.Fatalf("HasActive on an absent journal was an error: %v", err)
	}
	if active {
		t.Error("an absent journal reported active intents")
	}

	claimed, err := reader.ActiveForScaleSet(context.Background(), "example-org", "nddev-linux-standard")
	if err != nil {
		t.Fatalf("ActiveForScaleSet on an absent journal was an error: %v", err)
	}
	if claimed {
		t.Error("an absent journal reported an active scale set intent")
	}
}

// Absence is the only tolerated read failure. A journal that exists but is
// readable by other accounts, or is not valid, must still fail closed —
// otherwise the tolerance above would quietly become "ignore a broken journal",
// which is how a real queue state disappears from the health signal.
func TestPresentButUnusableJournalStillFails(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		contents  string
		mode      os.FileMode
		wantError string
	}{
		{name: "group readable", contents: `{"schema_version":1,"intents":{},"repositories":{}}`, mode: 0o640, wantError: "private regular file"},
		{name: "malformed", contents: `{`, mode: 0o600, wantError: "decode queue-intent journal"},
		{name: "wrong schema", contents: `{"schema_version":3,"intents":{},"repositories":{}}`, mode: 0o600, wantError: "schema_version"},
		{name: "null maps", contents: `{"schema_version":1,"intents":null,"repositories":null}`, mode: 0o600, wantError: "must not be null"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "queue-intents.json")
			if err := os.WriteFile(path, []byte(testCase.contents), testCase.mode); err != nil {
				t.Fatal(err)
			}
			// A umask can only clear bits, so set the mode explicitly.
			if err := os.Chmod(path, testCase.mode); err != nil {
				t.Fatal(err)
			}
			_, err := (Reader{Path: path}).ReadActive(context.Background())
			if err == nil {
				t.Fatalf("%s journal was accepted", testCase.name)
			}
			if !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("error %q does not mention %q", err, testCase.wantError)
			}
		})
	}
}
