package pool

import (
	"net/http"
	"testing"
	"time"
)

func TestAuthoritativeReconcileBackoff(t *testing.T) {
	t.Parallel()

	if got := authoritativeReconcileBackoff(http.StatusForbidden); got != 15*time.Minute {
		t.Fatalf("403 backoff = %s, want 15m", got)
	}
	if got := authoritativeReconcileBackoff(http.StatusTooManyRequests); got != 15*time.Minute {
		t.Fatalf("429 backoff = %s, want 15m", got)
	}
	if got := authoritativeReconcileBackoff(http.StatusNotFound); got != 0 {
		t.Fatalf("404 backoff = %s, want retain-and-retry rather than access-refusal deferral", got)
	}
	if got := authoritativeReconcileBackoff(http.StatusInternalServerError); got != 0 {
		t.Fatalf("500 backoff = %s, want ordinary per-job retry", got)
	}

	now := time.Unix(1_800_000_000, 0).UTC()
	manager := &basePoolManager{}
	if manager.authoritativeReconcileDeferred(now) {
		t.Fatal("zero-value manager started deferred")
	}
	if !manager.deferAuthoritativeReconcile(now, http.StatusForbidden) {
		t.Fatal("403 did not establish manager-wide backoff")
	}
	if manager.deferAuthoritativeReconcile(now, http.StatusNotFound) {
		t.Fatal("404 must not extend the access-refusal backoff")
	}
	if !manager.authoritativeReconcileDeferred(now.Add(14 * time.Minute)) {
		t.Fatal("manager-wide backoff expired early")
	}
	if manager.authoritativeReconcileDeferred(now.Add(15 * time.Minute)) {
		t.Fatal("manager-wide backoff did not expire at its boundary")
	}
}
