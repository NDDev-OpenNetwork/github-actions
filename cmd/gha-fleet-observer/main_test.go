package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseOptionsPinsLoopbackAddress(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseOptions(nil, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if options.listen != expectedListenAddress || options.platformConfig == "" || options.providerConfig == "" {
		t.Fatalf("unexpected defaults: %#v", options)
	}

	_, err = parseOptions([]string{"--listen", "0.0.0.0:9464"}, &stderr)
	if err == nil || !strings.Contains(err.Error(), "must remain 127.0.0.1:9464") {
		t.Fatalf("unsafe listen error = %v", err)
	}
	_, err = parseOptions([]string{"positional"}, &stderr)
	if err == nil || !strings.Contains(err.Error(), "positional") {
		t.Fatalf("positional error = %v", err)
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("version code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "gha-fleet-observer") {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestServiceStateRejectsUnboundedName(t *testing.T) {
	_, err := serviceState(t.Context(), "ssh")
	if err == nil || !strings.Contains(err.Error(), "outside the fixed observer inventory") {
		t.Fatalf("service error = %v", err)
	}
}

func TestSystemdUnitNamePreservesExplicitTimer(t *testing.T) {
	for input, expected := range map[string]string{
		"garm":                "garm.service",
		"gha-warm-pool.timer": "gha-warm-pool.timer",
		"explicit.service":    "explicit.service",
	} {
		if actual := systemdUnitName(input); actual != expected {
			t.Errorf("systemdUnitName(%q) = %q, want %q", input, actual, expected)
		}
	}
}
