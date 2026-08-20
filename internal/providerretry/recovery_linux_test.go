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
	dry, err := RecoverTerminal(context.Background(), journalPath, lockPath, key, "provider", updatedAt, false)
	if err != nil || dry.Applied || dry.Generation != 9 {
		t.Fatalf("dry recovery=%#v err=%v", dry, err)
	}
	applied, err := RecoverTerminal(context.Background(), journalPath, lockPath, key, "provider", updatedAt, true)
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
	if _, err := RecoverTerminal(context.Background(), journalPath, lockPath, key, "provider", updatedAt, true); err == nil {
		t.Fatal("non-terminal record was recoverable")
	}
}
