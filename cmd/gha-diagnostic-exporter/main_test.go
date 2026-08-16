package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--version"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "gha-diagnostic-exporter") || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRejectsPositionalArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"unexpected"}, &stdout, &stderr); err == nil ||
		!strings.Contains(err.Error(), "positional") {
		t.Fatalf("run() error = %v", err)
	}
}
