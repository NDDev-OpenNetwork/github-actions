package deploycontract

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	fleetconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

func TestQueueAdmissionDeploymentIsBoundedAndWiredBeforeAcquireJobs(t *testing.T) {
	configData, err := os.ReadFile("../../deploy/fleet-host/queue-admission.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		SchemaVersion          int `json:"schema_version"`
		MaxInFlight            int `json:"max_in_flight"`
		DefaultRepositoryLimit int `json:"default_repository_limit"`
		DefaultWeight          int `json:"default_weight"`
		QueuedTTLSeconds       int `json:"queued_ttl_seconds"`
		AcquiringTTLSeconds    int `json:"acquiring_ttl_seconds"`
		AcquiredTTLSeconds     int `json:"acquired_ttl_seconds"`
		ExecutionTTLSeconds    int `json:"execution_ttl_seconds"`
		PriorityAgingSeconds   int `json:"priority_aging_seconds"`
		Repositories           map[string]struct {
			Weight      int `json:"weight"`
			MaxInFlight int `json:"max_in_flight"`
		} `json:"repositories"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(configData)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("queue admission config has trailing data: %v", err)
	}
	// The queue's width is a capacity decision and moves with the fleet; the
	// TTL and aging policy is not, and drifting it silently changes how long a
	// stuck job holds a slot.
	if config.SchemaVersion != 1 || config.DefaultWeight != 1 ||
		config.QueuedTTLSeconds != 600 || config.AcquiringTTLSeconds != 120 ||
		config.AcquiredTTLSeconds != 600 || config.ExecutionTTLSeconds != 86400 || config.PriorityAgingSeconds != 300 {
		t.Fatalf("queue admission policy drifted: %#v", config)
	}
	// queueMaxInFlightCeiling in the GARM overlay. Restated rather than
	// imported because the overlay is not part of this module.
	const widthCeiling = 64
	if config.MaxInFlight < 1 || config.MaxInFlight > widthCeiling {
		t.Fatalf("queue width %d is outside 1..%d", config.MaxInFlight, widthCeiling)
	}
	if config.DefaultRepositoryLimit < 1 || config.DefaultRepositoryLimit > config.MaxInFlight {
		t.Fatalf("default repository limit %d is outside 1..%d", config.DefaultRepositoryLimit, config.MaxInFlight)
	}
	for repository, policy := range config.Repositories {
		if policy.Weight < 1 || policy.Weight > 100 {
			t.Fatalf("repository %q has weight %d, outside 1..100", repository, policy.Weight)
		}
		if policy.MaxInFlight < 1 || policy.MaxInFlight > config.MaxInFlight {
			t.Fatalf("repository %q is allowed %d in flight on a %d-wide queue", repository, policy.MaxInFlight, config.MaxInFlight)
		}
	}

	// The queue may not promise more concurrency than the fleet can actually
	// run. It admits jobs before any provider sees them, so a width above the
	// Incus instance ceiling would admit work that then waits on capacity that
	// does not exist -- which is how the fleet spent seven days answering
	// pool-saturated to jobs it had already told GitHub it would take.
	//
	// The ceiling is fleet-wide because the queue is: one queue stands in front
	// of every cluster member, and which member runs a given worker is the
	// placement scriptlet's decision, not this one's.
	host, err := fleetconfig.Load("../../config/server-gha-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxInFlight > host.Incus.FleetMaxInstances() {
		t.Fatalf("queue admits %d in flight but the fleet holds %d instances across %d cluster members",
			config.MaxInFlight, host.Incus.FleetMaxInstances(), host.Incus.ClusterMembers())
	}

	serviceData, err := os.ReadFile("../../deploy/fleet-host/garm.service")
	if err != nil {
		t.Fatal(err)
	}
	service := string(serviceData)
	for _, required := range []string{
		"Environment=GARM_NDDEV_QUEUE_ADMISSION_CONFIG=/etc/garm/queue-admission.json",
		"Environment=GARM_NDDEV_QUEUE_INTENT_FILE=/var/lib/gha-fleet/queue-intents.json",
		"Environment=GARM_NDDEV_QUEUE_INTENT_LOCK_FILE=/var/lib/gha-fleet/queue-intents.lock",
		"ReadOnlyPaths=/etc/garm /etc/gha-fleet /usr/local/libexec/gha-fleet",
		"ReadWritePaths=/var/lib/garm /var/lib/gha-fleet",
	} {
		if !strings.Contains(service, required) {
			t.Errorf("garm.service is missing %q", required)
		}
	}
	providerData, err := os.ReadFile("../../deploy/fleet-host/provider-incus.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(providerData), `queue_intent_file = "/var/lib/gha-fleet/queue-intents.json"`) {
		t.Fatal("provider does not consume the exact GARM-owned queue-intent journal")
	}
}
