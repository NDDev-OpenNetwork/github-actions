package deploycontract

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
)

var overlaySchemaVersionPattern = regexp.MustCompile(`(?m)^\s*queueIntentSchemaVersion\s*=\s*(\d+)\s*$`)

// The queue-intent journal has one writer, GARM through the in-tree overlay, and
// two readers, the provider and the observer. The writer's version lives in the
// overlay as its own constant and the readers' version lives in
// internal/queueintent, so the contract between them is two integers in two
// files with nothing comparing them.
//
// #220 records what that costs when they drift: both serving hosts reported
// `decode queue-intent journal: json: unknown field "owner"` while GARM was
// creating and running workers, so the fail-closed observer went red and its
// queue and instance counts were unreliable during live work. The drift is
// invisible in review because each file reads correct on its own.
//
// The overlay is digest-bound in config/garm-derivative.yaml, so changing it
// means rebuilding the derivative. That makes this check cheap and the failure
// it prevents expensive, which is the right way round.
func TestQueueJournalWriterAndReaderDeclareTheSameSchemaVersion(t *testing.T) {
	path := filepath.Join("..", "..", "third_party", "garm", "overlay", "workers", "scaleset", "queue_intent.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	match := overlaySchemaVersionPattern.FindSubmatch(source)
	if match == nil {
		t.Fatalf("%s no longer declares queueIntentSchemaVersion; the writer's version must stay discoverable", path)
	}
	writer, err := strconv.Atoi(string(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	if writer != queueintent.SchemaVersion {
		t.Fatalf(
			"queue-intent journal writer declares schema %d and the reader accepts %d; "+
				"a journal written by one is rejected in full by the other",
			writer, queueintent.SchemaVersion,
		)
	}
}
