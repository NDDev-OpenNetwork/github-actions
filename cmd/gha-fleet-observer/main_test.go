package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/fleetobserve"
)

func TestServicesHostOwnsDiagnosticExporterSource(t *testing.T) {
	services, err := config.Load("../../config/example-services.yaml")
	if err != nil {
		t.Fatal(err)
	}
	compute, err := config.Load("../../config/example-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !platformOwnsDiagnosticExporter(services) || platformOwnsDiagnosticExporter(compute) {
		t.Fatalf("diagnostic exporter ownership inverted: services=%t compute=%t",
			platformOwnsDiagnosticExporter(services), platformOwnsDiagnosticExporter(compute))
	}
}

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
	services, err := config.Load("../../config/example-services.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = serviceStateFor(services)(t.Context(), "ssh")
	if err == nil || !strings.Contains(err.Error(), "outside the fixed observer inventory") {
		t.Fatalf("service error = %v", err)
	}
}

// The probe admits exactly the inventory the health is computed from: the
// services-host units, its reconcilers, and the warm-pool pair of every pool
// with a warm depth -- and nothing else. A fixed list here read every newly
// demanded unit as down on 2026-09-02.
func TestServicesRoleInventoryFollowsTheConfig(t *testing.T) {
	services, err := config.Load("../../config/example-services.yaml")
	if err != nil {
		t.Fatal(err)
	}
	services.Pools[0].Warm.TargetReady = 1
	services.Pools[0].Warm.MaxReady = 1
	allowed := fleetobserve.ServiceNamesFor(services)
	for _, name := range []string{
		"gha-cache-broker", "gha-services-rustfs-route.timer", "gha-state-backup.timer",
		"gha-warm-pool@" + services.Pools[0].Name + ".service", "gha-warm-pool@" + services.Pools[0].Name + ".timer",
	} {
		if !serviceNameAllowed(name, allowed) {
			t.Fatalf("services inventory omits %q", name)
		}
	}
	for _, name := range []string{"ssh", "gha-warm-pool@" + services.Pools[1].Name + ".service", "gha-zot"} {
		if serviceNameAllowed(name, allowed) {
			t.Fatalf("services inventory admits %q", name)
		}
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
