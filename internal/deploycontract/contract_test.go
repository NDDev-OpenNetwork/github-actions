package deploycontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticexport"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmbootstrap"
	providerconfig "github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/zotcredentials"
)

// deploymentRoot holds the unit, sysusers, tmpfiles, timer and provider files
// every fleet host runs. It carried one host's name while there was one host,
// and kept it while there were three; it does not now, because the host it was
// named for is no longer part of the fleet at all.
const deploymentRoot = "../../deploy/fleet-host"

type garmConfig struct {
	Default struct {
		CallbackURL             string `toml:"callback_url"`
		MetadataURL             string `toml:"metadata_url"`
		WebhookURL              string `toml:"webhook_url"`
		EnableWebhookManagement bool   `toml:"enable_webhook_management"`
		EnableLogStreamer       bool   `toml:"enable_log_streamer"`
		DebugServer             bool   `toml:"debug_server"`
	} `toml:"default"`
	Logging struct {
		EnableLogStreamer bool   `toml:"enable_log_streamer"`
		LogFormat         string `toml:"log_format"`
		LogLevel          string `toml:"log_level"`
		LogSource         bool   `toml:"log_source"`
	} `toml:"logging"`
	Metrics struct {
		Enable      bool   `toml:"enable"`
		DisableAuth bool   `toml:"disable_auth"`
		Period      string `toml:"period"`
	} `toml:"metrics"`
	JWTAuth struct {
		Secret     string `toml:"secret"`
		TimeToLive string `toml:"time_to_live"`
	} `toml:"jwt_auth"`
	APIServer struct {
		Bind   string `toml:"bind"`
		Port   int    `toml:"port"`
		UseTLS bool   `toml:"use_tls"`
		WebUI  struct {
			Enable bool `toml:"enable"`
		} `toml:"webui"`
	} `toml:"apiserver"`
	Database struct {
		Debug      bool   `toml:"debug"`
		Backend    string `toml:"backend"`
		Passphrase string `toml:"passphrase"`
		SQLite     struct {
			DBFile             string `toml:"db_file"`
			BusyTimeoutSeconds int    `toml:"busy_timeout_seconds"`
		} `toml:"sqlite3"`
	} `toml:"database"`
	Providers []struct {
		Name             string `toml:"name"`
		ProviderType     string `toml:"provider_type"`
		Description      string `toml:"description"`
		DisableJITConfig bool   `toml:"disable_jit_config"`
		External         struct {
			InterfaceVersion     string   `toml:"interface_version"`
			ProviderExecutable   string   `toml:"provider_executable"`
			ConfigFile           string   `toml:"config_file"`
			EnvironmentVariables []string `toml:"environment_variables"`
		} `toml:"external"`
	} `toml:"provider"`
}

type zotPolicy struct {
	Users   []string `json:"users"`
	Actions []string `json:"actions"`
}

type zotRepositoryPolicy struct {
	Policies      []zotPolicy `json:"policies"`
	DefaultPolicy []string    `json:"defaultPolicy"`
}

type zotConfig struct {
	DistSpecVersion string `json:"distSpecVersion"`
	Storage         struct {
		RootDirectory string `json:"rootDirectory"`
		GC            bool   `json:"gc"`
		GCDelay       string `json:"gcDelay"`
		GCInterval    string `json:"gcInterval"`
		GCTimeWindow  string `json:"gcTimeWindow"`
	} `json:"storage"`
	HTTP struct {
		Address string `json:"address"`
		Port    string `json:"port"`
		Realm   string `json:"realm"`
		TLS     struct {
			Cert string `json:"cert"`
			Key  string `json:"key"`
		} `json:"tls"`
		Auth struct {
			HTPasswd struct {
				Path string `json:"path"`
			} `json:"htpasswd"`
			FailDelay int `json:"failDelay"`
		} `json:"auth"`
		AccessControl struct {
			Repositories map[string]zotRepositoryPolicy `json:"repositories"`
		} `json:"accessControl"`
	} `json:"http"`
	Log struct {
		Level string `json:"level"`
	} `json:"log"`
	Extensions json.RawMessage `json:"extensions"`
}

func TestGARMTemplateHasClosedNetworkAndProviderContract(t *testing.T) {
	raw := read(t, "garm.toml.tmpl")
	if strings.Count(raw, "@GARM_JWT_SECRET@") != 1 || strings.Count(raw, "@GARM_DATABASE_PASSPHRASE@") != 1 {
		t.Fatal("GARM template must contain exactly the two documented secret tokens")
	}
	rendered := strings.ReplaceAll(raw, "@GARM_JWT_SECRET@", "correct-horse-battery-staple-jwt-secret")
	rendered = strings.ReplaceAll(rendered, "@GARM_DATABASE_PASSPHRASE@", "0123456789abcdefghijklmnopqrstuv")
	var config garmConfig
	metadata, err := toml.Decode(rendered, &config)
	if err != nil {
		t.Fatal(err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		t.Fatalf("unknown GARM template keys: %v", undecoded)
	}

	if config.APIServer.Bind != "127.0.0.1" || config.APIServer.Port != 9997 || config.APIServer.UseTLS || config.APIServer.WebUI.Enable {
		t.Fatalf("GARM API is not loopback-only: %#v", config.APIServer)
	}
	if config.Default.CallbackURL != "https://192.0.2.1:9443/api/v1/callbacks" ||
		config.Default.MetadataURL != "https://192.0.2.1:9443/api/v1/metadata" ||
		config.Default.EnableWebhookManagement || config.Default.DebugServer || config.Default.EnableLogStreamer {
		t.Fatalf("unsafe GARM defaults: %#v", config.Default)
	}
	if !config.Metrics.Enable || config.Metrics.DisableAuth || config.Logging.LogFormat != "json" || config.Logging.EnableLogStreamer {
		t.Fatalf("unsafe observability configuration: metrics=%#v logging=%#v", config.Metrics, config.Logging)
	}
	if len(config.Providers) != 1 {
		t.Fatalf("expected one provider, got %d", len(config.Providers))
	}
	provider := config.Providers[0]
	if provider.Name != "nddev-incus" || provider.ProviderType != "external" || provider.DisableJITConfig ||
		provider.External.InterfaceVersion != "v0.1.0" ||
		provider.External.ProviderExecutable != "/usr/local/libexec/gha-fleet/garm-provider-incus-nddev" ||
		provider.External.ConfigFile != "/etc/garm/provider-incus.toml" ||
		len(provider.External.EnvironmentVariables) != 1 || provider.External.EnvironmentVariables[0] != "PATH" {
		t.Fatalf("unexpected external provider contract: %#v", provider)
	}
}

func TestProviderDeploymentPinsExactImageAndTLSBoundary(t *testing.T) {
	raw := read(t, "provider-incus.toml")
	var config providerconfig.Incus
	metadata, err := toml.Decode(raw, &config)
	if err != nil {
		t.Fatal(err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		t.Fatalf("unknown provider keys: %v", undecoded)
	}
	if config.URL != providerconfig.ExpectedIncusURL || config.UnixSocket != "" || config.ProjectName != providerconfig.ExpectedProjectName ||
		config.IncludeDefaultProfile || !config.SecureBoot || config.InstanceType != providerconfig.IncusImageVirtualMachine {
		t.Fatalf("unsafe provider endpoint or VM contract: %#v", config)
	}
	// Registering a class is what makes GitHub keep it and route jobs to it, so
	// every class the reconciler publishes needs an image the provider can
	// build. This asserted a count of two while three were published, and the
	// third -- the fast class, which is what 44 of the 46 reusable workflows in
	// this estate actually need -- had no pinned image: it did not fail to
	// exist, it accepted work and failed every create. The expectation is taken
	// from the published set now, so the two declarations cannot drift again.
	published := garmbootstrap.PublishedScaleSets()
	if len(config.WorkerImages) != len(published) {
		t.Fatalf("provider pins %d worker images for %d published classes: %#v",
			len(config.WorkerImages), len(published), config.WorkerImages)
	}
	for _, class := range published {
		image, exists := config.WorkerImageForFlavor(class.Flavor)
		switch {
		case !exists:
			t.Fatalf("scale set %q is registrable and the provider pins no worker image for it", class.Name)
		case image.Alias != class.Image:
			t.Fatalf("scale set %q registers alias %q, the provider pins %q", class.Name, class.Image, image.Alias)
		case image.RunnerUID != 1001 || image.RunnerGID != 1002:
			t.Fatalf("worker identity for %q drifted: %#v", class.Name, image)
		}
	}
	standard, _ := config.WorkerImageForFlavor(garmbootstrap.DefaultFlavor)
	integration, _ := config.WorkerImageForFlavor(garmbootstrap.IntegrationFlavor)
	fast, _ := config.WorkerImageForFlavor(garmbootstrap.FastFlavor)
	if standard.Alias != garmbootstrap.DefaultImage || standard.Variant != "standard" ||
		standard.Fingerprint != "366866d4ec6070c38351c73e6ac7cd79b2e7abaf6c3190e5388756d4e4961135" ||
		integration.Alias != "nddev-ubuntu-24.04-amd64-docker-current" || integration.Variant != "integration" ||
		integration.Fingerprint != "fa25e3753bbdcdc946c3dd9a8f546b86be823fee6e89e40fe6c495ab4ae895f2" ||
		// The fast class is the standard image under a second flavor, denied a
		// job credential and Docker by pool policy rather than by its contents,
		// so it is pinned to that exact digest and moves only when it moves.
		fast.Variant != "standard" || fast.Alias != standard.Alias || fast.Fingerprint != standard.Fingerprint ||
		config.AdmissionLeaseSeconds != 300 || config.PlatformConfigFile != "/etc/gha-fleet/platform.yaml" ||
		config.DiagnosticsDirectory != "/var/lib/gha-fleet/diagnostics" ||
		config.DiagnosticsRetentionHours != 168 || config.DiagnosticsMaxBundleBytes != 16*1024*1024 ||
		config.DiagnosticsMaxTotalBytes != 1024*1024*1024 {
		t.Fatalf("provider image/admission contract drifted: %#v", config)
	}
}

func TestSystemdUnitsRetainPrivilegeSeparation(t *testing.T) {
	garm := read(t, "garm.service")
	gateway := read(t, "gha-fleet-gateway.service")
	observer := read(t, "gha-fleet-observer.service")
	warmService := read(t, "gha-warm-pool.service")
	warmTimer := read(t, "gha-warm-pool.timer")
	for _, required := range []string{
		"User=garm", "SupplementaryGroups=kvm", "DevicePolicy=closed", "DeviceAllow=/dev/kvm rw",
		"ProtectSystem=strict", "NoNewPrivileges=true", "CapabilityBoundingSet=", "ReadWritePaths=/var/lib/garm /var/lib/gha-fleet",
		"Wants=network-online.target gha-fleet-gateway.service",
	} {
		if !strings.Contains(garm, required) {
			t.Errorf("garm.service is missing %q", required)
		}
	}
	if strings.Contains(garm, "incus/unix.socket") || strings.Contains(garm, "docker.sock") {
		t.Fatal("garm.service exposes a forbidden host socket")
	}
	for _, required := range []string{
		"User=gha-gateway", "Requires=incus.service garm.service", "PrivateDevices=true",
		"ProtectSystem=strict", "NoNewPrivileges=true", "RestrictAddressFamilies=AF_INET",
	} {
		if !strings.Contains(gateway, required) {
			t.Errorf("gateway service is missing %q", required)
		}
	}
	for _, required := range []string{
		"User=garm", "SupplementaryGroups=kvm", "DevicePolicy=closed", "DeviceAllow=/dev/kvm rw",
		"--listen 127.0.0.1:9464", "ProtectSystem=strict", "NoNewPrivileges=true", "CapabilityBoundingSet=",
		"After=network-online.target incus.service garm.service gha-fleet-gateway.service gha-rustfs.service gha-zot.service gha-warm-pool.timer",
		"Wants=network-online.target incus.service garm.service gha-fleet-gateway.service gha-rustfs.service gha-zot.service gha-warm-pool.timer",
		"IPAddressDeny=any", "IPAddressAllow=127.0.0.0/8", "RestrictAddressFamilies=AF_UNIX AF_INET",
		"InaccessiblePaths=-/var/run/docker.sock -/run/incus/unix.socket -/var/lib/incus/unix.socket",
		"ReadOnlyPaths=/etc/garm /etc/gha-fleet /var/lib/gha-fleet /var/lib/gha-diagnostic-exporter /usr/local/libexec/gha-fleet",
	} {
		if !strings.Contains(observer, required) {
			t.Errorf("observer service is missing %q", required)
		}
	}
	for _, forbidden := range []string{"ReadWritePaths=", "BindPaths=", "BindReadOnlyPaths=", "IPAddressAllow=192.0.2.0/24"} {
		if strings.Contains(observer, forbidden) {
			t.Errorf("observer service contains forbidden %q", forbidden)
		}
	}
	for _, required := range []string{
		"Type=oneshot", "User=garm", "Group=garm", "SupplementaryGroups=kvm", "EnvironmentFile=/etc/garm/warm-pool.env",
		// The flavour is per host, because a scale set name is unique per
		// repository and each host therefore keeps a different pool warm. It
		// must still come from the reviewed non-secret env file, never be
		// hard-coded and never be defaulted, so an unset value fails the unit
		// instead of warming whichever pool happened to be listed here.
		"warm-pool --config /etc/garm/provider-incus.toml", "--flavor ${GARM_WARM_FLAVOR} --apply",
		"NoNewPrivileges=true", "PrivateDevices=false", "DevicePolicy=closed", "DeviceAllow=/dev/kvm rw",
		"ProtectSystem=strict", "RestrictAddressFamilies=AF_UNIX AF_INET", "IPAddressDeny=any", "IPAddressAllow=127.0.0.0/8",
		"InaccessiblePaths=-/var/run/docker.sock -/run/incus/unix.socket -/var/lib/incus/unix.socket",
		"ReadOnlyPaths=/etc/garm /etc/gha-fleet /usr/local/libexec/gha-fleet", "ReadWritePaths=/var/lib/gha-fleet",
	} {
		if !strings.Contains(warmService, required) {
			t.Errorf("gha-warm-pool.service is missing %q", required)
		}
	}
	for _, forbidden := range []string{"PrivateDevices=true", "User=root", "SupplementaryGroups=incus-admin", "IPAddressAllow=192.0.2.0/24"} {
		if strings.Contains(warmService, forbidden) {
			t.Errorf("gha-warm-pool.service contains forbidden %q", forbidden)
		}
	}
	for _, required := range []string{
		"OnBootSec=45s", "OnUnitInactiveSec=30s", "RandomizedDelaySec=5s", "Persistent=true",
		"Unit=gha-warm-pool.service", "WantedBy=timers.target",
	} {
		if !strings.Contains(warmTimer, required) {
			t.Errorf("gha-warm-pool.timer is missing %q", required)
		}
	}
}

// One collector configuration now serves every host.
//
// It used to be two, because the telemetry host ran nothing but OpenObserve and
// had no observer to scrape. It runs the queue and the observer now, so the
// distinction it encoded is gone -- and two files that must stay identical are
// a drift source, not a boundary. What still differs between the roles is the
// systemd unit, which is asserted below.
func TestOneCollectorConfigurationServesEveryHost(t *testing.T) {
	fleet := read(t, "otelcol-fleet.yaml")
	if !strings.Contains(fleet, "prometheus/fleet:") || !strings.Contains(fleet, "prometheus/fleet, hostmetrics/member") {
		t.Fatal("collector lost its observer metrics pipeline")
	}
	// Every cluster member reports its own vitals, whether or not it runs the
	// observer. The numbers that decide whether a member can take another
	// worker have to exist for all of them, or a placement decision cannot be
	// explained after the fact.
	if !strings.Contains(fleet, "hostmetrics/member:") {
		t.Fatal("collector lost its per-member host metrics")
	}
	servicesConfig := filepath.Join(deploymentRoot, "..", "services-host", "otelcol-fleet.yaml")
	if _, err := os.Stat(servicesConfig); !os.IsNotExist(err) {
		t.Fatalf("a second collector configuration is back at %s; one file serves every host", servicesConfig)
	}

	// The queue host has no local hypervisor, so its units must not require
	// one. Requires=incus.service is what left GARM inactive there with no log
	// line at all on first install.
	for _, name := range []string{"garm.service", "gha-fleet-observer.service"} {
		unitBytes, err := os.ReadFile(filepath.Join(deploymentRoot, "..", "services-host", name))
		if err != nil {
			t.Fatal(err)
		}
		// Directives only. These units explain in comments why the
		// hypervisor dependencies are absent, and a comment saying so must
		// not read as the dependency being present.
		for _, line := range strings.Split(string(unitBytes), "\n") {
			directive := strings.TrimSpace(line)
			if directive == "" || strings.HasPrefix(directive, "#") {
				continue
			}
			for _, forbidden := range []string{"Requires=incus.service", "SupplementaryGroups=kvm", "DeviceAllow=/dev/kvm"} {
				if strings.HasPrefix(directive, forbidden) {
					t.Errorf("services-host %s assumes a local hypervisor: %q", name, directive)
				}
			}
		}
	}

	unitBytes, err := os.ReadFile(filepath.Join(deploymentRoot, "..", "services-host", "otelcol-fleet.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unitBytes), "After=network-online.target openobserve.service") {
		t.Fatal("services-host collector unit has the wrong role dependencies")
	}
}

func TestTmpfilesAllowsGatewayTraversalWithoutSharingGARMGroup(t *testing.T) {
	tmpfiles := read(t, "gha-fleet.tmpfiles")
	for _, required := range []string{
		"d /etc/gha-fleet 0755 root root -",
		"d /etc/gha-fleet/pki 0750 root gha-gateway -",
		"d /var/lib/garm 0700 garm garm -",
		// Without these the Incus CLI cannot create its config directory inside
		// the warm-pool unit's read-only filesystem, and the host probe reports
		// Incus missing on a host where Incus is installed and healthy.
		"d /var/lib/garm/.config 0700 garm garm -",
		"d /var/lib/garm/.config/incus 0700 garm garm -",
		"d /var/lib/gha-fleet 0700 garm garm -",
		"d /var/lib/gha-fleet/diagnostics 0700 garm garm -",
		"d /var/lib/gha-diagnostic-exporter 0750 root garm -",
		"d /etc/gha-fleet/pki/cache 0700 root root -",
		"d /etc/gha-fleet/trust 0755 root root -",
		"d /etc/gha-fleet/secrets 0700 root root -",
		"d /var/lib/gha-cache 0711 root root -",
		"d /var/lib/gha-cache/rustfs 0700 gha-rustfs gha-rustfs -",
		"d /var/lib/gha-cache/zot 0700 gha-zot gha-zot -",
		// rustfscache.LoadDelivery validates this directory on every one-job
		// cache delivery and refuses anything but mode 0750 owned by root:garm.
		// Nothing in the repository created it, so a host rebuilt from this
		// contract alone failed every delivery on a directory that was never
		// declared anywhere.
		"d /etc/garm/cache 0750 root garm -",
	} {
		if !strings.Contains(tmpfiles, required) {
			t.Errorf("gha-fleet.tmpfiles is missing %q", required)
		}
	}
	if strings.Contains(tmpfiles, "d /etc/gha-fleet 0750 root garm") {
		t.Fatal("gateway cannot traverse a root:garm 0750 /etc/gha-fleet parent")
	}
	if strings.Contains(tmpfiles, "d /etc/gha-fleet/pki/cache 0755") {
		t.Fatal("cache service private keys must not share the public trust-anchor boundary")
	}
}

func TestDiagnosticExporterIsCanaryCredentialBoundAndReadOnlyToSpool(t *testing.T) {
	service := read(t, "gha-diagnostic-exporter.service")
	timer := read(t, "gha-diagnostic-exporter.timer")
	for _, required := range []string{
		"Type=oneshot", "User=root", "Group=garm", "LoadCredential=exporter-config:",
		"LoadCredential=rustfs-access-key:",
		"LoadCredential=rustfs-secret-key:", "LoadCredential=rustfs-ca.pem:",
		"--config /run/credentials/gha-diagnostic-exporter.service/exporter-config", "CapabilityBoundingSet=CAP_DAC_READ_SEARCH",
		"AmbientCapabilities=CAP_DAC_READ_SEARCH", "NoNewPrivileges=true", "ProtectSystem=strict",
		"IPAddressDeny=any", "IPAddressAllow=192.0.2.1/32", "RestrictAddressFamilies=AF_INET",
		"BindReadOnlyPaths=/var/lib/gha-fleet/diagnostics:/run/gha-diagnostic-exporter-source",
		"ReadWritePaths=/var/lib/gha-diagnostic-exporter",
		"LoadCredential=rustfs-ca.pem:/etc/gha-fleet/trust/rustfs-ca.pem",
		"InaccessiblePaths=/etc /var/lib/gha-fleet /var/lib/garm /var/lib/gha-cache",
	} {
		if !strings.Contains(service, required) {
			t.Errorf("gha-diagnostic-exporter.service is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"EnvironmentFile=", "docker.sock rw", "incus/unix.socket rw", "IPAddressAllow=192.0.2.0/24",
		"ReadWritePaths=/var/lib/gha-fleet/diagnostics", "--config /etc/gha-fleet/diagnostic-exporter.yaml",
		"ReadOnlyPaths=/etc/gha-fleet/diagnostic-exporter.yaml",
	} {
		if strings.Contains(service, forbidden) {
			t.Errorf("gha-diagnostic-exporter.service contains forbidden %q", forbidden)
		}
	}
	for _, required := range []string{
		"OnUnitActiveSec=1min", "RandomizedDelaySec=5s", "Persistent=true",
		"Unit=gha-diagnostic-exporter.service", "WantedBy=timers.target",
	} {
		if !strings.Contains(timer, required) {
			t.Errorf("gha-diagnostic-exporter.timer is missing %q", required)
		}
	}
	config, err := diagnosticexport.LoadConfig("../../config/diagnostic-exporter.yaml")
	if err == nil || !strings.Contains(err.Error(), "path must be absolute") {
		t.Fatalf("relative config path did not fail closed: %v", err)
	}
	source, err := os.ReadFile("../../config/diagnostic-exporter.yaml")
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(t.TempDir(), "diagnostic-exporter.yaml")
	if err := os.WriteFile(installed, source, 0o640); err != nil {
		t.Fatal(err)
	}
	config, err = diagnosticexport.LoadConfig(installed)
	if err != nil {
		t.Fatal(err)
	}
	if config.SchemaVersion != 3 || config.DeploymentStage != "canary" || config.Endpoint != "https://192.0.2.1:9002" ||
		config.Bucket != "gha-diagnostics-canary" ||
		!slices.Equal(config.Repositories, []string{"NDDev-OpenNetwork/github-actions", "example-guild/ai_stp"}) ||
		!slices.Equal(config.AccountScopes, []string{"NDDev-OpenNetwork", "example-media"}) ||
		config.SourceDirectory != "/run/gha-diagnostic-exporter-source" || config.SourceOwner != "garm" ||
		!config.PathStyle {
		t.Fatalf("diagnostic exporter config drifted: %#v", config)
	}
	// The exporter refuses a bundle whose manifest names a pool it does not
	// list, so a class that is published and missing here exports nothing and
	// fails the unit -- which fails the observer, because the export status is
	// one of its collectors. Observed on gha-runner-1 the first time a worker
	// ran on nddev-linux-fast: "diagnostic manifest is outside the configured
	// repository or pool", one pending bundle, and the timer failing on every
	// tick until the pool was listed.
	//
	// Taken from the published set rather than repeated here, so publishing a
	// class and forgetting its diagnostics is caught before a worker proves it.
	listed := make(map[string]struct{}, len(config.Pools))
	for _, pool := range config.Pools {
		listed[pool] = struct{}{}
	}
	for _, class := range garmbootstrap.PublishedScaleSets() {
		if _, covered := listed[class.Flavor]; !covered {
			t.Errorf("scale set %q is published and the diagnostic exporter does not list its pool", class.Name)
		}
		delete(listed, class.Flavor)
	}
	for pool := range listed {
		t.Errorf("the diagnostic exporter lists pool %q, which no published class serves", pool)
	}
}

func TestCacheServicesAreCredentialBoundAndNetworkScoped(t *testing.T) {
	rustfs := read(t, "gha-rustfs.service")
	zot := read(t, "gha-zot.service")
	for _, required := range []string{
		"After=network-online.target incus.service", "Requires=incus.service",
		"User=gha-rustfs", "LoadCredential=rustfs-access-key:", "LoadCredential=rustfs-secret-key:",
		"LoadCredential=rustfs-rpc-secret:", "ExecStart=/usr/local/libexec/gha-fleet/rustfs-launcher",
		"ExecStartPre=/usr/local/libexec/gha-fleet/cache-network-ready gha0 120",
		"LoadCredential=rustfs_cert.pem:", "LoadCredential=rustfs_key.pem:",
		"RUSTFS_ACCESS_KEY_FILE=/run/credentials/gha-rustfs.service/rustfs-access-key",
		"RUSTFS_SECRET_KEY_FILE=/run/credentials/gha-rustfs.service/rustfs-secret-key",
		"RUSTFS_TLS_PATH=/run/credentials/gha-rustfs.service",
		"RUSTFS_ADDRESS=192.0.2.1:9002", "RUSTFS_CONSOLE_ENABLE=false", "RUSTFS_SFTP_ENABLE=false",
		"RUSTFS_FTPS_ENABLE=false", "RUSTFS_WEBDAV_ENABLE=false", "RUSTFS_SWIFT_ENABLE=false",
		"RUSTFS_OBS_ENVIRONMENT=production", "RUSTFS_OBS_LOGGER_LEVEL=warn",
		"RUSTFS_OBS_LOG_MAX_TOTAL_SIZE_BYTES=536870912", "RUSTFS_OBS_LOG_MAX_SINGLE_FILE_SIZE_BYTES=67108864",
		"RUSTFS_OBS_LOG_KEEP_FILES=48", "RUSTFS_OBS_LOG_CLEANUP_INTERVAL_SECONDS=900",
		"RUSTFS_OBS_TRACES_EXPORT_ENABLED=false", "RUSTFS_OBS_METRICS_EXPORT_ENABLED=false",
		"RUSTFS_OBS_LOGS_EXPORT_ENABLED=false", "RUSTFS_OBS_PROFILING_EXPORT_ENABLED=false",
		"ProtectSystem=strict", "NoNewPrivileges=true", "CapabilityBoundingSet=", "IPAddressDeny=any",
		"IPAddressAllow=192.0.2.0/24", "RestrictAddressFamilies=AF_INET AF_NETLINK",
		"ReadWritePaths=/var/lib/gha-cache/rustfs", "Restart=always", "TimeoutStartSec=150s",
	} {
		if !strings.Contains(rustfs, required) {
			t.Errorf("gha-rustfs.service is missing %q", required)
		}
	}
	for _, required := range []string{
		"After=network-online.target incus.service", "Requires=incus.service",
		"User=gha-zot", "LoadCredential=zot-cert.pem:", "LoadCredential=zot-key.pem:",
		"LoadCredential=zot.htpasswd:", "ExecStart=/usr/local/bin/zot serve /etc/gha-fleet/zot/config.json",
		"ExecStartPre=/usr/local/libexec/gha-fleet/cache-network-ready gha0 120",
		"ProtectSystem=strict", "NoNewPrivileges=true", "CapabilityBoundingSet=", "IPAddressDeny=any",
		"IPAddressAllow=192.0.2.0/24", "ReadWritePaths=/var/lib/gha-cache/zot",
		"Restart=always", "TimeoutStartSec=150s",
	} {
		if !strings.Contains(zot, required) {
			t.Errorf("gha-zot.service is missing %q", required)
		}
	}
	for name, unit := range map[string]string{"RustFS": rustfs, "Zot": zot} {
		if strings.Contains(unit, "docker.sock") || strings.Contains(unit, "EnvironmentFile=") {
			t.Fatalf("%s unit exposes a forbidden socket or ambient environment file", name)
		}
	}
	if strings.Contains(zot, "AF_NETLINK") {
		t.Fatal("Zot must not receive RustFS's interface-discovery exception")
	}
}

func TestCacheNetworkReadinessProbeIsBoundedAndSecretFree(t *testing.T) {
	data, err := os.ReadFile("../../scripts/cache-network-ready.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, required := range []string{
		"set -euo pipefail", "[[ $# -ne 2 ]]", "timeout_seconds > 300",
		"/sys/class/net/${interface}", "sleep 1", "exit 1",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("cache-network-ready.sh is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"eval", "source ", "/etc/", "/var/lib/", "CREDENTIALS_DIRECTORY", "curl ", "wget ", "ip ",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("cache-network-ready.sh contains forbidden %q", forbidden)
		}
	}
}

func TestZotConfigurationIsTLSAuthenticatedMinimalAndDefaultDeny(t *testing.T) {
	raw := read(t, "zot.json")
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config zotConfig
	if err := decoder.Decode(&config); err != nil {
		t.Fatal(err)
	}
	if config.DistSpecVersion != "1.1.1" || config.Storage.RootDirectory != "/var/lib/gha-cache/zot/data" ||
		!config.Storage.GC || config.Storage.GCDelay != "2h" || config.Storage.GCInterval != "1h" ||
		config.Storage.GCTimeWindow != "01:00-08:00" {
		t.Fatalf("unsafe Zot storage contract: %#v", config.Storage)
	}
	if config.HTTP.Address != "192.0.2.1" || config.HTTP.Port != "5001" || config.HTTP.Realm != "nddev-cache" ||
		config.HTTP.TLS.Cert != "/run/credentials/gha-zot.service/zot-cert.pem" ||
		config.HTTP.TLS.Key != "/run/credentials/gha-zot.service/zot-key.pem" ||
		config.HTTP.Auth.HTPasswd.Path != "/run/credentials/gha-zot.service/zot.htpasswd" || config.HTTP.Auth.FailDelay != 2 {
		t.Fatalf("unsafe Zot HTTP contract: %#v", config.HTTP)
	}
	if config.Extensions != nil {
		t.Fatal("minimal Zot configuration must omit extensions entirely")
	}
	if config.Log.Level != "error" {
		t.Fatalf("Zot log level = %q, want error to suppress expected minimal-build warnings", config.Log.Level)
	}
	repositories := config.HTTP.AccessControl.Repositories
	if len(repositories) != 4 {
		t.Fatalf("Zot must expose exactly three scoped namespaces and the catch-all deny: %#v", repositories)
	}
	catchAll, ok := config.HTTP.AccessControl.Repositories["**"]
	if !ok || len(catchAll.DefaultPolicy) != 0 || len(catchAll.Policies) != 0 {
		t.Fatalf("catch-all namespace is not default-deny: %#v", catchAll)
	}
	trusted := repositories["cache/example-user/github-actions/trusted/**"]
	untrusted := repositories["cache/example-user/github-actions/untrusted/**"]
	promoted := repositories["cache/example-user/github-actions/promoted/**"]
	for name, namespace := range map[string]zotRepositoryPolicy{
		"trusted": trusted, "untrusted": untrusted, "promoted": promoted,
	} {
		if len(namespace.DefaultPolicy) != 0 || len(namespace.Policies) == 0 {
			t.Fatalf("%s namespace is not explicit default-deny: %#v", name, namespace)
		}
	}
	if len(trusted.Policies) != 1 ||
		!sameSet(trusted.Policies[0].Users, "gha-zot-example-user-github-actions-trusted") ||
		!sameSet(trusted.Policies[0].Actions, "create", "read", "update", "delete") {
		t.Fatalf("unexpected trusted Zot policy: %#v", trusted.Policies)
	}
	if len(untrusted.Policies) != 1 ||
		!sameSet(untrusted.Policies[0].Users, "gha-zot-example-user-github-actions-untrusted") ||
		!sameSet(untrusted.Policies[0].Actions, "create", "read", "update", "delete") {
		t.Fatalf("unexpected untrusted Zot policy: %#v", untrusted.Policies)
	}
	if len(promoted.Policies) != 2 ||
		!sameSet(promoted.Policies[0].Users, "gha-zot-example-user-github-actions-promoter") ||
		!sameSet(promoted.Policies[0].Actions, "create", "read", "update", "delete") ||
		!sameSet(promoted.Policies[1].Users, "gha-zot-example-user-github-actions-release") ||
		!sameSet(promoted.Policies[1].Actions, "read") {
		t.Fatalf("unexpected promoted Zot policies: %#v", promoted.Policies)
	}
	configuredUsers := []string{
		trusted.Policies[0].Users[0],
		untrusted.Policies[0].Users[0],
		promoted.Policies[0].Users[0],
		promoted.Policies[1].Users[0],
	}
	managedIdentities := zotcredentials.Identities()
	managedUsers := make([]string, 0, len(managedIdentities))
	for _, identity := range managedIdentities {
		managedUsers = append(managedUsers, identity.Username)
	}
	if !sameSet(configuredUsers, managedUsers...) {
		t.Fatalf("Zot policy users and managed credential users diverged: policy=%v managed=%v", configuredUsers, managedUsers)
	}
	for _, forbidden := range []string{"gha-buildkit", "gha-cache-reader", `"cache/**"`, `"adminPolicy"`} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("Zot configuration retains broad or administrative policy %q", forbidden)
		}
	}
}

func TestCacheSmokeScriptsKeepCredentialsOutOfProcessArguments(t *testing.T) {
	tests := []struct {
		relative string
		required []string
	}{
		{"../../scripts/rustfs-smoke.sh", []string{"sha256sum", "systemctl kill", "--signal=KILL"}},
		{"../../scripts/rustfs-iam-smoke.sh", []string{"add-canned-policy", "add-user", "policy/attach", "remove-user"}},
		{"../../scripts/rustfs-scoped-smoke.sh", []string{"sha256sum", "denied_bucket", "cross-namespace"}},
		{"../../scripts/rustfs-quota-smoke.sh", []string{"quota-stats", "quota_type", "over-quota", "anonymous deny", "QUOTA_READY_TIMEOUT_SECONDS"}},
		{"../../scripts/rustfs-lifecycle-smoke.sh", []string{"LIFECYCLE_AUDIT_ACCELERATED", "expire-audit-prefix", "negative-control", "anonymous deny"}},
		{"../../scripts/rustfs-diagnostic-exporter-bootstrap.sh", []string{
			"gha-diagnostics-exporter", "diagnostics/v1", "s3:PutObject", "s3:GetObject",
			"set-user-or-group-policy", "quota_type", "LifecycleConfiguration", "cross-bucket", "anonymous",
		}},
		{"../../scripts/rustfs-launcher.sh", []string{"CREDENTIALS_DIRECTORY", "rustfs-rpc-secret", "RUSTFS_RPC_SECRET"}},
		{"../../scripts/cache-network-ready.sh", []string{"/sys/class/net/${interface}", "timeout_seconds > 300", "sleep 1"}},
		{"../../scripts/zot-reproducible-build.sh", []string{"go mod verify", "CGO_ENABLED=0", "GOEXPERIMENT=jsonv2", "release_asset_match"}},
		{"../../scripts/zot-smoke.sh", []string{
			"sha256sum", "systemctl kill", "--signal=KILL", "ZOT_REPOSITORY_PREFIX", "ZOT_DENIED_REPOSITORY",
		}},
		{"../../scripts/zot-storage-audit.sh", []string{
			"dedicated-loopback-ext4", "verify-feature retention", "disk-pressure-filler", "e2fsck", "IPAddressDeny=any",
		}},
	}
	for _, test := range tests {
		relative := test.relative
		data, err := os.ReadFile(relative)
		if err != nil {
			t.Fatal(err)
		}
		script := string(data)
		for _, forbidden := range []string{"set -x", "--user", "--insecure", "-k "} {
			if strings.Contains(script, forbidden) {
				t.Errorf("%s contains forbidden %q", relative, forbidden)
			}
		}
		if !strings.Contains(relative, "rustfs-launcher") && !strings.Contains(relative, "cache-network-ready") &&
			!strings.Contains(relative, "zot-reproducible-build") &&
			!strings.Contains(relative, "zot-storage-audit") &&
			!strings.Contains(script, "--config") {
			t.Errorf("%s is missing %q", relative, "--config")
		}
		if strings.Contains(relative, "diagnostic-exporter") {
			for _, forbidden := range []string{"s3:ListBucket", "s3:GetBucketLocation", "policy/attach"} {
				if strings.Contains(script, forbidden) {
					t.Errorf("%s grants or appends forbidden IAM surface %q", relative, forbidden)
				}
			}
		}
		for _, required := range test.required {
			if !strings.Contains(script, required) {
				t.Errorf("%s is missing %q", relative, required)
			}
		}
	}
}

func TestCacheSystemUsersAreDedicated(t *testing.T) {
	sysusers := read(t, "gha-fleet.sysusers")
	for _, required := range []string{
		"u gha-rustfs - \"GitHub Actions RustFS cache\" /var/lib/gha-cache/rustfs /usr/sbin/nologin",
		"u gha-zot - \"GitHub Actions Zot registry\" /var/lib/gha-cache/zot /usr/sbin/nologin",
	} {
		if !strings.Contains(sysusers, required) {
			t.Errorf("gha-fleet.sysusers is missing %q", required)
		}
	}
}

func containsAll(values []string, wanted ...string) bool {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	for _, value := range wanted {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func sameSet(values []string, wanted ...string) bool {
	return len(values) == len(wanted) && containsAll(values, wanted...)
}

func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(deploymentRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
