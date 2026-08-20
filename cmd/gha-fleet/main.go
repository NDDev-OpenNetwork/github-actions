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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
	"github.com/NDDev-OpenNetwork/github-actions/internal/cachemanifest"
	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
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
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerrelease"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerretry"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
	"github.com/NDDev-OpenNetwork/github-actions/internal/telemetrymanifest"
	"github.com/NDDev-OpenNetwork/github-actions/internal/zotcredentials"
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
	case "reconcile-incus":
		return runReconcileIncus(args[1:], stdout, stderr)
	case "reconcile-image":
		return runReconcileImage(args[1:], stdout, stderr)
	case "validate-cache":
		return runValidateCache(args[1:], stdout, stderr)
	case "validate-telemetry":
		return runValidateTelemetry(args[1:], stdout, stderr)
	case "validate-rustfs-cache":
		return runValidateRustFSCache(args[1:], stdout, stderr)
	case "reconcile-zot-credentials":
		return runReconcileZotCredentials(args[1:], stdout, stderr)
	case "reconcile-rustfs-cache":
		return runReconcileRustFSCache(args[1:], stdout, stderr)
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

func runRecoverProviderRetry(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("recover-provider-retry", flag.ContinueOnError)
	flags.SetOutput(stderr)
	journalPath := flags.String("journal", "/var/lib/gha-fleet/create-retries.json", "exact GARM provider retry journal")
	lockPath := flags.String("lock", "/var/lib/gha-fleet/create-retries.lock", "exact GARM provider retry lock")
	providerPath := flags.String("provider-journal", "/var/lib/gha-fleet/provider-journal.json", "provider execution journal")
	poolName := flags.String("pool-name", "", "exact provider pool protected by this circuit")
	key := flags.String("key", "", "exact terminal retry key")
	errorClass := flags.String("error-class", "", "exact recoverable error class")
	updatedAtText := flags.String("updated-at", "", "exact RFC3339Nano updated_at precondition")
	apply := flags.Bool("apply", false, "remove the exact proven terminal circuit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *key == "" || *poolName == "" || *errorClass == "" || *updatedAtText == "" {
		fmt.Fprintln(stderr, "gha-fleet: recover-provider-retry requires --key, --pool-name, --error-class and --updated-at")
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
	providerState, err := (providerjournal.Store{Path: *providerPath}).ReadOnly(ctx)
	targetOwned := false
	for _, lease := range providerState.Leases {
		targetOwned = targetOwned || lease.PoolName == *poolName
	}
	for _, claim := range providerState.Claims {
		targetOwned = targetOwned || claim.PoolName == *poolName
	}
	if err != nil || targetOwned {
		if err == nil {
			err = fmt.Errorf("provider journal still owns execution state for pool %q", *poolName)
		}
		fmt.Fprintf(stderr, "gha-fleet: recover provider retry: %v\n", err)
		return 1
	}
	result, err := providerretry.RecoverTerminal(ctx, *journalPath, *lockPath, *key, *errorClass, updatedAt, *apply)
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

func runReconcileGARM(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile-garm", flag.ContinueOnError)
	flags.SetOutput(stderr)
	tenantID := flags.String("tenant", tenant.DefaultID, "registered account to reconcile: "+strings.Join(tenant.IDs(), ", "))
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := (garmbootstrap.Runner{}).Run(ctx, garmbootstrap.Options{
		Tenant:               *tenantID,
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
	tenantID := flags.String("tenant", tenant.DefaultID, "registered account this App will serve: "+strings.Join(tenant.IDs(), ", "))
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
	selected, err := tenant.ByID(*tenantID)
	if err != nil {
		fmt.Fprintf(stderr, "gha-fleet: %v; known tenants: %v\n", err, tenant.IDs())
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
	tenantID := flags.String("tenant", tenant.DefaultID, "registered account whose App is being re-verified: "+strings.Join(tenant.IDs(), ", "))
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

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	result, err := (githubappbootstrap.Runner{}).Verify(ctx, githubappbootstrap.VerifyOptions{
		Tenant:              *tenantID,
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
	decision, err := collectColdPilotDecision(cfg, pool)
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
	launchDecision, err := collectColdPilotDecision(cfg, pool)
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
	orchestrator := &imagebuild.Orchestrator{Runner: imagebuild.ExecRunner{}}
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
	fmt.Fprintln(writer, "usage: gha-fleet <validate|validate-cache|validate-telemetry|validate-rustfs-cache|render|admit|preflight|reconcile-incus|reconcile-image|bootstrap-github-app|verify-github-app|reconcile-garm|reconcile-zot-credentials|reconcile-rustfs-cache|render-garm-build|provider-release|fleet-contract|capacity|version> [options]")
}
