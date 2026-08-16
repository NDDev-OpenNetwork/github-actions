package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

type diagnosticSourceStub struct {
	files             map[string][]byte
	consoleReadCalled *bool
}

func (d diagnosticSourceStub) GetInstanceConsoleLog(string, *incus.InstanceConsoleLogArgs) (io.ReadCloser, error) {
	if d.consoleReadCalled != nil {
		*d.consoleReadCalled = true
	}
	return io.NopCloser(strings.NewReader("console token=secret-value\n")), nil
}

func (d diagnosticSourceStub) GetInstanceLogfiles(string) ([]string, error) {
	return []string{"qemu.log", "../escape.log", "metadata.txt"}, nil
}

func (d diagnosticSourceStub) GetInstanceLogfile(_ string, filename string) (io.ReadCloser, error) {
	if filename != "qemu.log" {
		return nil, errors.New("unexpected instance log")
	}
	return io.NopCloser(strings.NewReader("qemu ok\n")), nil
}

func (d diagnosticSourceStub) GetInstanceFile(_ string, filename string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	if filename == runnerDiagnosticDirectory {
		return io.NopCloser(bytes.NewReader(nil)), &incus.InstanceFileResponse{
			Type: "directory", Entries: []string{"Worker_1.log", ".credentials", "../escape.log"},
		}, nil
	}
	content, exists := d.files[filename]
	if !exists {
		return nil, nil, errors.New("missing fixture")
	}
	return io.NopCloser(bytes.NewReader(content)), &incus.InstanceFileResponse{Type: "file", Mode: 0o600}, nil
}

func TestCollectDiagnosticArtifactsUsesOnlyAllowlistedSources(t *testing.T) {
	source := diagnosticSourceStub{files: map[string][]byte{
		"/var/log/cloud-init.log":                        []byte("cloud init\n"),
		"/var/log/cloud-init-output.log":                 []byte("cloud output\n"),
		"/home/runner/actions-runner/_diag/Worker_1.log": []byte("worker\n"),
	}}
	artifacts, failures := collectDiagnosticArtifacts(
		context.Background(), source, "runner-test", api.InstanceTypeContainer,
	)
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %#v", failures)
	}
	paths := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		paths = append(paths, artifact.Path)
	}
	expected := []string{
		"console.log",
		"incus/qemu.log",
		"guest/cloud-init.log",
		"guest/cloud-init-output.log",
		"runner/Worker_1.log",
	}
	if strings.Join(paths, "|") != strings.Join(expected, "|") {
		t.Fatalf("artifact paths = %#v, want %#v", paths, expected)
	}
	for _, artifact := range artifacts {
		if strings.Contains(artifact.Path, "credentials") || strings.Contains(artifact.Path, "escape") {
			t.Fatalf("unsafe artifact admitted: %#v", artifact)
		}
	}
}

func TestCollectDiagnosticArtifactsSkipsContainerConsoleAPIForVM(t *testing.T) {
	consoleReadCalled := false
	source := diagnosticSourceStub{
		consoleReadCalled: &consoleReadCalled,
		files: map[string][]byte{
			"/var/log/cloud-init.log":                        []byte("cloud init\n"),
			"/var/log/cloud-init-output.log":                 []byte("cloud output\n"),
			"/home/runner/actions-runner/_diag/Worker_1.log": []byte("worker\n"),
		},
	}
	artifacts, failures := collectDiagnosticArtifacts(
		context.Background(), source, "runner-vm", api.InstanceTypeVM,
	)
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %#v", failures)
	}
	if consoleReadCalled {
		t.Fatal("container console API was called for a virtual machine")
	}
	for _, artifact := range artifacts {
		if artifact.Path == "console.log" {
			t.Fatal("container console artifact was emitted for a virtual machine")
		}
	}
}

func TestReadBoundedMarksTruncation(t *testing.T) {
	content, truncated, err := readBounded(strings.NewReader("abcdef"), 3)
	if err != nil || !truncated || string(content) != "abc" {
		t.Fatalf("readBounded = %q, %v, %v", content, truncated, err)
	}
}

func TestSafeLogFilename(t *testing.T) {
	for _, value := range []string{"Runner_1.log", "qemu.LOG"} {
		if !safeLogFilename(value) {
			t.Fatalf("safe filename rejected: %q", value)
		}
	}
	for _, value := range []string{"", "../escape.log", "nested/file.log", ".credentials", "line\n.log"} {
		if safeLogFilename(value) {
			t.Fatalf("unsafe filename accepted: %q", value)
		}
	}
}
