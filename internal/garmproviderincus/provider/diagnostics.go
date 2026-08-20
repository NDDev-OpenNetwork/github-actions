package provider

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	providerconfig "github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

const (
	maxDiagnosticReadBytes   = workerdiagnostics.DefaultMaxArtifactBytes
	maxInstanceLogFiles      = 8
	maxRunnerDiagnosticFiles = 20
)

var diagnosticCollectionTimeout = 15 * time.Second

var guestDiagnosticFiles = []struct {
	guestPath string
	bundle    string
}{
	{guestPath: "/var/log/cloud-init.log", bundle: "guest/cloud-init.log"},
	{guestPath: "/var/log/cloud-init-output.log", bundle: "guest/cloud-init-output.log"},
}

const runnerDiagnosticDirectory = "/home/runner/actions-runner/_diag"

type instanceDiagnosticCollector interface {
	Capture(context.Context, InstanceServerInterface, *api.InstanceFull) (workerdiagnostics.Result, error)
}

type diagnosticCaptureOutcome struct {
	result workerdiagnostics.Result
	err    error
}

type diagnosticClient interface {
	GetInstanceConsoleLog(string, *incus.InstanceConsoleLogArgs) (io.ReadCloser, error)
	GetInstanceLogfiles(string) ([]string, error)
	GetInstanceLogfile(string, string) (io.ReadCloser, error)
	GetInstanceFile(string, string) (io.ReadCloser, *incus.InstanceFileResponse, error)
}

type providerDiagnostics struct {
	store         workerdiagnostics.Store
	runnerVersion string
}

func newProviderDiagnostics(cfg *providerconfig.Incus, runnerVersion string) providerDiagnostics {
	return providerDiagnostics{
		store: workerdiagnostics.Store{
			Directory:      cfg.DiagnosticsDirectory,
			Retention:      time.Duration(cfg.DiagnosticsRetentionHours) * time.Hour,
			MaxBundleBytes: cfg.DiagnosticsMaxBundleBytes,
			MaxTotalBytes:  cfg.DiagnosticsMaxTotalBytes,
			MaxArtifacts:   workerdiagnostics.DefaultMaxArtifacts,
		},
		runnerVersion: runnerVersion,
	}
}

func (d providerDiagnostics) Capture(
	ctx context.Context,
	cli InstanceServerInterface,
	instance *api.InstanceFull,
) (workerdiagnostics.Result, error) {
	if instance == nil {
		return workerdiagnostics.Result{}, fmt.Errorf("diagnostic instance is required")
	}
	source, ok := cli.(diagnosticClient)
	if !ok {
		return workerdiagnostics.Result{}, fmt.Errorf("Incus client does not expose the diagnostic read API")
	}
	artifacts, collectionErrors := collectDiagnosticArtifacts(ctx, source, instance.Name, api.InstanceType(instance.Type))
	state := "unknown"
	if instance.State != nil && strings.TrimSpace(instance.State.Status) != "" {
		state = instance.State.Status
	}
	metadata := workerdiagnostics.Instance{
		Name:             instance.Name,
		Trust:            instance.ExpandedConfig[trustKey],
		ControllerID:     instance.ExpandedConfig[controllerIDKeyName],
		PoolID:           instance.ExpandedConfig[poolIDKey],
		PoolName:         instance.ExpandedConfig[flavorKey],
		ScaleSet:         instance.ExpandedConfig[scaleSetKey],
		Repository:       instance.ExpandedConfig[repositoryKey],
		ImageFingerprint: instance.ExpandedConfig[imageFingerprintKey],
		RunnerVersion:    d.runnerVersion,
		ProviderVersion:  instance.ExpandedConfig[providerVersionKey],
		ProviderCommit:   instance.ExpandedConfig[providerCommitKey],
		State:            state,
	}
	return d.store.Write(ctx, metadata, artifacts, collectionErrors)
}

func collectDiagnosticArtifacts(
	ctx context.Context,
	cli diagnosticClient,
	instanceName string,
	instanceType api.InstanceType,
) ([]workerdiagnostics.Artifact, []string) {
	artifacts := make([]workerdiagnostics.Artifact, 0, workerdiagnostics.DefaultMaxArtifacts)
	failures := make([]string, 0)
	appendReader := func(bundlePath, sourceName string, reader io.ReadCloser, err error) {
		if err != nil {
			failures = append(failures, fmt.Sprintf("read %s: %v", sourceName, err))
			return
		}
		if reader == nil {
			failures = append(failures, fmt.Sprintf("read %s: Incus returned no content", sourceName))
			return
		}
		content, truncated, readErr := readBounded(reader, maxDiagnosticReadBytes)
		if closeErr := reader.Close(); readErr == nil && closeErr != nil {
			readErr = closeErr
		}
		if readErr != nil {
			failures = append(failures, fmt.Sprintf("read %s: %v", sourceName, readErr))
			return
		}
		artifacts = append(artifacts, workerdiagnostics.Artifact{
			Path: bundlePath, Source: sourceName, Content: content, Truncated: truncated,
		})
	}

	if err := ctx.Err(); err != nil {
		return artifacts, append(failures, err.Error())
	}
	// Incus exposes GetInstanceConsoleLog for containers. Calling it for a
	// virtual machine returns "Instance is not container type" even though the
	// VM diagnostics that matter are available through GetInstanceLogfiles and
	// the guest agent. Do not turn that expected API boundary into a false
	// collection error for the full-VM-only fleet.
	if instanceType != api.InstanceTypeVM {
		console, err := cli.GetInstanceConsoleLog(instanceName, &incus.InstanceConsoleLogArgs{})
		appendReader("console.log", "Incus console log", console, err)
	}

	if err := ctx.Err(); err != nil {
		return artifacts, append(failures, err.Error())
	}
	logfiles, err := cli.GetInstanceLogfiles(instanceName)
	if err != nil {
		failures = append(failures, fmt.Sprintf("list Incus instance logs: %v", err))
	} else {
		sort.Strings(logfiles)
		accepted := 0
		for _, filename := range logfiles {
			if !safeLogFilename(filename) {
				continue
			}
			if accepted == maxInstanceLogFiles {
				failures = append(failures, "Incus instance log count exceeded the allowlisted limit")
				break
			}
			reader, readErr := cli.GetInstanceLogfile(instanceName, filename)
			appendReader(path.Join("incus", filename), "Incus instance log "+filename, reader, readErr)
			accepted++
		}
	}

	for _, guestFile := range guestDiagnosticFiles {
		if err := ctx.Err(); err != nil {
			return artifacts, append(failures, err.Error())
		}
		reader, response, readErr := cli.GetInstanceFile(instanceName, guestFile.guestPath)
		if readErr == nil && (response == nil || response.Type != "file") {
			if reader != nil {
				_ = reader.Close()
			}
			failures = append(failures, fmt.Sprintf("read guest file %s: not a regular file", guestFile.guestPath))
			continue
		}
		appendReader(guestFile.bundle, "guest file "+guestFile.guestPath, reader, readErr)
	}

	if err := ctx.Err(); err != nil {
		return artifacts, append(failures, err.Error())
	}
	directory, response, err := cli.GetInstanceFile(instanceName, runnerDiagnosticDirectory)
	if directory != nil {
		_ = directory.Close()
	}
	if err != nil {
		failures = append(failures, fmt.Sprintf("list runner diagnostics: %v", err))
	} else if response == nil || response.Type != "directory" {
		failures = append(failures, "list runner diagnostics: path is not a directory")
	} else {
		entries := append([]string(nil), response.Entries...)
		sort.Strings(entries)
		accepted := 0
		for _, filename := range entries {
			if !safeLogFilename(filename) {
				continue
			}
			if accepted == maxRunnerDiagnosticFiles {
				failures = append(failures, "runner diagnostic count exceeded the allowlisted limit")
				break
			}
			guestPath := path.Join(runnerDiagnosticDirectory, filename)
			reader, fileResponse, readErr := cli.GetInstanceFile(instanceName, guestPath)
			if readErr == nil && (fileResponse == nil || fileResponse.Type != "file") {
				if reader != nil {
					_ = reader.Close()
				}
				failures = append(failures, fmt.Sprintf("read runner diagnostic %s: not a regular file", filename))
				continue
			}
			appendReader(path.Join("runner", filename), "runner diagnostic "+filename, reader, readErr)
			accepted++
		}
	}
	return artifacts, failures
}

func readBounded(reader io.Reader, maximum int) ([]byte, bool, error) {
	content, err := io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
	if err != nil {
		return nil, false, err
	}
	if len(content) > maximum {
		return content[:maximum], true, nil
	}
	return content, false, nil
}

func safeLogFilename(value string) bool {
	return value != "" && len(value) <= 128 && path.Base(value) == value &&
		!strings.ContainsAny(value, "\\\r\n\x00") && strings.HasSuffix(strings.ToLower(value), ".log")
}
