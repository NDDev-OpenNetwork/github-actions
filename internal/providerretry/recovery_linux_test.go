//go:build linux

package providerretry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoverTerminalUsesExactCASAndGeneration(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	lockPath := filepath.Join(directory, "create-retries.lock")
	updatedAt := time.Now().UTC().Add(-time.Hour)
	key := "scale-set:org-id:1"
	state := journal{SchemaVersion: 1, Generation: 9, UpdatedAt: updatedAt, Records: map[string]record{key: {
		JobID: key, Attempts: 8, LastErrorClass: "provider", UpdatedAt: updatedAt,
		NextAllowedAt: updatedAt, TerminalUntil: updatedAt.Add(24 * time.Hour),
	}}}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	dry, err := RecoverTerminal(context.Background(), journalPath, lockPath, key, "org-id", 1, "provider", updatedAt, false)
	if err != nil || dry.Applied || dry.Generation != 9 {
		t.Fatalf("dry recovery=%#v err=%v", dry, err)
	}
	applied, err := RecoverTerminal(context.Background(), journalPath, lockPath, key, "org-id", 1, "provider", updatedAt, true)
	if err != nil || !applied.Applied || applied.Generation != 10 {
		t.Fatalf("applied recovery=%#v err=%v", applied, err)
	}
	observed, err := readJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := observed.Records[key]; exists || observed.Generation != 10 {
		t.Fatalf("recovered journal=%#v", observed)
	}
}

func TestRecoverTerminalRejectsNonTerminalOrChangedRecord(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	lockPath := filepath.Join(directory, "create-retries.lock")
	updatedAt := time.Now().UTC().Add(-time.Hour)
	key := "scale-set:org-id:1"
	state := journal{SchemaVersion: 1, Records: map[string]record{key: {
		JobID: key, Attempts: 7, LastErrorClass: "provider", UpdatedAt: updatedAt,
		NextAllowedAt: updatedAt.Add(time.Minute),
	}}}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverTerminal(context.Background(), journalPath, lockPath, key, "org-id", 1, "provider", updatedAt, true); err == nil {
		t.Fatal("non-terminal record was recoverable")
	}
}

func TestRecoverTerminalRejectsWrongTenantOrConcreteRetryKey(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	lockPath := filepath.Join(directory, "create-retries.lock")
	updatedAt := time.Now().UTC().Add(-time.Hour)
	key := "scale-set:account-a:17"
	state := journal{SchemaVersion: 1, Records: map[string]record{key: {
		JobID: key, Attempts: 8, LastErrorClass: "identity", UpdatedAt: updatedAt,
		NextAllowedAt: updatedAt, TerminalUntil: updatedAt.Add(24 * time.Hour),
	}}}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		key        string
		entityID   string
		scaleSetID uint
	}{
		{name: "wrong account", key: key, entityID: "account-b", scaleSetID: 17},
		{name: "wrong scale set", key: key, entityID: "account-a", scaleSetID: 18},
		{name: "concrete instance", key: key + ":instance:runner-one", entityID: "account-a", scaleSetID: 17},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RecoverTerminal(context.Background(), journalPath, lockPath, test.key, test.entityID, test.scaleSetID, "identity", updatedAt, false); err == nil {
				t.Fatal("mismatched recovery identity was accepted")
			}
		})
	}
}
