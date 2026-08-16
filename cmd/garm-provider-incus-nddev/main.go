// SPDX-License-Identifier: Apache-2.0
// Copyright 2023 Cloudbase Solutions SRL
// Modified by NDDev in 2026 for the hardened NDDev fleet provider.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/provider"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerrelease"
	"github.com/cloudbase/garm-provider-common/execution"
	commonExecution "github.com/cloudbase/garm-provider-common/execution/common"
)

var (
	// Not a release version, deliberately. A build without the release stamp
	// must refuse every real platform policy rather than claim to be the release
	// it was built beside; issue #263 records two hosts that ran different
	// binaries because a version literal did not move when the code did.
	version      = providerrelease.DevelopmentVersion
	commit       = "unknown"
	signals      = []os.Signal{os.Interrupt, syscall.SIGTERM}
	effectiveUID = os.Geteuid
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runExternalProvider(stdout, stderr)
	}
	switch args[0] {
	case "version":
		return writeJSON(stdout, stderr, map[string]string{
			"version":           version,
			"commit":            commit,
			"incus_sdk_version": provider.IncusSDKVersion,
		})
	case "probe":
		return runProbe(args[1:], stdout, stderr)
	case "warm-pool":
		return runWarmPool(args[1:], stdout, stderr)
	case "warm-drain":
		return runWarmDrain(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "garm-provider-incus-nddev: unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

// refuseRoot keeps every operator command on the same identity the production
// provider runs as. The cache credential loader derives the expected owner of
// /etc/garm/cache from the running process, so the same binary invoked under
// sudo demands root:root while the deployment ships root:garm and the command
// fails for a reason that has nothing to do with what it was asked to check.
// warm-drain has refused root since it was written; probe and warm-pool ran
// the same credential path without the guard.
func refuseRoot(command string, stderr io.Writer) error {
	if effectiveUID() != 0 {
		return nil
	}
	err := fmt.Errorf("%s refuses effective UID 0; run it as the garm service account", command)
	fmt.Fprintf(stderr, "garm-provider-incus-nddev: %v\n", err)
	return err
}

func runWarmDrain(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("warm-drain", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configFile := flags.String("config", "/etc/garm/provider-incus.toml", "provider configuration path")
	flavor := flags.String("flavor", "nddev-linux-standard", "exact warm-pool flavor")
	controllerID := flags.String("controller-id", "", "exact production GARM controller ID")
	instance := flags.String("instance", "", "exact unassigned warm instance name")
	apply := flags.Bool("apply", false, "capture diagnostics and destroy the exact warm instance")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *controllerID == "" || *instance == "" {
		fmt.Fprintln(stderr, "garm-provider-incus-nddev: warm-drain requires --controller-id and --instance and accepts no positional arguments")
		return 2
	}
	if err := refuseRoot("warm-drain", stderr); err != nil {
		return 1
	}
	provider.Version = version
	provider.Commit = commit
	incusProvider, err := provider.NewIncusProvider(*configFile, *controllerID)
	if err != nil {
		fmt.Fprintf(stderr, "garm-provider-incus-nddev: initialize warm drain: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := incusProvider.DrainWarm(ctx, *flavor, *instance, *apply)
	if err != nil {
		fmt.Fprintf(stderr, "garm-provider-incus-nddev: drain warm instance: %v\n", err)
		return 1
	}
	return writeJSON(stdout, stderr, result)
}

func runWarmPool(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("warm-pool", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configFile := flags.String("config", "/etc/garm/provider-incus.toml", "provider configuration path")
	flavor := flags.String("flavor", "nddev-linux-standard", "exact warm-pool flavor")
	controllerID := flags.String("controller-id", "", "exact production GARM controller ID")
	apply := flags.Bool("apply", false, "converge Incus to the configured unregistered warm target")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *controllerID == "" {
		fmt.Fprintln(stderr, "garm-provider-incus-nddev: warm-pool requires --controller-id and accepts no positional arguments")
		return 2
	}
	if err := refuseRoot("warm-pool", stderr); err != nil {
		return 1
	}
	provider.Version = version
	provider.Commit = commit
	incusProvider, err := provider.NewIncusProvider(*configFile, *controllerID)
	if err != nil {
		fmt.Fprintf(stderr, "garm-provider-incus-nddev: initialize warm pool: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := incusProvider.ReconcileWarm(ctx, *flavor, *apply)
	if err != nil {
		fmt.Fprintf(stderr, "garm-provider-incus-nddev: reconcile warm pool: %v\n", err)
		return 1
	}
	return writeJSON(stdout, stderr, result)
}

func runExternalProvider(stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), signals...)
	defer stop()

	executionEnv, err := execution.GetEnvironment()
	if err != nil {
		fmt.Fprintf(stderr, "garm-provider-incus-nddev: %v\n", err)
		return 1
	}

	provider.Version = version
	provider.Commit = commit
	incusProvider, err := provider.NewIncusProvider(executionEnv.ProviderConfigFile, executionEnv.ControllerID)
	if err != nil {
		fmt.Fprintf(stderr, "garm-provider-incus-nddev: %v\n", err)
		return 1
	}

	result, err := executionEnv.Run(ctx, incusProvider)
	if err != nil {
		fmt.Fprintf(stderr, "failed to run command: %s\n", err)
		return commonExecution.ResolveErrorToExitCode(err)
	}
	if len(result) > 0 {
		_, _ = io.WriteString(stdout, result)
	}
	return 0
}

func runProbe(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configFile := flags.String("config", "/etc/garm/provider-incus.toml", "provider configuration path")
	profile := flags.String("profile", "nddev-linux-standard", "pilot Incus profile")
	controllerID := flags.String("controller-id", "nddev-read-only-compatibility-probe", "non-production probe controller ID")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "garm-provider-incus-nddev: probe accepts no positional arguments")
		return 2
	}
	if err := refuseRoot("probe", stderr); err != nil {
		return 1
	}

	provider.Version = version
	provider.Commit = commit
	incusProvider, err := provider.NewIncusProvider(*configFile, *controllerID)
	if err != nil {
		fmt.Fprintf(stderr, "garm-provider-incus-nddev: initialize probe: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := incusProvider.Probe(ctx, *profile)
	if err != nil {
		fmt.Fprintf(stderr, "garm-provider-incus-nddev: compatibility probe: %v\n", err)
		return 1
	}
	return writeJSON(stdout, stderr, result)
}

func writeJSON(stdout, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(stderr, "garm-provider-incus-nddev: encode output: %v\n", err)
		return 1
	}
	return 0
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: garm-provider-incus-nddev [version|probe [flags]|warm-pool [flags]|warm-drain [flags]]")
}
