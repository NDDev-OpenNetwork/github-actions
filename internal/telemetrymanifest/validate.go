package telemetrymanifest

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var (
	filePattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
	shaPattern            = regexp.MustCompile(`^[0-9a-f]{64}$`)
	streamPattern         = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)
	versionPattern        = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	releaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*)?$`)
	bucketPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,62}$`)
)

type Issue struct {
	Path    string
	Message string
}

type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		parts = append(parts, issue.Path+": "+issue.Message)
	}
	return "invalid telemetry manifest: " + strings.Join(parts, "; ")
}

func (m Manifest) Validate() error {
	issues := make([]Issue, 0)
	add := func(field, message string) { issues = append(issues, Issue{field, message}) }

	if m.SchemaVersion != 1 {
		add("schema_version", "must be 1")
	}
	validateCollector(add, m.Collector)
	validateStore(add, m.Store)
	validateTransport(add, m.Transport)

	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path == issues[j].Path {
			return issues[i].Message < issues[j].Message
		}
		return issues[i].Path < issues[j].Path
	})
	return &ValidationError{Issues: issues}
}

func validateCollector(add func(string, string), collector Collector) {
	if collector.Implementation != "opentelemetry-collector-contrib" {
		add("collector.implementation", "must be opentelemetry-collector-contrib")
	}
	if collector.Repository != "open-telemetry/opentelemetry-collector-releases" {
		add("collector.repository", "must be the official collector release repository")
	}
	if !versionPattern.MatchString(collector.Version) {
		add("collector.version", "must be an exact vMAJOR.MINOR.PATCH version")
		return
	}
	if collector.Architecture != "linux_amd64" {
		add("collector.architecture", "must be linux_amd64")
	}
	if collector.DeploymentStage != "canary" && collector.DeploymentStage != "production" {
		add("collector.deployment_stage", "must be canary or production")
	}
	if collector.BinaryPath != "otelcol-contrib" {
		add("collector.binary_path", "must be the exact extracted collector executable")
	}

	// Every asset upstream publishes for this release is pinned, so a build
	// cannot be accepted on the strength of its archive digest alone.
	base := "otelcol-contrib_" + strings.TrimPrefix(collector.Version, "v") + "_linux_amd64.tar.gz"
	release := "/open-telemetry/opentelemetry-collector-releases/releases/download/" + collector.Version + "/"
	for field, expected := range map[string]struct {
		asset Asset
		name  string
	}{
		"collector.archive":         {collector.Archive, base},
		"collector.checksum":        {collector.Checksum, base + ".sha256"},
		"collector.sbom":            {collector.SBOM, base + ".sbom.json"},
		"collector.sigstore_bundle": {collector.SigstoreBundle, base + ".sigstore.json"},
	} {
		validateAsset(add, field, expected.asset, "github.com", release+expected.name, expected.name)
	}
}

func validateStore(add func(string, string), store Store) {
	if store.Implementation != "openobserve" {
		add("store.implementation", "must be openobserve")
	}
	if !releaseVersionPattern.MatchString(store.Version) {
		add("store.version", "must be an exact semantic release version")
	}
	if store.BinaryPath != "openobserve" {
		add("store.binary_path", "must be the exact extracted openobserve executable")
	}
	if releaseVersionPattern.MatchString(store.Version) {
		name := "openobserve-" + store.Version + "-linux-amd64.tar.gz"
		validateAsset(add, "store.archive", store.Archive, "downloads.openobserve.ai",
			"/releases/openobserve/"+store.Version+"/"+name, name)
	}
	if store.Host == "" || strings.ContainsAny(store.Host, " /") {
		add("store.host", "must name the host that runs the instance")
	}
	if store.Organization == "" || !streamPattern.MatchString(store.Organization) {
		add("store.organization", "must be a bounded lowercase organization name")
	}
	if store.ObjectStore != "rustfs" {
		add("store.object_store", "must be rustfs")
	}
	if !bucketPattern.MatchString(store.Bucket) {
		add("store.bucket", "must be a bounded lowercase bucket name")
	}
	// A shared bucket would let one tenant's retention policy delete another's
	// telemetry, which is the whole reason this instance is separate.
	if store.Bucket == "myattention-telemetry" {
		add("store.bucket", "must not reuse another tenant's telemetry bucket")
	}
	if store.RetentionDays < 30 || store.RetentionDays > 400 {
		add("store.retention_days", "must be between 30 and 400 days")
	}
	if store.DeploymentStage != "canary" && store.DeploymentStage != "production" {
		add("store.deployment_stage", "must be canary or production")
	}
	if !slices.Equal(store.Streams, FleetStreams()) {
		add("store.streams", "must be exactly "+strings.Join(FleetStreams(), ", "))
	}
	for index, stream := range store.Streams {
		if !streamPattern.MatchString(stream) {
			add(fmt.Sprintf("store.streams[%d]", index), "must be a bounded lowercase stream name")
		}
	}
	for stream, days := range store.StreamRetentionDays {
		if !slices.Contains(store.Streams, stream) {
			add("store.stream_retention_days."+stream, "must name a declared stream")
			continue
		}
		// Seven days is the shortest window in which a failure reported on a
		// Monday can still be read on the following Monday.
		if days < 7 || days > store.RetentionDays {
			add(fmt.Sprintf("store.stream_retention_days.%s", stream),
				fmt.Sprintf("must be between 7 and the store default of %d days", store.RetentionDays))
		}
	}
}

func validateTransport(add func(string, string), transport Transport) {
	if transport.Protocol != "otlp-http" {
		add("transport.protocol", "must be otlp-http")
	}
	for field, address := range map[string]string{
		"transport.source_address": transport.SourceAddress,
		"transport.target_address": transport.TargetAddress,
	} {
		parsed := net.ParseIP(address)
		if parsed == nil || parsed.To4() == nil {
			add(field, "must be an IPv4 address")
			continue
		}
		// Telemetry crosses hosts but must never cross the private network.
		if !parsed.IsPrivate() {
			add(field, "must be a private address; telemetry must not leave the private network")
		}
	}
	if transport.SourceAddress == transport.TargetAddress {
		add("transport", "source and target must be different hosts")
	}
	if transport.TargetPort < 1024 || transport.TargetPort > 65535 {
		add("transport.target_port", "must be an unprivileged port")
	}
	// The queue is what makes a store outage survivable rather than lossy.
	if !path.IsAbs(transport.QueueDirectory) || path.Clean(transport.QueueDirectory) != transport.QueueDirectory {
		add("transport.queue_directory", "must be an absolute cleaned path")
	}
	validateDurability(add, transport.Durability)
}

func validateDurability(add func(string, string), durability Durability) {
	if durability.RPO != "zero-record-loss-within-tested-outage-envelope" {
		add("transport.durability.rpo", "must state the bounded zero-loss objective")
	}
	if durability.BackendOutageMinutes != 60 || durability.RecoveryRTOMinutes != 15 {
		add("transport.durability", "must declare the tested 60-minute outage and 15-minute recovery envelope")
	}
	if durability.RetryMaxElapsedTime != "0s" || durability.QueueSizeBatchesPerSignal != 10000 ||
		!durability.BlockOnOverflow || durability.PersistentStorage != "file_storage" {
		add("transport.durability", "must use indefinite retry, bounded persistent queues and overflow backpressure")
	}
	if durability.AcceptedLossOutside != "none-silent" || strings.TrimSpace(durability.Note) == "" {
		add("transport.durability.accepted_loss_outside_envelope", "must reject silent loss and document storage exhaustion")
	}
	wanted := []string{
		"otelcol_exporter_enqueue_failed",
		"otelcol_exporter_queue_capacity",
		"otelcol_exporter_queue_size",
		"otelcol_exporter_send_failed",
		"otelcol_receiver_refused",
	}
	actual := append([]string(nil), durability.RequiredSelfMetrics...)
	sort.Strings(actual)
	if !slices.Equal(actual, wanted) {
		add("transport.durability.required_self_metrics", "must contain the exact queue, failure and refusal signals")
	}
}

func validateAsset(add func(string, string), field string, asset Asset, host, wantedPath, wantedName string) {
	if asset.Name != wantedName || !filePattern.MatchString(asset.Name) || path.Base(asset.Name) != asset.Name {
		add(field+".name", "must be the pinned asset filename "+wantedName)
	}
	if !shaPattern.MatchString(asset.SHA256) {
		add(field+".sha256", "must be a lowercase SHA-256 digest")
	}
	parsed, err := url.Parse(asset.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != host ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != wantedPath {
		add(field+".url", "must exactly match the pinned release asset on "+host)
	}
	if strings.Contains(asset.URL, "/latest/") || strings.Contains(asset.URL, "latest.") {
		add(field+".url", "must not reference a mutable release alias")
	}
}
