package main

import (
	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"

	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
	"github.com/NDDev-OpenNetwork/github-actions/internal/cachebroker"
	"github.com/NDDev-OpenNetwork/github-actions/internal/cachemanifest"
	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticexport"
	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticstore"
	"github.com/NDDev-OpenNetwork/github-actions/internal/fleetcontract"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmbootstrap"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmderivative"
	"github.com/NDDev-OpenNetwork/github-actions/internal/githubappbootstrap"
	"github.com/NDDev-OpenNetwork/github-actions/internal/hostcapacity"
	"github.com/NDDev-OpenNetwork/github-actions/internal/hostdeps"
	"github.com/NDDev-OpenNetwork/github-actions/internal/hostfirewall"
	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
	"github.com/NDDev-OpenNetwork/github-actions/internal/imagebuild"
	"github.com/NDDev-OpenNetwork/github-actions/internal/imagemanifest"
	"github.com/NDDev-OpenNetwork/github-actions/internal/imageplan"
	"github.com/NDDev-OpenNetwork/github-actions/internal/incusplan"
	"github.com/NDDev-OpenNetwork/github-actions/internal/incusreconcile"
	"github.com/NDDev-OpenNetwork/github-actions/internal/joblifecycle"
	"github.com/NDDev-OpenNetwork/github-actions/internal/memberdrain"
	"github.com/NDDev-OpenNetwork/github-actions/internal/observabilitydashboards"
	"github.com/NDDev-OpenNetwork/github-actions/internal/observabilityrules"
	"github.com/NDDev-OpenNetwork/github-actions/internal/pressuregate"
	"github.com/NDDev-OpenNetwork/github-actions/internal/pressurepublish"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerrelease"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerretry"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueadmission"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
	"github.com/NDDev-OpenNetwork/github-actions/internal/slabheal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/telemetrymanifest"
	"github.com/NDDev-OpenNetwork/github-actions/internal/zotcredentials"
	incusclient "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "render":
		return runRender(args[1:], stdout, stderr)
	case "admit":
		return runAdmit(args[1:], stdout, stderr)
	case "preflight":
		return runPreflight(args[1:], stdout, stderr)
	case "publish-pressure":
		return runPublishPressure(args[1:], stdout, stderr)
	case "slab-heal":
		return runSlabHeal(args[1:], stdout, stderr)
	case "drain-member":
		return runDrainMember(args[1:], stdout, stderr)
	case "reconcile-incus":
		return runReconcileIncus(args[1:], stdout, stderr)
	case "reconcile-image":
		return runReconcileImage(args[1:], stdout, stderr)
	case "validate-cache":
		return runValidateCache(args[1:], stdout, stderr)
	case "validate-cache-broker":
		return runValidateCacheBroker(args[1:], stdout, stderr)
	case "validate-telemetry":
		return runValidateTelemetry(args[1:], stdout, stderr)
	case "validate-rustfs-cache":
		return runValidateRustFSCache(args[1:], stdout, stderr)
	case "validate-diagnostic-exporter":
		return runValidateDiagnosticExporter(args[1:], stdout, stderr)
	case "validate-diagnostic-storage":
		return runValidateDiagnosticStorage(args[1:], stdout, stderr)
	case "validate-tenant-registry":
		return runValidateTenantRegistry(args[1:], stdout, stderr)
	case "validate-queue-admission":
		return runValidateQueueAdmission(args[1:], stdout, stderr)
	case "validate-observability-rules":
		return runValidateObservabilityRules(args[1:], stdout, stderr)
	case "validate-observability-dashboards":
		return runValidateObservabilityDashboards(args[1:], stdout, stderr)
	case "render-openobserve-dashboards":
		return runRenderOpenObserveDashboards(args[1:], stdout, stderr)
	case "reconcile-openobserve-dashboards":
		return runReconcileOpenObserveDashboards(args[1:], stdout, stderr)
	case "render-openobserve-alerts":
		return runRenderOpenObserveAlerts(args[1:], stdout, stderr)
	case "reconcile-openobserve-alerts":
		return runReconcileOpenObserveAlerts(args[1:], stdout, stderr)
	case "export-job-lifecycle":
		return runExportJobLifecycle(args[1:], stdout, stderr)
	case "reconcile-zot-credentials":
		return runReconcileZotCredentials(args[1:], stdout, stderr)
	case "reconcile-rustfs-cache":
		return runReconcileRustFSCache(args[1:], stdout, stderr)
	case "reconcile-diagnostic-storage":
		return runReconcileDiagnosticStorage(args[1:], stdout, stderr)
	case "bootstrap-github-app":
		return runBootstrapGitHubApp(args[1:], stdout, stderr)
	case "verify-github-app":
		return runVerifyGitHubApp(args[1:], stdout, stderr)
	case "reconcile-garm":
		return runReconcileGARM(args[1:], stdout, stderr)
	case "render-garm-build":
		return runRenderGARMBuild(args[1:], stdout, stderr)
	case "provider-release":
		return runProviderRelease(args[1:], stdout, stderr)
	case "fleet-contract":
		return runFleetContract(args[1:], stdout, stderr)
	case "capacity":
		return runCapacity(args[1:], stdout, stderr)
	case "recover-queue-intent":
		return runRecoverQueueIntent(args[1:], stdout, stderr, false)
	case "recover-canceled-queue-intent":
		return runRecoverQueueIntent(args[1:], stdout, stderr, true)
	case "recover-provider-retry":
		return runRecoverProviderRetry(args[1:], stdout, stderr)
	case "recover-provider-job-retry":
		return runRecoverProviderJobRetry(args[1:], stdout, stderr)
	case "version":
		if err := writeJSON(stdout, map[string]string{"version": version, "commit": commit}); err != nil {
			fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
			return 1
		}
		return 0
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "gha-fleet: unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runValidateDiagnosticExporter(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate-diagnostic-exporter", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "committed diagnostic exporter configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: validate-diagnostic-exporter requires --config")
		return 2
	}
	raw, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: read diagnostic exporter config: %v\n", err)
		return 1
	}
	config, err := diagnosticexport.ParseConfig(raw)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, config); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runValidateTenantRegistry(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate-tenant-registry", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config/tenant-registry.yaml", "strict tenant registry path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: validate-tenant-registry requires --config and no positional arguments")
		return 2
	}
	registry, err := tenant.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return writeJSONOrFail(stdout, stderr, registry)
}

func runValidateQueueAdmission(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate-queue-admission", flag.ContinueOnError)
	flags.SetOutput(stderr)
	queuePath := flags.String("config", "", "strict queue admission JSON path")
	platformPath := flags.String("platform-config", "", "fleet-member platform configuration")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *queuePath == "" || *platformPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: validate-queue-admission requires --config and --platform-config")
		return 2
	}
	queue, err := queueadmission.Load(*queuePath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	platform, err := config.Load(*platformPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: load platform config: %v\n", err)
		return 1
	}
	if err := queue.ValidateAgainstPlatform(platform); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return writeJSONOrFail(stdout, stderr, queue)
}

// overlayPaths turns the single optional --overlay flag value into the list
// LoadWithOverlays takes; empty means the base bundle alone.
func overlayPaths(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

func runValidateObservabilityRules(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate-observability-rules", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config/observability-rules.yaml", "strict observability rules bundle")
	overlayPath := flags.String("overlay", "", "optional estate overlay bundle merged into the base rules")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: validate-observability-rules requires --config")
		return 2
	}
	bundle, err := observabilityrules.LoadWithOverlays(*configPath, overlayPaths(*overlayPath))
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return writeJSONOrFail(stdout, stderr, bundle)
}

func runValidateObservabilityDashboards(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate-observability-dashboards", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config/observability-dashboards.yaml", "strict observability dashboard bundle")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: validate-observability-dashboards requires --config")
		return 2
	}
	bundle, err := observabilitydashboards.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return writeJSONOrFail(stdout, stderr, bundle)
}

func runRenderOpenObserveDashboards(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("render-openobserve-dashboards", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config/observability-dashboards.yaml", "strict observability dashboard bundle")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: render-openobserve-dashboards requires --config")
		return 2
	}
	bundle, err := observabilitydashboards.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	rendered, err := observabilitydashboards.Render(bundle)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(append(rendered, '\n')); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runReconcileOpenObserveDashboards(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile-openobserve-dashboards", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config/observability-dashboards.yaml", "strict observability dashboard bundle")
	folder := flags.String("folder", "default", "exact reviewed OpenObserve folder identity")
	endpoint := flags.String("endpoint", "", "OpenObserve HTTP(S) origin")
	usernameFile := flags.String("username-file", "", "absolute file containing only the OpenObserve username")
	passwordFile := flags.String("password-file", "", "absolute file containing only the OpenObserve password")
	apply := flags.Bool("apply", false, "apply the exact current plan and verify read-back")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" || *folder == "" || *endpoint == "" || *usernameFile == "" || *passwordFile == "" {
		fmt.Fprintln(stderr, "gha-fleet: reconcile-openobserve-dashboards requires --config, --folder, --endpoint, --username-file, and --password-file")
		return 2
	}
	bundle, err := observabilitydashboards.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	desired, err := observabilitydashboards.RenderOpenObserve(bundle)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	username, err := readOpenObserveCredential(*usernameFile)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: read OpenObserve username: %v\n", err)
		return 1
	}
	password, err := readOpenObserveCredential(*passwordFile)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: read OpenObserve password: %v\n", err)
		return 1
	}
	client, err := observabilitydashboards.NewOpenObserveClient(*endpoint, username, password)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var plan observabilitydashboards.ReconcilePlan
	if *apply {
		plan, err = client.Apply(ctx, bundle.Organization, *folder, desired)
	} else {
		plan, err = client.Plan(ctx, bundle.Organization, *folder, desired)
	}
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return writeJSONOrFail(stdout, stderr, plan)
}

func runRenderOpenObserveAlerts(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("render-openobserve-alerts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config/observability-rules.yaml", "strict observability rules bundle")
	overlayPath := flags.String("overlay", "", "optional estate overlay bundle merged into the base rules")
	destination := flags.String("destination", "fleet_oncall", "exact reviewed OpenObserve destination")
	enable := flags.Bool("enable", false, "render alerts enabled after the destination is independently accepted")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" || *destination == "" {
		fmt.Fprintln(stderr, "gha-fleet: render-openobserve-alerts requires --config and --destination")
		return 2
	}
	bundle, err := observabilityrules.LoadWithOverlays(*configPath, overlayPaths(*overlayPath))
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	rendered, err := observabilityrules.RenderOpenObserve(bundle, *destination, *enable)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return writeJSONOrFail(stdout, stderr, rendered)
}

func runReconcileOpenObserveAlerts(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile-openobserve-alerts", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config/observability-rules.yaml", "strict observability rules bundle")
	overlayPath := flags.String("overlay", "", "optional estate overlay bundle merged into the base rules")
	destination := flags.String("destination", "fleet_oncall", "exact reviewed OpenObserve destination")
	endpoint := flags.String("endpoint", "", "OpenObserve HTTP(S) origin")
	usernameFile := flags.String("username-file", "", "absolute file containing only the OpenObserve username")
	passwordFile := flags.String("password-file", "", "absolute file containing only the OpenObserve password")
	enable := flags.Bool("enable", false, "manage alerts enabled after destination acceptance")
	apply := flags.Bool("apply", false, "apply the exact current plan and verify read-back")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" || *destination == "" || *endpoint == "" || *usernameFile == "" || *passwordFile == "" {
		fmt.Fprintln(stderr, "gha-fleet: reconcile-openobserve-alerts requires --config, --destination, --endpoint, --username-file, and --password-file")
		return 2
	}
	bundle, err := observabilityrules.LoadWithOverlays(*configPath, overlayPaths(*overlayPath))
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	desired, err := observabilityrules.RenderOpenObserve(bundle, *destination, *enable)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	username, err := readOpenObserveCredential(*usernameFile)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: read OpenObserve username: %v\n", err)
		return 1
	}
	password, err := readOpenObserveCredential(*passwordFile)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: read OpenObserve password: %v\n", err)
		return 1
	}
	client, err := observabilityrules.NewOpenObserveClient(*endpoint, username, password)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var plan observabilityrules.ReconcilePlan
	if *apply {
		plan, err = client.Apply(ctx, desired)
	} else {
		plan, err = client.Plan(ctx, desired)
	}
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return writeJSONOrFail(stdout, stderr, plan)
}

func readOpenObserveCredential(filename string) (string, error) {
	if !filepath.IsAbs(filename) {
		return "", errors.New("credential path must be absolute")
	}
	info, err := os.Lstat(filename)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o077 != 0 || info.Size() < 1 || info.Size() > 4096 {
		return "", errors.New("credential must be a bounded private regular file")
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	value := strings.TrimSuffix(string(content), "\n")
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("credential must contain one non-empty line")
	}
	return value, nil
}

func runRecoverProviderRetry(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("recover-provider-retry", flag.ContinueOnError)
	flags.SetOutput(stderr)
	journalPath := flags.String("journal", "/var/lib/gha-fleet/create-retries.json", "exact GARM provider retry journal")
	lockPath := flags.String("lock", "/var/lib/gha-fleet/create-retries.lock", "exact GARM provider retry lock")
	key := flags.String("key", "", "exact terminal retry key")
	entityID := flags.String("entity-id", "", "exact forge entity UUID encoded in the retry key")
	scaleSetID := flags.Uint("scale-set-id", 0, "exact GARM scale-set database ID encoded in the retry key")
	errorClass := flags.String("error-class", "", "exact recoverable error class")
	updatedAtText := flags.String("updated-at", "", "exact RFC3339Nano updated_at precondition")
	apply := flags.Bool("apply", false, "remove the exact proven terminal circuit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *key == "" || *entityID == "" || *scaleSetID == 0 || *errorClass == "" || *updatedAtText == "" {
		fmt.Fprintln(stderr, "gha-fleet: recover-provider-retry requires --key, --entity-id, --scale-set-id, --error-class and --updated-at")
		return 2
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(stderr, "gha-fleet: recover-provider-retry must run as the garm service account")
		return 1
	}
	active, err := garmServiceActive()
	if err != nil || active {
		if err == nil {
			err = errors.New("garm.service must be stopped")
		}
		fmt.Fprintf(stderr, "gha-fleet: recover provider retry: %v\n", err)
		return 1
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, *updatedAtText)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: recover provider retry: invalid updated_at: %v\n", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := providerretry.RecoverTerminal(ctx, *journalPath, *lockPath, *key, *entityID, *scaleSetID, *errorClass, updatedAt, *apply)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: recover provider retry: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runRecoverProviderJobRetry(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("recover-provider-job-retry", flag.ContinueOnError)
	flags.SetOutput(stderr)
	journalPath := flags.String("journal", "/var/lib/gha-fleet/create-retries.json", "exact GARM provider retry journal")
	lockPath := flags.String("lock", "/var/lib/gha-fleet/create-retries.lock", "exact GARM provider retry lock")
	queuePath := flags.String("queue", "/var/lib/gha-fleet/queue-intents.json", "exact durable queue journal")
	key := flags.String("key", "", "exact terminal job retry key")
	entityID := flags.String("entity-id", "", "exact forge entity UUID encoded in the retry key")
	scaleSetID := flags.Uint("scale-set-id", 0, "exact GARM scale-set database ID encoded in the retry key")
	errorClass := flags.String("error-class", "", "exact recoverable error class")
	updatedAtText := flags.String("updated-at", "", "exact RFC3339Nano updated_at precondition")
	apply := flags.Bool("apply", false, "remove the exact proven terminal job circuit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *key == "" || *entityID == "" || *scaleSetID == 0 || *errorClass == "" || *updatedAtText == "" {
		fmt.Fprintln(stderr, "gha-fleet: recover-provider-job-retry requires --key, --entity-id, --scale-set-id, --error-class and --updated-at")
		return 2
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(stderr, "gha-fleet: recover-provider-job-retry must run as the garm service account")
		return 1
	}
	active, err := garmServiceActive()
	if err != nil || active {
		if err == nil {
			err = errors.New("garm.service must be stopped")
		}
		fmt.Fprintf(stderr, "gha-fleet: recover provider job retry: %v\n", err)
		return 1
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, *updatedAtText)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: recover provider job retry: invalid updated_at: %v\n", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := providerretry.RecoverExactJobTerminal(ctx, *journalPath, *lockPath, *queuePath, *key, *entityID, *scaleSetID, *errorClass, updatedAt, *apply)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: recover provider job retry: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

var garmServiceActive = func() (bool, error) {
	err := exec.Command("systemctl", "is-active", "--quiet", "garm.service").Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 3 {
		return false, nil
	}
	return false, fmt.Errorf("inspect garm.service: %w", err)
}

func runRecoverQueueIntent(args []string, stdout, stderr io.Writer, canceled bool) int {
	flags := flag.NewFlagSet("recover-queue-intent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	journalPath := flags.String("journal", "/var/lib/gha-fleet/queue-intents.json", "exact GARM queue journal")
	lockPath := flags.String("lock", "/var/lib/gha-fleet/queue-intents.lock", "exact GARM queue lock")
	providerPath := flags.String("provider-journal", "/var/lib/gha-fleet/provider-journal.json", "provider execution journal")
	key := flags.String("key", "", "exact stale queue-intent key")
	updatedAtText := flags.String("updated-at", "", "exact RFC3339Nano updated_at precondition")
	apply := flags.Bool("apply", false, "remove the exact proven orphan")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *key == "" || *updatedAtText == "" {
		fmt.Fprintln(stderr, "gha-fleet: recover-queue-intent requires --key and --updated-at")
		return 2
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(stderr, "gha-fleet: recover-queue-intent must run as the garm service account")
		return 1
	}
	active, err := garmServiceActive()
	if err != nil || active {
		if err == nil {
			err = errors.New("garm.service must be stopped")
		}
		fmt.Fprintf(stderr, "gha-fleet: recover queue intent: %v\n", err)
		return 1
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, *updatedAtText)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: recover queue intent: invalid updated_at: %v\n", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	providerState, err := (providerjournal.Store{Path: *providerPath}).ReadOnly(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: recover queue intent: %v\n", err)
		return 1
	}
	if len(providerState.Leases) != 0 || len(providerState.Claims) != 0 {
		fmt.Fprintln(stderr, "gha-fleet: recover queue intent: provider journal still owns execution state")
		return 1
	}
	var result queueintent.RecoveryResult
	if canceled {
		result, err = queueintent.RecoverCanceledUnbound(ctx, *journalPath, *lockPath, *key, updatedAt, *apply)
	} else {
		result, err = queueintent.RecoverUnboundRunning(ctx, *journalPath, *lockPath, *key, updatedAt, *apply)
	}
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: recover queue intent: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runReconcileRustFSCache(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile-rustfs-cache", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", rustfscache.DefaultConfigPath, "RustFS cache identity configuration path")
	apply := flags.Bool("apply", false, "create or repair the exact trust-scoped cache identities")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: reconcile-rustfs-cache accepts no positional arguments")
		return 2
	}
	if err := requireCredentialRoot(); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	config, err := rustfscache.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if err := config.ValidateProductionPaths(); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	group, err := user.LookupGroup("garm")
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: resolve garm group: %v\n", err)
		return 1
	}
	garmGID, err := strconv.Atoi(group.Gid)
	if err != nil || garmGID < 0 {
		fmt.Fprintln(stderr, "gha-fleet: garm group has an invalid numeric GID")
		return 1
	}
	requester, err := rustfscache.NewHTTPRequester(config)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: initialize RustFS cache client: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := (rustfscache.Runner{Requester: requester}).Run(ctx, rustfscache.Options{
		Config: config, Apply: *apply, RootOwnerUID: 0, RootOwnerGID: 0, OwnerUID: 0, OwnerGID: garmGID,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: reconcile RustFS cache identities: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runReconcileDiagnosticStorage(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile-diagnostic-storage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", diagnosticstore.DefaultConfigPath, "diagnostic storage configuration path")
	credentialDirectory := flags.String("credential-directory", "", "systemd credential directory override")
	apply := flags.Bool("apply", false, "create or repair the exact diagnostic bucket capacity and lifecycle")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: reconcile-diagnostic-storage accepts no positional arguments")
		return 2
	}
	if err := requireCredentialRoot(); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	config, err := diagnosticstore.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if *credentialDirectory != "" {
		config, err = diagnosticstore.WithCredentialDirectory(config, *credentialDirectory)
		if err != nil {
			fmt.Fprintf(stderr, "gha-fleet: bind diagnostic storage credentials: %v\n", err)
			return 1
		}
	}
	requester, err := rustfscache.NewHTTPRequester(rustfscache.Config{
		Endpoint: config.Endpoint, Region: config.Region, CAFile: config.CAFile,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: initialize diagnostic storage client: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := (diagnosticstore.Runner{Requester: requester}).Run(ctx, config, *apply)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: reconcile diagnostic storage: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runValidateDiagnosticStorage(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate-diagnostic-storage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", diagnosticstore.DefaultConfigPath, "diagnostic storage configuration path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: validate-diagnostic-storage accepts no positional arguments")
		return 2
	}
	config, err := diagnosticstore.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, config); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runValidateRustFSCache(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate-rustfs-cache", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", rustfscache.DefaultConfigPath, "RustFS cache identity configuration path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: validate-rustfs-cache accepts no positional arguments")
		return 2
	}
	config, err := rustfscache.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if err := config.ValidateProductionPaths(); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, config); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runValidateCacheBroker(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate-cache-broker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", cachebroker.DefaultConfigPath, "cache broker configuration path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: validate-cache-broker accepts no positional arguments")
		return 2
	}
	config, err := cachebroker.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, config); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runReconcileGARM(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile-garm", flag.ContinueOnError)
	flags.SetOutput(stderr)
	tenantConfig := flags.String("tenant-config", tenant.DefaultRegistryPath, "strict private tenant registry")
	tenantID := flags.String("tenant", "", "tenant id from the private registry; empty selects its declared default")
	repository := flags.String("repository", "", "exact reviewed repository entity; empty selects the tenant primary repository")
	baseURL := flags.String("garm-url", garmbootstrap.DefaultBaseURL, "loopback GARM API base URL")
	adminCredentials := flags.String("admin-credentials", "/etc/garm/admin-credentials.json", "private GARM administrator credential file")
	credentialAnchor := flags.String("credential-anchor", "/etc/gha-fleet/garm-credential-anchor.json", "reviewed non-secret GARM credential anchor")
	appBundle := flags.String("app-bundle", "", "private one-time GitHub App bundle, required only when creating the credential")
	scaleSet := flags.String("scale-set", garmbootstrap.DefaultScaleSetName, "exact managed scale set to reconcile")
	entityKind := flags.String("entity-kind", garmbootstrap.EntityKindRepository, "forge entity the scale set hangs from: repository serves only this repository, organization serves every repository the account holds")
	apply := flags.Bool("apply", false, "apply the exact reviewed resource and activation transition")
	enable := flags.Bool("enable", false, "enable an already-created and exactly verified disabled scale set")
	activationMode := flags.String("activation-mode", garmbootstrap.ActivationModeDirectJIT, "warm activation mode: direct-jit or metadata")
	migrateActivation := flags.Bool("migrate-activation", false, "explicitly disable and migrate between the two exact activation modes")
	migrateCapacity := flags.Bool("migrate-capacity", false, "explicitly disable and migrate to the class concurrency contract")
	migrateImage := flags.Bool("migrate-image", false, "explicitly disable and migrate to the class image/backend contract")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: reconcile-garm accepts no positional arguments")
		return 2
	}
	registry, err := tenant.Load(*tenantConfig)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	selected, err := registry.ByID(*tenantID)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v; known tenants: %v\n", err, registry.IDs())
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := (garmbootstrap.Runner{}).Run(ctx, garmbootstrap.Options{
		Registry:             registry,
		Tenant:               selected.ID,
		Repository:           *repository,
		BaseURL:              *baseURL,
		AdminCredentialsPath: *adminCredentials,
		CredentialAnchorPath: *credentialAnchor,
		AppBundleDirectory:   *appBundle,
		ScaleSetName:         *scaleSet,
		EntityKind:           *entityKind,
		Apply:                *apply,
		Enable:               *enable,
		ActivationMode:       *activationMode,
		MigrateActivation:    *migrateActivation,
		MigrateCapacity:      *migrateCapacity,
		MigrateImage:         *migrateImage,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: reconcile GARM: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runBootstrapGitHubApp(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bootstrap-github-app", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "127.0.0.1:0", "loopback callback address")
	tenantConfig := flags.String("tenant-config", tenant.DefaultRegistryPath, "strict private tenant registry")
	tenantID := flags.String("tenant", "", "tenant id from the private registry; empty selects its declared default")
	repository := flags.String("repository", "", "override the tenant's managed owner/name repository")
	ownerType := flags.String("owner-type", githubappbootstrap.OwnerTypeOrganization, "account kind that owns and installs the App: organization or user")
	organizationRunners := flags.Bool("organization-runners", false, "also request the organization runner permission; required when the App will back an organization entity")
	appName := flags.String("app-name", "", "override the tenant's private GitHub App name")
	homepage := flags.String("homepage", "", "override the tenant's GitHub App homepage URL")
	outputDirectory := flags.String("output-dir", "", "new absolute directory for the one-time private key and verified metadata")
	openBrowser := flags.Bool("open-browser", false, "open the manifest flow in the desktop browser")
	timeout := flags.Duration("timeout", 15*time.Minute, "maximum manifest and installation approval time")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *outputDirectory == "" || *timeout <= 0 || *timeout > time.Hour {
		fmt.Fprintln(stderr, "gha-fleet: bootstrap-github-app requires --output-dir and a timeout in (0,1h]")
		return 2
	}

	// An operator names an account; the identity comes from the registry. The
	// overrides exist so a mismatch can still be expressed and refused, not so
	// one can be configured.
	registry, err := tenant.Load(*tenantConfig)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	selected, err := registry.ByID(*tenantID)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v; known tenants: %v\n", err, registry.IDs())
		return 2
	}
	if *repository == "" {
		*repository = selected.Repository
	}
	if *appName == "" {
		*appName = selected.AppSlug
	}
	if *homepage == "" {
		*homepage = selected.HomepageURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result, err := (githubappbootstrap.Runner{}).Run(ctx, githubappbootstrap.Options{
		Registry:            registry,
		Tenant:              selected.ID,
		OrganizationRunners: *organizationRunners,
		ListenAddress:       *listen,
		Repository:          *repository,
		OwnerType:           *ownerType,
		AppName:             githubappbootstrap.NormalizeAppName(*appName),
		HomepageURL:         *homepage,
		OutputDirectory:     *outputDirectory,
		OpenBrowser:         *openBrowser,
	}, stderr)
	if err != nil {
		if githubappbootstrap.IsTimeout(err) {
			fmt.Fprintln(stderr, "gha-fleet: GitHub App bootstrap timed out without persisting credentials")
			return 3
		}
		fmt.Fprintf(stderr, "gha-fleet: bootstrap GitHub App: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, githubappbootstrap.RedactedResult(result)); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runVerifyGitHubApp(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("verify-github-app", flag.ContinueOnError)
	flags.SetOutput(stderr)
	tenantConfig := flags.String("tenant-config", tenant.DefaultRegistryPath, "strict private tenant registry")
	tenantID := flags.String("tenant", "", "tenant id from the private registry; empty selects its declared default")
	repository := flags.String("repository", "", "override the tenant's managed owner/name repository")
	ownerType := flags.String("owner-type", githubappbootstrap.OwnerTypeOrganization, "account kind that owns and installs the App: organization or user")
	organizationRunners := flags.Bool("organization-runners", false, "the App is expected to hold the organization runner permission; required when it backs an organization entity")
	appID := flags.Int64("app-id", 0, "the existing App's numeric id")
	installationID := flags.Int64("installation-id", 0, "the existing installation's numeric id")
	privateKey := flags.String("key", "", "absolute path to the existing App's private key")
	outputDirectory := flags.String("output-dir", "", "new absolute directory for the refreshed bundle")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum verification time")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *outputDirectory == "" || *privateKey == "" || *timeout <= 0 || *timeout > time.Hour {
		fmt.Fprintln(stderr, "gha-fleet: verify-github-app requires --key, --output-dir and a timeout in (0,1h]")
		return 2
	}
	registry, err := tenant.Load(*tenantConfig)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	selected, err := registry.ByID(*tenantID)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v; known tenants: %v\n", err, registry.IDs())
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result, err := (githubappbootstrap.Runner{}).Verify(ctx, githubappbootstrap.VerifyOptions{
		Registry:            registry,
		Tenant:              selected.ID,
		Repository:          *repository,
		OwnerType:           *ownerType,
		OrganizationRunners: *organizationRunners,
		AppID:               *appID,
		InstallationID:      *installationID,
		PrivateKeyPath:      *privateKey,
		OutputDirectory:     *outputDirectory,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: verify GitHub App: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, githubappbootstrap.RedactedResult(result)); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runValidateTelemetry(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate-telemetry", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "config/telemetry-artifacts.yaml", "pinned telemetry artifact manifest path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: validate-telemetry accepts no positional arguments")
		return 2
	}
	manifest, err := telemetrymanifest.Load(*manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	fingerprint, err := manifest.Fingerprint()
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: fingerprint telemetry manifest: %v\n", err)
		return 1
	}
	result := struct {
		Valid       bool     `json:"valid"`
		Fingerprint string   `json:"fingerprint"`
		Collector   string   `json:"collector"`
		Store       string   `json:"store"`
		StoreHost   string   `json:"store_host"`
		Bucket      string   `json:"bucket"`
		Streams     []string `json:"streams"`
		Target      string   `json:"target"`
	}{
		Valid:       true,
		Fingerprint: fingerprint,
		Collector:   manifest.Collector.Implementation + " " + manifest.Collector.Version,
		Store:       manifest.Store.Implementation + " " + manifest.Store.Version,
		StoreHost:   manifest.Store.Host,
		Bucket:      manifest.Store.Bucket,
		Streams:     manifest.Store.Streams,
		Target: fmt.Sprintf("%s://%s:%d", manifest.Transport.Protocol,
			manifest.Transport.TargetAddress, manifest.Transport.TargetPort),
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runValidateCache(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate-cache", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "config/cache-artifacts.yaml", "pinned cache artifact manifest path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: validate-cache accepts no positional arguments")
		return 2
	}
	manifest, err := cachemanifest.Load(*manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	fingerprint, err := manifest.Fingerprint()
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: fingerprint cache manifest: %v\n", err)
		return 1
	}
	result := struct {
		Valid       bool   `json:"valid"`
		Fingerprint string `json:"fingerprint"`
		RustFS      string `json:"rustfs"`
		OCIRegistry string `json:"oci_registry"`
		RustFSStage string `json:"rustfs_deployment_stage"`
		RustFSReady bool   `json:"rustfs_production_promotion_allowed"`
		OCIStage    string `json:"oci_registry_deployment_stage"`
		OCIReady    bool   `json:"oci_registry_production_promotion_allowed"`
		Production  bool   `json:"production_promotion_allowed"`
	}{
		Valid:       true,
		Fingerprint: fingerprint,
		RustFS:      manifest.RustFS.Version,
		OCIRegistry: manifest.OCIRegistry.Implementation + "@" + manifest.OCIRegistry.Version,
		RustFSStage: manifest.RustFS.DeploymentStage,
		RustFSReady: manifest.RustFS.ProductionPromotionAllowed,
		OCIStage:    manifest.OCIRegistry.DeploymentStage,
		OCIReady:    manifest.OCIRegistry.ProductionPromotionAllowed,
		Production:  manifest.RustFS.ProductionPromotionAllowed && manifest.OCIRegistry.ProductionPromotionAllowed,
	}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

// runExportJobLifecycle samples the queue journal and says every transition
// once into the gha_fleet_job_lifecycle stream. Watermarks advance only after
// a successful delivery, so a failed export retries the same records.
func runExportJobLifecycle(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("export-job-lifecycle", flag.ContinueOnError)
	flags.SetOutput(stderr)
	journalPath := flags.String("journal", "", "exact queue intents journal path")
	statePath := flags.String("state-path", "/var/lib/gha-fleet/job-lifecycle-export.json", "private export watermark state")
	endpoint := flags.String("endpoint", "", "OpenObserve HTTP(S) origin")
	usernameFile := flags.String("username-file", "", "absolute file containing only the OpenObserve username")
	passwordFile := flags.String("password-file", "", "absolute file containing only the OpenObserve password")
	apply := flags.Bool("apply", false, "deliver the records and advance the watermarks")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *journalPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: export-job-lifecycle requires --journal and no positional arguments")
		return 2
	}
	journal, err := joblifecycle.ReadJournal(*journalPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	marks, err := joblifecycle.ReadWatermarks(*statePath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	records, next := joblifecycle.Diff(marks, journal, time.Now().UTC())
	if !*apply {
		return writeJSONOrFail(stdout, stderr, map[string]any{
			"schema_version": 1, "stream": joblifecycle.StreamName,
			"pending_records": records, "applied": false,
		})
	}
	if *endpoint == "" || *usernameFile == "" || *passwordFile == "" {
		fmt.Fprintln(stderr, "gha-fleet: export-job-lifecycle --apply requires --endpoint, --username-file, and --password-file")
		return 2
	}
	username, err := readOpenObserveCredential(*usernameFile)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: read OpenObserve username: %v\n", err)
		return 1
	}
	password, err := readOpenObserveCredential(*passwordFile)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: read OpenObserve password: %v\n", err)
		return 1
	}
	exporter, err := joblifecycle.NewExporter(*endpoint, username, password)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := exporter.Export(ctx, records); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if err := joblifecycle.WriteWatermarks(*statePath, next); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return writeJSONOrFail(stdout, stderr, map[string]any{
		"schema_version": 1, "stream": joblifecycle.StreamName,
		"exported_records": len(records), "applied": true,
	})
}

func runReconcileZotCredentials(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile-zot-credentials", flag.ContinueOnError)
	flags.SetOutput(stderr)
	apply := flags.Bool("apply", false, "atomically replace bootstrap identities with repository/trust-scoped credentials")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: reconcile-zot-credentials accepts no positional arguments")
		return 2
	}
	if *apply {
		if err := requireCredentialRoot(); err != nil {
			fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
			return 1
		}
	}
	result, err := (zotcredentials.Runner{}).Run(zotcredentials.Options{
		SecretsDirectory: zotcredentials.DefaultSecretsDir,
		HTPasswdPath:     zotcredentials.DefaultHTPasswdPath,
		Apply:            *apply,
		OwnerUID:         0,
		OwnerGID:         0,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: reconcile Zot credentials: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runReconcileImage(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile-image", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config/example-runner-1.yaml", "platform configuration path")
	manifestPath := flags.String("manifest", "config/golden-image.yaml", "pinned golden-image manifest path")
	profile := flags.String("profile", "nddev-linux-standard", "existing Incus profile whose Docker capability matches the manifest")
	apply := flags.Bool("apply", false, "download, verify, build, smoke, and promote the image")
	stageOnly := flags.Bool("stage-only", false, "with --apply, build and smoke the immutable alias without changing current/previous aliases")
	preserveFailed := flags.Bool("preserve-failed-builder", false, "keep the builder instance when the build fails, so the rootfs can be inspected; it holds disk and a capacity lease until deleted")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: reconcile-image accepts no positional arguments")
		return 2
	}
	if *stageOnly && !*apply {
		fmt.Fprintln(stderr, "gha-fleet: reconcile-image --stage-only requires --apply")
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	manifest, err := imagemanifest.Load(*manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	plan, err := imageplan.Build(cfg, manifest, *profile)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: build image plan: %v\n", err)
		return 1
	}
	recipe, err := imagebuild.RecipeFingerprint(plan)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: fingerprint image recipe: %v\n", err)
		return 1
	}
	smoke, err := imagebuild.SmokeFingerprint(plan)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: fingerprint image smoke policy: %v\n", err)
		return 1
	}
	if !*apply {
		output := struct {
			Applied           bool           `json:"applied"`
			RecipeFingerprint string         `json:"recipe_fingerprint"`
			SmokeFingerprint  string         `json:"smoke_fingerprint"`
			Plan              imageplan.Plan `json:"plan"`
		}{false, recipe, smoke, plan}
		if err := writeJSON(stdout, output); err != nil {
			fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
			return 1
		}
		return 0
	}
	if err := requireLinuxRoot(); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if manifest.Image.EffectiveType() == "container" {
		if _, err := hostdeps.VerifyIncusContainer(context.Background()); err != nil {
			fmt.Fprintf(stderr, "gha-fleet: verify Incus container host dependencies: %v\n", err)
			return 1
		}
		if plan.Variant == "integration" {
			if err := hostdeps.VerifyNestedDocker(context.Background()); err != nil {
				fmt.Fprintf(stderr, "gha-fleet: verify nested Docker host dependencies: %v\n", err)
				return 1
			}
		}
	} else if _, err := hostdeps.VerifyIncusVM(context.Background()); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: verify Incus VM host dependencies: %v\n", err)
		return 1
	}
	pool, exists := cfg.Pool(*profile)
	if !exists {
		fmt.Fprintf(stderr, "gha-fleet: image profile %q does not exist\n", *profile)
		return 2
	}
	decision, err := collectImageBuildDecision(cfg, pool, manifest.Image.EffectiveType())
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: collect image-build preflight: %v\n", err)
		return 1
	}
	if !decision.PilotReady {
		return writeImagePreflightRejection(stderr, "initial", decision)
	}
	lock, err := imagebuild.AcquireLock("/run/lock/gha-fleet-image-build.lock")
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	defer lock.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()
	artifacts, err := imagebuild.FetchArtifacts(ctx, plan)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: fetch verified image artifacts: %v\n", err)
		return 1
	}
	launchDecision, err := collectImageBuildDecision(cfg, pool, manifest.Image.EffectiveType())
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: collect image-build launch preflight: %v\n", err)
		if cleanupErr := artifacts.Cleanup(); cleanupErr != nil {
			fmt.Fprintf(stderr, "gha-fleet: clean verified artifacts: %v\n", cleanupErr)
		}
		return 1
	}
	if !launchDecision.PilotReady {
		if cleanupErr := artifacts.Cleanup(); cleanupErr != nil {
			fmt.Fprintf(stderr, "gha-fleet: clean verified artifacts: %v\n", cleanupErr)
			return 1
		}
		return writeImagePreflightRejection(stderr, "pre-mutation", launchDecision)
	}
	orchestrator := &imagebuild.Orchestrator{Runner: imagebuild.ExecRunner{}, PreserveFailedBuilder: *preserveFailed}
	result, applyErr := orchestrator.ApplyWithOptions(
		ctx,
		plan,
		artifacts,
		imagebuild.ApplyOptions{StageOnly: *stageOnly},
	)
	cleanupErr := artifacts.Cleanup()
	if applyErr != nil {
		fmt.Fprintf(stderr, "gha-fleet: reconcile golden image: %v\n", applyErr)
		if cleanupErr != nil {
			fmt.Fprintf(stderr, "gha-fleet: clean verified artifacts: %v\n", cleanupErr)
		}
		return 1
	}
	if cleanupErr != nil {
		fmt.Fprintf(stderr, "gha-fleet: clean verified artifacts: %v\n", cleanupErr)
		return 1
	}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func collectColdPilotDecision(cfg config.Config, pool config.Pool) (hostprobe.Decision, error) {
	snapshot, err := hostprobe.Collect(context.Background())
	if err != nil {
		return hostprobe.Decision{}, err
	}
	return hostprobe.EvaluateColdPilot(snapshot, cfg.HostReserve, pool), nil
}

func collectImageBuildDecision(cfg config.Config, pool config.Pool, imageType string) (hostprobe.Decision, error) {
	snapshot, err := hostprobe.Collect(context.Background())
	if err != nil {
		return hostprobe.Decision{}, err
	}
	if imageType == "container" {
		return hostprobe.EvaluateContainerImageBuild(snapshot, cfg.HostReserve, pool), nil
	}
	return hostprobe.EvaluateColdPilot(snapshot, cfg.HostReserve, pool), nil
}

func writeImagePreflightRejection(stderr io.Writer, phase string, decision hostprobe.Decision) int {
	if err := writeJSON(stderr, struct {
		Error    string             `json:"error"`
		Phase    string             `json:"phase"`
		Decision hostprobe.Decision `json:"decision"`
	}{"image-build preflight rejected", phase, decision}); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 3
}

func runReconcileIncus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile-incus", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "config/example-runner-1.yaml", "platform configuration path")
	poolNames := flags.String("pool", "nddev-linux-standard", "comma-separated pilot pool names")
	apply := flags.Bool("apply", false, "apply the plan through the local Incus Unix socket")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: reconcile-incus accepts no positional arguments")
		return 2
	}

	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	pools, err := parsePools(*poolNames)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 2
	}
	plan, err := incusplan.Build(cfg, pools)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: build Incus plan: %v\n", err)
		return 1
	}
	if !*apply {
		output := struct {
			Applied bool           `json:"applied"`
			Plan    incusplan.Plan `json:"plan"`
		}{Applied: false, Plan: plan}
		if err := writeJSON(stdout, output); err != nil {
			fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
			return 1
		}
		return 0
	}
	if err := requireLinuxRoot(); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if _, err := hostdeps.VerifyIncusVM(context.Background()); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: verify Incus VM host dependencies: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	firewallResult, err := (hostfirewall.Reconciler{Runner: hostfirewall.ExecRunner{}}).Apply(ctx, plan.HostFirewall)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: reconcile host firewall: %v\n", err)
		return 1
	}
	result, err := (incusreconcile.Reconciler{Runner: incusreconcile.ExecRunner{}}).Apply(ctx, plan)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: reconcile Incus: %v\n", err)
		return 1
	}
	changes := make([]incusreconcile.Change, 0, len(firewallResult.Changes)+len(result.Changes))
	for _, change := range firewallResult.Changes {
		changes = append(changes, incusreconcile.Change{Kind: change.Kind, Name: change.Name, Action: change.Action})
	}
	changes = append(changes, result.Changes...)
	output := struct {
		Applied bool                    `json:"applied"`
		Changes []incusreconcile.Change `json:"changes"`
	}{Applied: firewallResult.Applied && result.Applied, Changes: changes}
	if err := writeJSON(stdout, output); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func parsePools(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			return nil, fmt.Errorf("--pool must contain non-empty pool names")
		}
		result = append(result, name)
	}
	return result, nil
}

func runPreflight(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "config/example-runner-1.yaml", "platform configuration path")
	poolName := flags.String("pool", "nddev-linux-fast", "cold-pilot pool name")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: preflight accepts no positional arguments")
		return 2
	}

	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	pool, exists := cfg.Pool(*poolName)
	if !exists {
		fmt.Fprintf(stderr, "gha-fleet: pool %q does not exist\n", *poolName)
		return 2
	}
	snapshot, err := hostprobe.Collect(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: collect host preflight: %v\n", err)
		return 1
	}
	decision := hostprobe.EvaluateColdPilot(snapshot, cfg.HostReserve, pool)
	result := struct {
		Snapshot hostprobe.Snapshot `json:"snapshot"`
		Decision hostprobe.Decision `json:"decision"`
	}{Snapshot: snapshot, Decision: decision}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if !decision.PilotReady {
		return 3
	}
	return 0
}

func runPublishPressure(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("publish-pressure", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "exact platform configuration path")
	statePath := flags.String("state-path", "/var/lib/gha-fleet/pressure-gate.json", "private pressure gate state")
	incusSocket := flags.String("incus-socket", "/var/lib/incus/unix.socket", "local Incus unix socket")
	forceClose := flags.String("force-close-reason", "", "publish a fail-closed state without reading PSI")
	apply := flags.Bool("apply", false, "publish member metadata and persist hysteresis state")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: publish-pressure requires --config and no positional arguments")
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if !cfg.Incus.Cluster.Enabled || cfg.Incus.Cluster.MemberName == "" {
		fmt.Fprintln(stderr, "gha-fleet: publish-pressure requires an Incus cluster member config")
		return 1
	}
	if !cfg.Pressure.Required {
		fmt.Fprintln(stderr, "gha-fleet: publish-pressure requires an enabled pressure_admission policy")
		return 1
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != cfg.Platform.Host || hostname != cfg.Incus.Cluster.MemberName {
		fmt.Fprintf(stderr, "gha-fleet: pressure publisher host %q differs from platform/member %q/%q\n", hostname, cfg.Platform.Host, cfg.Incus.Cluster.MemberName)
		return 1
	}
	now := time.Now().UTC()
	// A drained member keeps publishing: the marker turns every cycle into a
	// fresh force-closed state carrying the drain reason, so the staleness
	// alert stays quiet through the window and a reboot comes back drained.
	if *forceClose == "" {
		marker, markerErr := pressuregate.ReadDrainMarker(*statePath)
		if markerErr != nil {
			fmt.Fprintf(stderr, "gha-fleet: %v\n", markerErr)
			return 1
		}
		if marker != nil {
			*forceClose = "drained: " + marker.Reason
		}
	}
	sample := pressuregate.Sample{ObservedAt: now}
	if *forceClose == "" {
		host, collectErr := hostprobe.Collect(context.Background())
		if collectErr != nil {
			fmt.Fprintf(stderr, "gha-fleet: collect pressure: %v\n", collectErr)
			return 1
		}
		if !host.Pressure.Available {
			fmt.Fprintln(stderr, "gha-fleet: Linux PSI is unavailable")
			return 1
		}
		sample.CPUSomeAvg10 = host.Pressure.CPU.Some.Avg10
		sample.MemoryFullAvg10 = host.Pressure.Memory.Full.Avg10
		sample.IOFullAvg10 = host.Pressure.IO.Full.Avg10
		sample.OOMKillsTotal = host.Memory.OOMKillsTotal
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := incusclient.ConnectIncusUnixWithContext(ctx, *incusSocket, &incusclient.ConnectionArgs{})
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: connect Incus: %v\n", err)
		return 1
	}
	result, err := pressurepublish.Reconcile(ctx, client, pressurepublish.Options{
		MemberName: cfg.Incus.Cluster.MemberName, StatePath: *statePath, Policy: cfg.Pressure,
		Sample: sample, Apply: *apply, Now: now, ForceCloseReason: *forceClose,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: publish pressure: %v\n", err)
		return 1
	}
	return writeJSONOrFail(stdout, stderr, result)
}

// systemdUnits is the drain's control over the timer that owns the member's
// gate. The pressure publisher reasserts the gate every eleven seconds, so this
// is what makes a close hold rather than last one cycle.
type systemdUnits struct{}

func (systemdUnits) Stop(ctx context.Context, unit string) error {
	if output, err := exec.CommandContext(ctx, "systemctl", "stop", unit).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl stop %s: %v: %s", unit, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (systemdUnits) Start(ctx context.Context, unit string) error {
	if output, err := exec.CommandContext(ctx, "systemctl", "start", unit).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl start %s: %v: %s", unit, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (systemdUnits) IsActive(ctx context.Context, unit string) (bool, error) {
	// `is-active` exits non-zero for every inactive state, so the exit code
	// alone cannot tell "the unit is stopped" from "systemctl is missing". The
	// word it prints can.
	output, err := exec.CommandContext(ctx, "systemctl", "is-active", unit).CombinedOutput()
	state := strings.TrimSpace(string(output))
	switch state {
	case "active", "activating", "reloading":
		return true, nil
	case "inactive", "deactivating", "failed", "unknown":
		return false, nil
	}
	return false, fmt.Errorf("systemctl is-active %s: %v: %s", unit, err, state)
}

// drainMarker persists the drained state beside the gate file, where the
// publisher reads it on every cycle.
type drainMarker struct{ statePath string }

func (m drainMarker) Set(reason string) error {
	return pressuregate.WriteDrainMarker(m.statePath, reason, time.Now().UTC())
}

func (m drainMarker) Clear() error {
	return pressuregate.ClearDrainMarker(m.statePath)
}

// drainClient narrows the Incus client to what a drain may do: list, and
// delete the jobless warm instances it recycles.
type drainClient struct{ incus incusclient.InstanceServer }

func (c drainClient) GetInstances(kind api.InstanceType) ([]api.Instance, error) {
	return c.incus.GetInstances(kind)
}

func (c drainClient) DeleteInstance(name string) error {
	// Incus refuses to delete a running instance, and a warm occupant is
	// running by definition. Force-stop first; if the stop fails because the
	// instance is already stopped or already gone, the delete below is the
	// call that decides.
	stop, err := c.incus.UpdateInstanceState(name, api.InstanceStatePut{Action: "stop", Force: true, Timeout: -1}, "")
	if err == nil {
		_ = stop.Wait()
	}
	op, err := c.incus.DeleteInstance(name)
	if err != nil {
		return err
	}
	return op.Wait()
}

// pressureGate closes and reopens the member's gate through the publisher that
// owns it. Writing scheduler.instance directly is overwritten within one cycle.
type pressureGate struct {
	client     pressurepublish.Client
	memberName string
	statePath  string
	policy     pressuregate.Policy
}

func (g pressureGate) publish(ctx context.Context, sample pressuregate.Sample, reason string) (string, error) {
	now := time.Now().UTC()
	sample.ObservedAt = now
	result, err := pressurepublish.Reconcile(ctx, g.client, pressurepublish.Options{
		MemberName: g.memberName, StatePath: g.statePath, Policy: g.policy,
		Sample: sample, Apply: true, Now: now, ForceCloseReason: reason,
	})
	if err != nil {
		return "", err
	}
	return result.Scheduler, nil
}

func (g pressureGate) ForceClose(ctx context.Context, reason string) (string, error) {
	return g.publish(ctx, pressuregate.Sample{}, reason)
}

// Reopen does not force the gate open. It publishes from live pressure, so a
// member that is genuinely under pressure stays closed on its own merits.
func (g pressureGate) Reopen(ctx context.Context) (string, error) {
	host, err := hostprobe.Collect(ctx)
	if err != nil {
		return "", fmt.Errorf("collect pressure: %w", err)
	}
	if !host.Pressure.Available {
		return "", fmt.Errorf("Linux PSI is unavailable")
	}
	return g.publish(ctx, pressuregate.Sample{
		CPUSomeAvg10:    host.Pressure.CPU.Some.Avg10,
		MemoryFullAvg10: host.Pressure.Memory.Full.Avg10,
		IOFullAvg10:     host.Pressure.IO.Full.Avg10,
		OOMKillsTotal:   host.Memory.OOMKillsTotal,
	}, "")
}

// runSlabHeal is the automated rolling reboot for unreclaimable slab: decide
// timidly, drain through the marker, write the cooldown, and only then ask
// systemd to reboot. With --restore-after-boot it reopens exactly the drains
// it created, never an operator's.
func runSlabHeal(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("slab-heal", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "exact platform configuration path")
	statePath := flags.String("state-path", "/var/lib/gha-fleet/pressure-gate.json", "private pressure gate state")
	incusSocket := flags.String("incus-socket", "/var/lib/incus/unix.socket", "local Incus unix socket")
	timerUnit := flags.String("timer-unit", memberdrain.DefaultTimerUnit, "the timer that owns this member's gate")
	thresholdBytes := flags.Uint64("threshold-bytes", 2<<30, "SUnreclaim budget; the alert pages at the same value")
	cooldownFile := flags.String("cooldown-file", "/var/lib/gha-fleet/slab-heal.json", "when this member last healed")
	cooldown := flags.Duration("cooldown", 12*time.Hour, "minimum time between heals of this member")
	timeout := flags.Duration("timeout", memberdrain.DefaultTimeout, "how long to wait for running jobs to finish")
	poll := flags.Duration("poll", memberdrain.DefaultPoll, "how often to re-read what the member is carrying")
	restoreAfterBoot := flags.Bool("restore-after-boot", false, "reopen the gate if and only if the standing drain is a slab heal")
	apply := flags.Bool("apply", false, "drain, record the heal, and reboot, rather than reporting the decision")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: slab-heal requires --config and no positional arguments")
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if !cfg.Incus.Cluster.Enabled || cfg.Incus.Cluster.MemberName == "" || !cfg.Pressure.Required {
		fmt.Fprintln(stderr, "gha-fleet: slab-heal requires an Incus cluster member config with pressure admission")
		return 1
	}
	hostname, err := os.Hostname()
	if err != nil || hostname != cfg.Platform.Host || hostname != cfg.Incus.Cluster.MemberName {
		fmt.Fprintf(stderr, "gha-fleet: slab-heal host %q differs from platform/member %q/%q\n", hostname, cfg.Platform.Host, cfg.Incus.Cluster.MemberName)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout+2*time.Minute)
	defer cancel()
	client, err := incusclient.ConnectIncusUnixWithContext(ctx, *incusSocket, &incusclient.ConnectionArgs{})
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: connect Incus: %v\n", err)
		return 1
	}
	deps := memberdrain.Deps{
		Client: drainClient{client.UseProject(cfg.Incus.Project)},
		Units:  systemdUnits{},
		Gate: pressureGate{
			client: client, memberName: cfg.Incus.Cluster.MemberName,
			statePath: *statePath, policy: cfg.Pressure,
		},
		Marker: drainMarker{statePath: *statePath},
	}
	if *restoreAfterBoot {
		marker, err := pressuregate.ReadDrainMarker(*statePath)
		if err != nil {
			fmt.Fprintf(stderr, "gha-fleet: slab-heal: read drain marker: %v\n", err)
			return 1
		}
		if marker == nil {
			return writeJSONOrFail(stdout, stderr, map[string]any{"action": "restore-after-boot", "restored": false, "reason": "no drain marker; nothing to reopen"})
		}
		if !strings.HasPrefix(marker.Reason, slabheal.HealReasonPrefix) {
			return writeJSONOrFail(stdout, stderr, map[string]any{"action": "restore-after-boot", "restored": false, "reason": "standing drain is not a slab heal; it belongs to its operator"})
		}
		result, err := memberdrain.Restore(ctx, deps, memberdrain.Options{
			MemberName: cfg.Incus.Cluster.MemberName, TimerUnit: *timerUnit,
			Timeout: *timeout, Poll: *poll, Apply: *apply,
		})
		if err != nil {
			fmt.Fprintf(stderr, "gha-fleet: slab-heal: %v\n", err)
			return 1
		}
		return writeJSONOrFail(stdout, stderr, result)
	}
	meminfo, err := os.Open("/proc/meminfo")
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: slab-heal: %v\n", err)
		return 1
	}
	sunreclaim, err := slabheal.ParseSUnreclaim(meminfo)
	meminfo.Close()
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: slab-heal: %v\n", err)
		return 1
	}
	members, err := client.GetClusterMembers()
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: slab-heal: read cluster members: %v\n", err)
		return 1
	}
	selfState := ""
	otherStates := map[string]string{}
	for _, member := range members {
		state := member.Config["user.gha_pressure.state"]
		if member.ServerName == cfg.Incus.Cluster.MemberName {
			selfState = state
		} else {
			otherStates[member.ServerName] = state
		}
	}
	instances, err := client.UseProject(cfg.Incus.Project).GetInstances(api.InstanceTypeAny)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: slab-heal: read occupants: %v\n", err)
		return 1
	}
	nonWarm := 0
	for _, instance := range instances {
		if instance.Location == cfg.Incus.Cluster.MemberName && !strings.HasPrefix(instance.Name, "warm-") {
			nonWarm++
		}
	}
	lastHeal := time.Time{}
	if raw, err := os.ReadFile(*cooldownFile); err == nil {
		var record struct {
			LastHealAt time.Time `json:"last_heal_at"`
		}
		if json.Unmarshal(raw, &record) == nil {
			lastHeal = record.LastHealAt
		}
	}
	facts := slabheal.Facts{
		SUnreclaimBytes: sunreclaim, ThresholdBytes: *thresholdBytes,
		SelfState: selfState, OtherStates: otherStates,
		NonWarmOccupants: nonWarm, LastHealAt: lastHeal,
		Cooldown: *cooldown, Now: time.Now().UTC(),
	}
	decision := slabheal.Decide(facts)
	if !decision.Heal || !*apply {
		return writeJSONOrFail(stdout, stderr, map[string]any{
			"action": "slab-heal", "heal": decision.Heal, "applied": false,
			"reason": decision.Reason, "sunreclaim_bytes": sunreclaim,
		})
	}
	reason := fmt.Sprintf("%sSUnreclaim %.1f GiB over the %.1f GiB budget", slabheal.HealReasonPrefix, float64(sunreclaim)/(1<<30), float64(*thresholdBytes)/(1<<30))
	result, err := memberdrain.Drain(ctx, deps, memberdrain.Options{
		MemberName: cfg.Incus.Cluster.MemberName, Reason: reason,
		TimerUnit: *timerUnit, Timeout: *timeout, Poll: *poll, Apply: true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: slab-heal: %v\n", err)
		return 1
	}
	if !result.Drained {
		fmt.Fprintln(stderr, "gha-fleet: slab-heal: drain did not complete; refusing to reboot")
		return 1
	}
	record, err := json.Marshal(map[string]any{"schema_version": 1, "last_heal_at": facts.Now, "sunreclaim_bytes": sunreclaim})
	if err == nil {
		temporary := *cooldownFile + ".tmp"
		if os.WriteFile(temporary, record, 0o600) == nil {
			_ = os.Rename(temporary, *cooldownFile)
		}
	}
	if err := writeJSONOrFail(stdout, stderr, map[string]any{
		"action": "slab-heal", "heal": true, "applied": true,
		"reason": reason, "recycled_warm": result.RecycledWarm, "rebooting": true,
	}); err != 0 {
		return err
	}
	reboot := exec.CommandContext(ctx, "systemctl", "reboot")
	reboot.Stdout, reboot.Stderr = stdout, stderr
	if err := reboot.Run(); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: slab-heal: request reboot: %v\n", err)
		return 1
	}
	return 0
}

func runDrainMember(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("drain-member", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "exact platform configuration path")
	statePath := flags.String("state-path", "/var/lib/gha-fleet/pressure-gate.json", "private pressure gate state")
	incusSocket := flags.String("incus-socket", "/var/lib/incus/unix.socket", "local Incus unix socket")
	timerUnit := flags.String("timer-unit", memberdrain.DefaultTimerUnit, "the timer that owns this member's gate")
	reason := flags.String("reason", "", "why the member is being taken out of service, published as the gate's close reason")
	restore := flags.Bool("restore", false, "hand the member back: republish from live pressure and start the timer")
	timeout := flags.Duration("timeout", memberdrain.DefaultTimeout, "how long to wait for running jobs to finish")
	poll := flags.Duration("poll", memberdrain.DefaultPoll, "how often to re-read what the member is carrying")
	apply := flags.Bool("apply", false, "stop the timer and publish, rather than reporting what would happen")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *configPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: drain-member requires --config and no positional arguments")
		return 2
	}
	if *restore && *reason != "" {
		fmt.Fprintln(stderr, "gha-fleet: drain-member --restore takes no --reason")
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if !cfg.Incus.Cluster.Enabled || cfg.Incus.Cluster.MemberName == "" {
		fmt.Fprintln(stderr, "gha-fleet: drain-member requires an Incus cluster member config")
		return 1
	}
	if !cfg.Pressure.Required {
		fmt.Fprintln(stderr, "gha-fleet: drain-member requires an enabled pressure_admission policy")
		return 1
	}
	// The gate is per-member and published by the member itself, so draining
	// one host from another would write somebody else's state.
	hostname, err := os.Hostname()
	if err != nil || hostname != cfg.Platform.Host || hostname != cfg.Incus.Cluster.MemberName {
		fmt.Fprintf(stderr, "gha-fleet: drain host %q differs from platform/member %q/%q\n", hostname, cfg.Platform.Host, cfg.Incus.Cluster.MemberName)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout+2*time.Minute)
	defer cancel()
	client, err := incusclient.ConnectIncusUnixWithContext(ctx, *incusSocket, &incusclient.ConnectionArgs{})
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: connect Incus: %v\n", err)
		return 1
	}
	// Cluster members are not project-scoped; workers are. The gate is read and
	// written on the unscoped connection, the occupancy on the worker project.
	deps := memberdrain.Deps{
		Client: drainClient{client.UseProject(cfg.Incus.Project)},
		Units:  systemdUnits{},
		Gate: pressureGate{
			client: client, memberName: cfg.Incus.Cluster.MemberName,
			statePath: *statePath, policy: cfg.Pressure,
		},
		Marker: drainMarker{statePath: *statePath},
	}
	options := memberdrain.Options{
		MemberName: cfg.Incus.Cluster.MemberName, Reason: *reason,
		TimerUnit: *timerUnit, Timeout: *timeout, Poll: *poll, Apply: *apply,
	}
	var result memberdrain.Result
	if *restore {
		result, err = memberdrain.Restore(ctx, deps, options)
	} else {
		result, err = memberdrain.Drain(ctx, deps, options)
	}
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: drain-member: %v\n", err)
		return 1
	}
	if writeJSONOrFail(stdout, stderr, result) != 0 {
		return 1
	}
	// A drain that ran out of time left somebody's job running and the member
	// closed. That is not a success, and a caller scripting a reboot must see it.
	if result.TimedOut {
		return 1
	}
	return 0
}

func runValidate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "config/example-runner-1.yaml", "platform configuration path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: validate accepts no positional arguments")
		return 2
	}

	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	fingerprint, err := cfg.Fingerprint()
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: fingerprint: %v\n", err)
		return 1
	}
	result := struct {
		Valid         bool   `json:"valid"`
		SchemaVersion int    `json:"schema_version"`
		Platform      string `json:"platform"`
		Host          string `json:"host"`
		Backends      int    `json:"backends"`
		Pools         int    `json:"pools"`
		Fingerprint   string `json:"fingerprint"`
	}{true, cfg.SchemaVersion, cfg.Platform.Name, cfg.Platform.Host, len(cfg.Backends), len(cfg.Pools), fingerprint}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

func runRender(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("render", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "config/example-runner-1.yaml", "platform configuration path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: render accepts no positional arguments")
		return 2
	}

	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	rendered, err := cfg.RenderJSON()
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: render: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, string(rendered)); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: write output: %v\n", err)
		return 1
	}
	return 0
}

func runAdmit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("admit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "config/example-runner-1.yaml", "platform configuration path")
	poolName := flags.String("pool", "", "pool name")
	snapshotPath := flags.String("snapshot", "", "JSON host snapshot path, or - for stdin")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *poolName == "" || *snapshotPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: admit requires --pool and --snapshot")
		return 2
	}

	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	pool, exists := cfg.Pool(*poolName)
	if !exists {
		fmt.Fprintf(stderr, "gha-fleet: pool %q does not exist\n", *poolName)
		return 2
	}

	snapshot, err := readSnapshot(*snapshotPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: read snapshot: %v\n", err)
		return 1
	}
	decision, err := admission.Evaluate(snapshot, admission.ReservePolicy{
		MinimumCPUUnits:        cfg.HostReserve.MinimumCPUUnits,
		MinimumMemoryMiB:       cfg.HostReserve.MinimumMemoryMiB,
		MinimumPercent:         cfg.HostReserve.MinimumPercent,
		MinimumFreeDiskPercent: cfg.HostReserve.MinimumFreeDiskPercent,
	}, admission.Request{
		PoolName:  pool.Name,
		VCPU:      pool.Resources.VCPU,
		MemoryMiB: pool.Resources.MemoryMiB,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: evaluate admission: %v\n", err)
		return 1
	}
	if err := writeJSON(stdout, decision); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if !decision.Admitted {
		return 3
	}
	return 0
}

func readSnapshot(path string) (admission.HostSnapshot, error) {
	var reader io.Reader
	if path == "-" {
		reader = os.Stdin
	} else {
		file, err := os.Open(path)
		if err != nil {
			return admission.HostSnapshot{}, err
		}
		defer file.Close()
		reader = file
	}

	const maxSnapshotBytes = 64 * 1024
	data, err := io.ReadAll(io.LimitReader(reader, maxSnapshotBytes+1))
	if err != nil {
		return admission.HostSnapshot{}, err
	}
	if len(data) > maxSnapshotBytes {
		return admission.HostSnapshot{}, fmt.Errorf("snapshot exceeds %d bytes", maxSnapshotBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot admission.HostSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return admission.HostSnapshot{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return admission.HostSnapshot{}, fmt.Errorf("multiple JSON values are not allowed")
		}
		return admission.HostSnapshot{}, err
	}
	return snapshot, nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}

// runRenderGARMBuild rewrites the generated region of the GARM build script from
// the derivative manifest. The script is the only consumer of those values, so
// generating them is what stops the manifest from being a description the build
// is free to contradict -- which is what it was when the fifth patch digest sat
// in the script unchecked by anything.
//
// --check reports whether the region is already current and changes nothing,
// which is the form the contract test and CI use.
func runRenderGARMBuild(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("render-garm-build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", garmderivative.DefaultManifestPath, "GARM derivative manifest path")
	scriptPath := flags.String("script", garmderivative.DefaultScriptPath, "build script whose generated region is rendered")
	check := flags.Bool("check", false, "report whether the region is current instead of rewriting it")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: render-garm-build accepts no positional arguments")
		return 2
	}
	manifest, err := garmderivative.Load(*manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	current, err := os.ReadFile(*scriptPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: read build script: %v\n", err)
		return 1
	}
	rendered, err := manifest.SpliceRegion(string(current))
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if rendered == string(current) {
		return writeJSONOrFail(stdout, stderr, map[string]any{"script": *scriptPath, "current": true, "written": false})
	}
	if *check {
		fmt.Fprintf(stderr, "gha-fleet: %s is stale; run `make garm-derivative-script`\n", *scriptPath)
		return 1
	}
	info, err := os.Stat(*scriptPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: stat build script: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*scriptPath, []byte(rendered), info.Mode().Perm()); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: write build script: %v\n", err)
		return 1
	}
	return writeJSONOrFail(stdout, stderr, map[string]any{"script": *scriptPath, "current": false, "written": true})
}

func writeJSONOrFail(stdout, stderr io.Writer, payload any) int {
	if err := writeJSON(stdout, payload); err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return 0
}

// runProviderRelease reports the provider release identity from its manifest.
// The Makefile reads --field derivative_version to stamp the binary, so the
// version the build embeds and the version the manifest declares are the same
// statement rather than two literals that happened to agree.
func runProviderRelease(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("provider-release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", providerrelease.DefaultManifestPath, "provider release manifest path")
	field := flags.String("field", "", "print one field verbatim instead of the whole record")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: provider-release accepts no positional arguments")
		return 2
	}
	manifest, err := providerrelease.Load(*manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	fields := map[string]string{
		"derivative_version": manifest.DerivativeVersion,
		"interface_version":  manifest.InterfaceVersion,
		"upstream_release":   manifest.Upstream.Release,
		"upstream_commit":    manifest.Upstream.Commit,
		"incus_sdk_version":  manifest.Runtime.IncusSDKVersion,
		"source_commit":      manifest.Build.SourceCommit,
		"binary_sha256":      manifest.Build.BinarySHA256,
	}
	if *field != "" {
		value, known := fields[*field]
		if !known {
			names := make([]string, 0, len(fields))
			for name := range fields {
				names = append(names, name)
			}
			slices.Sort(names)
			fmt.Fprintf(stderr, "gha-fleet: unknown field %q, want one of %s\n", *field, strings.Join(names, ", "))
			return 2
		}
		fmt.Fprintln(stdout, value)
		return 0
	}
	return writeJSONOrFail(stdout, stderr, fields)
}

// runFleetContract renders the one statement of what this fleet offers, at the
// commit it is run from. A consumer pins that commit; the contract describes
// exactly that tree.
//
// It is rendered rather than committed. A generated file holding its own commit
// would be stale the moment it was committed, so the artifact is produced on
// demand and the tree holds only what cannot be derived.
func runFleetContract(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("fleet-contract", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root the contract is assembled from")
	commit := flags.String("commit", "", "commit this contract describes; read from git when empty")
	configPath := flags.String("config", "", "optional deployment overlay to verify against the rendered contract")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: fleet-contract accepts no positional arguments")
		return 2
	}
	resolved := *commit
	if resolved == "" {
		output, err := exec.Command("git", "-C", *root, "rev-parse", "HEAD").Output()
		if err != nil {
			fmt.Fprintf(stderr, "gha-fleet: resolve commit: %v (pass --commit to state it)\n", err)
			return 1
		}
		resolved = strings.TrimSpace(string(output))
	}
	contract, err := fleetcontract.Build(fleetcontract.Sources{Root: *root}, resolved)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	if *configPath != "" {
		platform, loadErr := config.Load(*configPath)
		if loadErr != nil {
			fmt.Fprintf(stderr, "gha-fleet: load deployment overlay: %v\n", loadErr)
			return 1
		}
		if validateErr := fleetcontract.ValidateConfig(contract, platform); validateErr != nil {
			fmt.Fprintf(stderr, "gha-fleet: verify deployment overlay: %v\n", validateErr)
			return 1
		}
	}
	return writeJSONOrFail(stdout, stderr, contract)
}

// runCapacity reports how many workers of each class a host can run at once, and
// which declared cap decides it.
//
// The fleet asserts one job per host in nine places, which reads as a pilot
// constant waiting to be raised. For the heavy classes it is arithmetic:
// project_max_cpu_units: 6 divided by a four-vCPU worker is one. Reporting the
// binding constraint is what turns "why is this one" into an answerable question.
func runCapacity(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("capacity", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "platform configuration path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "gha-fleet: capacity requires --config")
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "gha-fleet: capacity accepts no positional arguments")
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	limits, err := hostcapacity.ForHost(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v\n", err)
		return 1
	}
	return writeJSONOrFail(stdout, stderr, map[string]any{
		"host":   cfg.Platform.Host,
		"limits": limits,
	})
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: gha-fleet <validate|validate-cache|validate-cache-broker|validate-telemetry|validate-rustfs-cache|validate-diagnostic-exporter|validate-diagnostic-storage|validate-tenant-registry|validate-queue-admission|validate-observability-rules|validate-observability-dashboards|render-openobserve-alerts|render-openobserve-dashboards|reconcile-openobserve-alerts|reconcile-openobserve-dashboards|export-job-lifecycle|render|admit|preflight|publish-pressure|drain-member|slab-heal|reconcile-incus|reconcile-image|bootstrap-github-app|verify-github-app|reconcile-garm|reconcile-zot-credentials|reconcile-rustfs-cache|reconcile-diagnostic-storage|render-garm-build|provider-release|fleet-contract|capacity|recover-provider-retry|recover-provider-job-retry|version> [options]")
}
