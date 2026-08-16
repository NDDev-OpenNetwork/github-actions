package provideradmission

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
)

const testFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testController(t *testing.T, now *time.Time) Controller {
	t.Helper()
	directory := t.TempDir()
	return Controller{
		Store: providerjournal.Store{
			Path:     filepath.Join(directory, "journal.json"),
			LockPath: filepath.Join(directory, "journal.lock"),
			Now:      func() time.Time { return *now },
		},
		ControllerID: "controller-test",
		Policy: admission.ReservePolicy{
			MinimumCPUUnits:        4,
			MinimumMemoryMiB:       16 * 1024,
			MinimumPercent:         10,
			MinimumFreeDiskPercent: 20,
		},
		LeaseTTL: 5 * time.Minute,
		Now:      func() time.Time { return *now },
	}
}

func healthyHost() admission.HostSnapshot {
	return admission.HostSnapshot{
		Healthy:            true,
		TotalCPUUnits:      8,
		TotalMemoryMiB:     32 * 1024,
		AvailableMemoryMiB: 32 * 1024,
		FreeDiskPercent:    60,
	}
}

func integrationRequest(name string) Request {
	value := request(name)
	value.PoolID = "pool-integration"
	value.PoolName = "nddev-linux-integration"
	value.ImageFingerprint = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	return value
}

func TestColdAdmissionAtomicallyReservesWarmPreemption(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	warm := warmAllocation("warm-standard-a")
	host := healthyHost()
	host.AvailableMemoryMiB = 22 * 1024

	result, err := controller.AdmitPreemptible(context.Background(), host, []Allocation{warm}, integrationRequest("runner-integration"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Decision.Admitted || len(result.PreemptedWarmWorkers) != 1 || result.PreemptedWarmWorkers[0] != warm.InstanceName {
		t.Fatalf("preemption result=%#v", result)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Leases[warm.InstanceName].State != providerjournal.StateWarmReady ||
		journal.Leases[warm.InstanceName].PreemptedBy != "runner-integration" ||
		journal.Leases["runner-integration"].State != providerjournal.StateAdmitted ||
		journal.WarmPreemptionsTotal != 1 {
		t.Fatalf("preemption was not atomic: %#v", journal)
	}

	claim, err := controller.ClaimWarm(context.Background(), []Allocation{warm}, warmClaim("competing-job"))
	if err != nil {
		t.Fatal(err)
	}
	if claim.Found {
		t.Fatalf("preempted warm worker was claimed: %#v", claim)
	}

	repeated, err := controller.AdmitPreemptible(context.Background(), host, []Allocation{warm}, integrationRequest("runner-integration"))
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.Decision.Admitted || len(repeated.PreemptedWarmWorkers) != 1 || repeated.PreemptedWarmWorkers[0] != warm.InstanceName {
		t.Fatalf("retry lost its durable preemption: %#v", repeated)
	}
	journal, err = controller.Store.Read(context.Background())
	if err != nil || journal.WarmPreemptionsTotal != 1 {
		t.Fatalf("retry incremented durable preemption counter: journal=%#v err=%v", journal, err)
	}

	host.Healthy = false
	cleanupRetry, err := controller.AdmitPreemptible(context.Background(), host, []Allocation{warm}, integrationRequest("runner-integration"))
	if err != nil {
		t.Fatal(err)
	}
	if cleanupRetry.Decision.Admitted || cleanupRetry.Decision.Reason != admission.ReasonHostUnhealthy ||
		len(cleanupRetry.PreemptedWarmWorkers) != 1 || cleanupRetry.PreemptedWarmWorkers[0] != warm.InstanceName {
		t.Fatalf("unhealthy retry lost teardown ownership: %#v", cleanupRetry)
	}
}

func TestQueueIntentAuthorizesPreparingWarmPreemption(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	preparing := warmAllocation("warm-standard-preparing")
	preparing.State = providerjournal.StateCreated
	host := healthyHost()
	host.AvailableMemoryMiB = 22 * 1024

	withoutIntent := testController(t, &now)
	requestWithoutIntent := integrationRequest("runner-without-intent")
	result, err := withoutIntent.AdmitPreemptible(context.Background(), host, []Allocation{preparing}, requestWithoutIntent)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Admitted || len(result.PreemptedWarmWorkers) != 0 {
		t.Fatalf("preparing warm capacity was preempted without queue intent: %#v", result)
	}

	withIntent := testController(t, &now)
	requestWithIntent := integrationRequest("runner-with-intent")
	requestWithIntent.QueueIntentAuthorized = true
	result, err = withIntent.AdmitPreemptible(context.Background(), host, []Allocation{preparing}, requestWithIntent)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Decision.Admitted || len(result.PreemptedWarmWorkers) != 1 || result.PreemptedWarmWorkers[0] != preparing.InstanceName {
		t.Fatalf("queue-authorized preparing preemption = %#v", result)
	}
	journal, err := withIntent.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Leases[preparing.InstanceName].State != providerjournal.StateCreated ||
		journal.Leases[preparing.InstanceName].PreemptedBy != requestWithIntent.InstanceName ||
		journal.WarmPreemptionsTotal != 1 {
		t.Fatalf("preparing preemption was not durably owned: %#v", journal)
	}
	if err := withIntent.Release(context.Background(), requestWithIntent.InstanceName); err != nil {
		t.Fatal(err)
	}
	journal, err = withIntent.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease := journal.Leases[preparing.InstanceName]
	if lease.State != providerjournal.StateCreated || lease.PreemptedBy != "" {
		t.Fatalf("released preparing preemption lost its observed lifecycle: %#v", journal)
	}
}

func TestColdAdmissionFailsClosedOnPreemptionCounterOverflow(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	if _, err := controller.Store.Update(context.Background(), func(journal *providerjournal.Journal) error {
		journal.WarmPreemptionsTotal = ^uint64(0)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	warm := warmAllocation("warm-standard-a")
	if _, err := controller.AdmitPreemptible(context.Background(), healthyHost(), []Allocation{warm}, integrationRequest("runner-integration")); err == nil || !strings.Contains(err.Error(), "counter overflow") {
		t.Fatalf("overflow admission error = %v", err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.WarmPreemptionsTotal != ^uint64(0) || len(journal.Leases) != 0 {
		t.Fatalf("overflow mutated journal: %#v", journal)
	}
}

func TestColdAdmissionDoesNotPreemptClaimedOrSamePoolWarmCapacity(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	warm := warmAllocation("warm-standard-a")
	if claim, err := controller.ClaimWarm(context.Background(), []Allocation{warm}, warmClaim("existing-job")); err != nil || !claim.Found {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}

	result, err := controller.AdmitPreemptible(context.Background(), healthyHost(), []Allocation{warm}, integrationRequest("runner-integration"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Admitted || len(result.PreemptedWarmWorkers) != 0 {
		t.Fatalf("claimed warm capacity was preempted: %#v", result)
	}
	if _, err := controller.Store.Update(context.Background(), func(journal *providerjournal.Journal) error {
		lease := journal.Leases[warm.InstanceName]
		lease.State = providerjournal.StateCreated
		journal.Leases[warm.InstanceName] = lease
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	createdClaimed := warm
	createdClaimed.State = providerjournal.StateCreated
	requestWithIntent := integrationRequest("runner-queue-authorized")
	requestWithIntent.QueueIntentAuthorized = true
	result, err = controller.AdmitPreemptible(context.Background(), healthyHost(), []Allocation{createdClaimed}, requestWithIntent)
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Admitted || len(result.PreemptedWarmWorkers) != 0 {
		t.Fatalf("queue-authorized admission preempted a claimed created lease: %#v", result)
	}

	other := testController(t, &now)
	result, err = other.AdmitPreemptible(context.Background(), healthyHost(), []Allocation{warm}, request("runner-standard"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision.Admitted || len(result.PreemptedWarmWorkers) != 0 || result.Decision.Reason != admission.ReasonPoolSaturated {
		t.Fatalf("same-pool warm capacity was preempted: %#v", result)
	}
}

func TestReleasePreemptionTargetRestoresObservedWarmLease(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	warm := warmAllocation("warm-standard-a")
	result, err := controller.AdmitPreemptible(context.Background(), healthyHost(), []Allocation{warm}, integrationRequest("runner-integration"))
	if err != nil || !result.Decision.Admitted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if err := controller.Release(context.Background(), "runner-integration"); err != nil {
		t.Fatal(err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease := journal.Leases[warm.InstanceName]
	if lease.State != providerjournal.StateWarmReady || lease.PreemptedBy != "" {
		t.Fatalf("warm lease was not restored: %#v", journal)
	}
}

func TestExpiredUnobservedPreemptionTargetRestoresWarmLease(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	warm := warmAllocation("warm-standard-a")
	result, err := controller.AdmitPreemptible(context.Background(), healthyHost(), []Allocation{warm}, integrationRequest("runner-integration"))
	if err != nil || !result.Decision.Admitted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	now = now.Add(6 * time.Minute)
	if err := controller.Reconcile(context.Background(), []Allocation{warm}); err != nil {
		t.Fatal(err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := journal.Leases["runner-integration"]; exists {
		t.Fatalf("expired request survived: %#v", journal)
	}
	lease := journal.Leases[warm.InstanceName]
	if lease.State != providerjournal.StateWarmReady || lease.PreemptedBy != "" {
		t.Fatalf("expired reservation did not restore warm lease: %#v", journal)
	}
}

func TestExpiredTargetRestoresPreparingVictimRegardlessOfLexicalOrder(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	preparing := warmAllocation("a-warm-preparing")
	preparing.State = providerjournal.StateCreated
	target := integrationRequest("z-runner-integration")
	target.QueueIntentAuthorized = true
	host := healthyHost()
	host.AvailableMemoryMiB = 22 * 1024
	result, err := controller.AdmitPreemptible(context.Background(), host, []Allocation{preparing}, target)
	if err != nil || !result.Decision.Admitted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	now = now.Add(6 * time.Minute)
	if err := controller.Reconcile(context.Background(), []Allocation{preparing}); err != nil {
		t.Fatal(err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := journal.Leases[target.InstanceName]; exists {
		t.Fatalf("expired target survived: %#v", journal)
	}
	lease := journal.Leases[preparing.InstanceName]
	if lease.State != providerjournal.StateCreated || lease.PreemptedBy != "" {
		t.Fatalf("preparing victim was not restored exactly: %#v", journal)
	}
}

func TestReleaseFailsClosedAfterVictimTeardownStarts(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	warm := warmAllocation("warm-standard-a")
	result, err := controller.AdmitPreemptible(context.Background(), healthyHost(), []Allocation{warm}, integrationRequest("runner-integration"))
	if err != nil || !result.Decision.Admitted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if err := controller.MarkDeleting(context.Background(), warm.InstanceName); err != nil {
		t.Fatal(err)
	}
	err = controller.Release(context.Background(), "runner-integration")
	if err == nil || !strings.Contains(err.Error(), "teardown is active") {
		t.Fatalf("release error=%v", err)
	}
	journal, readErr := controller.Store.Read(context.Background())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if journal.Leases[warm.InstanceName].State != providerjournal.StateDeleting ||
		journal.Leases[warm.InstanceName].PreemptedBy != "runner-integration" {
		t.Fatalf("failed release mutated teardown ownership: %#v", journal)
	}
}

func request(name string) Request {
	return Request{
		Allocation: Allocation{
			InstanceName:     name,
			ControllerID:     "controller-test",
			PoolID:           "pool-test",
			PoolName:         "nddev-linux-standard",
			VCPU:             4,
			MemoryMiB:        10 * 1024,
			ImageFingerprint: testFingerprint,
		},
		MaxRunning: 1,
	}
}

func warmAllocation(name string) Allocation {
	allocation := request(name).Allocation
	allocation.PoolID = "warm/nddev-linux-standard"
	allocation.State = providerjournal.StateWarmReady
	return allocation
}

func warmClaim(jobName string) WarmClaimRequest {
	return WarmClaimRequest{
		JobName:          jobName,
		ControllerID:     "controller-test",
		PoolID:           "pool-test",
		PoolName:         "nddev-linux-standard",
		ImageFingerprint: testFingerprint,
	}
}

func TestAdmitPersistsLeaseAndRejectsPoolSaturation(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)

	first, err := controller.Admit(context.Background(), healthyHost(), nil, request("runner-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Admitted || first.Reason != admission.ReasonAdmitted {
		t.Fatalf("first decision=%#v", first)
	}
	second, err := controller.Admit(context.Background(), healthyHost(), nil, request("runner-2"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Admitted || second.Reason != admission.ReasonPoolSaturated {
		t.Fatalf("second decision=%#v", second)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Leases) != 1 || journal.Leases["runner-1"].State != providerjournal.StateAdmitted {
		t.Fatalf("journal=%#v", journal)
	}
}

func TestAdmitIsIdempotentForSameRequest(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	if decision, err := controller.Admit(context.Background(), healthyHost(), nil, request("runner-1")); err != nil || !decision.Admitted {
		t.Fatalf("initial admit decision=%#v err=%v", decision, err)
	}
	now = now.Add(time.Minute)
	decision, err := controller.Admit(context.Background(), healthyHost(), nil, request("runner-1"))
	if err != nil || !decision.Admitted {
		t.Fatalf("idempotent admit decision=%#v err=%v", decision, err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Leases) != 1 || !journal.Leases["runner-1"].ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("idempotent journal=%#v", journal)
	}
}

func TestExpiredUnobservedAdmissionIsReclaimed(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	if decision, err := controller.Admit(context.Background(), healthyHost(), nil, request("runner-1")); err != nil || !decision.Admitted {
		t.Fatalf("initial admit decision=%#v err=%v", decision, err)
	}
	now = now.Add(6 * time.Minute)
	decision, err := controller.Admit(context.Background(), healthyHost(), nil, request("runner-2"))
	if err != nil || !decision.Admitted {
		t.Fatalf("replacement admit decision=%#v err=%v", decision, err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := journal.Leases["runner-1"]; exists {
		t.Fatalf("expired lease survived: %#v", journal)
	}
}

func TestObservedInstanceIsRecoveredAndCounted(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	observed := []Allocation{request("runner-existing").Allocation}

	decision, err := controller.Admit(context.Background(), healthyHost(), observed, request("runner-new"))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Admitted || decision.Reason != admission.ReasonPoolSaturated {
		t.Fatalf("decision=%#v", decision)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease := journal.Leases["runner-existing"]
	if lease.State != providerjournal.StateCreated || len(journal.Leases) != 1 {
		t.Fatalf("recovered journal=%#v", journal)
	}
}

func TestReconcileAdoptsObservedInstanceWithoutNewCapacityDecision(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	observed := request("runner-existing").Allocation

	if err := controller.Reconcile(context.Background(), []Allocation{observed}); err != nil {
		t.Fatal(err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease := journal.Leases[observed.InstanceName]
	if lease.State != providerjournal.StateCreated || lease.ImageFingerprint != observed.ImageFingerprint {
		t.Fatalf("reconciled journal=%#v", journal)
	}
}

func TestForeignObservedInstanceFailsClosed(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	foreign := request("foreign").Allocation
	foreign.ControllerID = "other-controller"
	if _, err := controller.Admit(context.Background(), healthyHost(), []Allocation{foreign}, request("runner")); err == nil {
		t.Fatal("foreign instance was accepted")
	}
}

func TestTransitionsAndReleaseRequireExistingOwnedLease(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	if decision, err := controller.Admit(context.Background(), healthyHost(), nil, request("runner")); err != nil || !decision.Admitted {
		t.Fatalf("admit decision=%#v err=%v", decision, err)
	}
	if err := controller.MarkCreated(context.Background(), "runner"); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkDeleting(context.Background(), "runner"); err != nil {
		t.Fatal(err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Leases["runner"].State != providerjournal.StateDeleting {
		t.Fatalf("journal=%#v", journal)
	}
	if err := controller.Release(context.Background(), "runner"); err != nil {
		t.Fatal(err)
	}
	journal, err = controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Leases) != 0 {
		t.Fatalf("released journal=%#v", journal)
	}
	if err := controller.MarkCreated(context.Background(), "missing"); err == nil {
		t.Fatal("missing lease transition succeeded")
	}
}

func TestConflictingIdempotencyKeyFailsClosed(t *testing.T) {
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	if decision, err := controller.Admit(context.Background(), healthyHost(), nil, request("runner")); err != nil || !decision.Admitted {
		t.Fatalf("admit decision=%#v err=%v", decision, err)
	}
	conflict := request("runner")
	conflict.ImageFingerprint = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, err := controller.Admit(context.Background(), healthyHost(), nil, conflict); err == nil {
		t.Fatal("conflicting request reused an existing lease")
	}
}

func TestWarmClaimIsAtomicDeterministicAndIdempotent(t *testing.T) {
	now := time.Date(2026, time.August, 9, 2, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	observed := []Allocation{warmAllocation("warm-standard-b"), warmAllocation("warm-standard-a")}

	claimed, err := controller.ClaimWarm(context.Background(), observed, warmClaim("garm-runner-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !claimed.Found || claimed.InstanceName != "warm-standard-a" || claimed.State != providerjournal.ClaimReserved {
		t.Fatalf("claim=%#v", claimed)
	}
	now = now.Add(time.Minute)
	repeated, err := controller.ClaimWarm(context.Background(), observed, warmClaim("garm-runner-1"))
	if err != nil {
		t.Fatal(err)
	}
	if repeated != claimed {
		t.Fatalf("repeated claim=%#v, want %#v", repeated, claimed)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Claims) != 1 || journal.Leases["warm-standard-a"].State != providerjournal.StateWarmClaimed ||
		journal.Leases["warm-standard-b"].State != providerjournal.StateWarmReady {
		t.Fatalf("journal=%#v", journal)
	}
}

func TestAuthorizeWarmDrainRequiresExactReadyLeaseAndZeroClaims(t *testing.T) {
	now := time.Date(2026, time.August, 9, 2, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	observed := []Allocation{warmAllocation("warm-standard-1")}
	if err := controller.Reconcile(context.Background(), observed); err != nil {
		t.Fatal(err)
	}
	if err := controller.AuthorizeWarmDrain(context.Background(), "warm-standard-1"); err != nil {
		t.Fatalf("authorize clean ready lease: %v", err)
	}
	if _, err := controller.ClaimWarm(context.Background(), observed, warmClaim("runner-1")); err != nil {
		t.Fatal(err)
	}
	if err := controller.AuthorizeWarmDrain(context.Background(), "warm-standard-1"); err == nil {
		t.Fatal("authorized warm drain while a claim existed")
	}
	if err := controller.AuthorizeWarmDrain(context.Background(), "missing"); err == nil {
		t.Fatal("authorized missing warm lease")
	}
}

func TestAuthorizeWarmDrainRejectsPreemptedReadyLease(t *testing.T) {
	now := time.Date(2026, time.August, 9, 2, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	warm := warmAllocation("warm-standard-1")
	result, err := controller.AdmitPreemptible(context.Background(), healthyHost(), []Allocation{warm}, integrationRequest("runner-integration"))
	if err != nil || !result.Decision.Admitted {
		t.Fatalf("preemption result=%#v err=%v", result, err)
	}
	err = controller.AuthorizeWarmDrain(context.Background(), warm.InstanceName)
	if err == nil || !strings.Contains(err.Error(), "reserved by preemption target") {
		t.Fatalf("preempted warm drain error=%v", err)
	}
}

func TestWarmClaimDoesNotDoubleAllocateUnderConcurrency(t *testing.T) {
	now := time.Date(2026, time.August, 9, 2, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	observed := []Allocation{warmAllocation("warm-standard-a")}
	results := make(chan WarmClaimResult, 2)
	errs := make(chan error, 2)
	for _, job := range []string{"garm-runner-a", "garm-runner-b"} {
		go func(job string) {
			result, err := controller.ClaimWarm(context.Background(), observed, warmClaim(job))
			results <- result
			errs <- err
		}(job)
	}
	claimed := 0
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if result := <-results; result.Found {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("claimed=%d, want exactly one", claimed)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Claims) != 1 || len(journal.Leases) != 1 {
		t.Fatalf("journal=%#v", journal)
	}
}

func TestWarmClaimConvergesAfterIncusOwnershipTransition(t *testing.T) {
	now := time.Date(2026, time.August, 9, 2, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	warm := warmAllocation("warm-standard-a")
	if result, err := controller.ClaimWarm(context.Background(), []Allocation{warm}, warmClaim("garm-runner-1")); err != nil || !result.Found {
		t.Fatalf("claim=%#v err=%v", result, err)
	}
	activated := warm
	activated.PoolID = "pool-test"
	activated.JobName = "garm-runner-1"
	activated.State = providerjournal.StateCreated
	if err := controller.Reconcile(context.Background(), []Allocation{activated}); err != nil {
		t.Fatal(err)
	}
	if err := controller.MarkWarmInjected(context.Background(), "garm-runner-1", warm.InstanceName); err != nil {
		t.Fatal(err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Leases[warm.InstanceName].PoolID != "pool-test" || journal.Leases[warm.InstanceName].State != providerjournal.StateCreated ||
		journal.Claims["garm-runner-1"].State != providerjournal.ClaimInjected {
		t.Fatalf("journal=%#v", journal)
	}
}

func TestWarmClaimNeverReturnsToReadyAfterReservation(t *testing.T) {
	now := time.Date(2026, time.August, 9, 2, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	warm := warmAllocation("warm-standard-a")
	if result, err := controller.ClaimWarm(context.Background(), []Allocation{warm}, warmClaim("garm-runner-1")); err != nil || !result.Found {
		t.Fatalf("claim=%#v err=%v", result, err)
	}
	now = now.Add(10 * time.Minute)
	if err := controller.Reconcile(context.Background(), []Allocation{warm}); err != nil {
		t.Fatal(err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Leases[warm.InstanceName].State != providerjournal.StateWarmClaimed || len(journal.Claims) != 1 {
		t.Fatalf("claimed VM became reusable: %#v", journal)
	}
}

func TestWarmClaimIsRemovedWhenClaimedInstanceIsAbsent(t *testing.T) {
	now := time.Date(2026, time.August, 9, 2, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	warm := warmAllocation("warm-standard-a")
	if result, err := controller.ClaimWarm(context.Background(), []Allocation{warm}, warmClaim("garm-runner-1")); err != nil || !result.Found {
		t.Fatalf("claim=%#v err=%v", result, err)
	}
	if err := controller.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Leases) != 0 || len(journal.Claims) != 0 {
		t.Fatalf("absent claimed instance left durable ownership: %#v", journal)
	}
}

func TestReleaseByGARMNameRemovesWarmLeaseAndClaim(t *testing.T) {
	now := time.Date(2026, time.August, 9, 2, 0, 0, 0, time.UTC)
	controller := testController(t, &now)
	warm := warmAllocation("warm-standard-a")
	if result, err := controller.ClaimWarm(context.Background(), []Allocation{warm}, warmClaim("garm-runner-1")); err != nil || !result.Found {
		t.Fatalf("claim=%#v err=%v", result, err)
	}
	if err := controller.Release(context.Background(), "garm-runner-1"); err != nil {
		t.Fatal(err)
	}
	journal, err := controller.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Leases) != 0 || len(journal.Claims) != 0 {
		t.Fatalf("journal=%#v", journal)
	}
}
