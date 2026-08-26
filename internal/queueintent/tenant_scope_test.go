package queueintent

import (
	"context"
	"testing"
	"time"
)

// GitHub scopes a scale set name to one forge entity, so two tenants may each
// serve a scale set called nddev-linux-integration. Answering the provider's
// authorization question from the name alone would let one tenant's admitted
// job authorize the other tenant's VM create, which is a permission the forge
// never granted.
func TestActiveForScaleSetSeparatesAccountsSharingAClassName(t *testing.T) {
	now := time.Date(2026, 8, 12, 14, 0, 0, 0, time.UTC)
	path := writeFixture(t, `{
  "schema_version": 1,
  "generation": 3,
  "updated_at": "2026-08-12T13:59:00Z",
  "intents": {
    "github-scale-set-job:v2:31:31111111-1111-4111-8111-111111111111": {
      "key": "github-scale-set-job:v2:31:31111111-1111-4111-8111-111111111111", "scale_set_id": 31,
      "job_id": "31111111-1111-4111-8111-111111111111", "runner_request_id": 301,
      "scale_set_name": "nddev-linux-integration", "owner": "example-media",
      "repository": "owner/attention",
      "workflow_ref": "owner/attention/.github/workflows/ci.yml@refs/heads/main",
      "event_name": "push", "queue_time": "2026-08-12T13:58:00Z",
      "state": "acquiring", "priority": 1, "updated_at": "2026-08-12T13:59:00Z",
      "expires_at": "2026-08-12T14:10:00Z"
    }
  },
  "terminal_jobs": {},
  "repositories": {
    "owner/attention": {"repository": "owner/attention", "weight": 1, "pass": 1}
  }
}`)
	reader := Reader{Path: path, Now: func() time.Time { return now }}

	authorized, err := reader.ActiveForScaleSet(context.Background(), "example-media", "nddev-linux-integration")
	if err != nil || !authorized {
		t.Fatalf("the tenant that owns the intent was refused: %t, %v", authorized, err)
	}
	authorized, err = reader.ActiveForScaleSet(context.Background(), "example-org", "nddev-linux-integration")
	if err != nil {
		t.Fatal(err)
	}
	if authorized {
		t.Fatal("one tenant's admitted intent authorized another tenant's identical class name")
	}
}

func TestRepositoryForScaleSetNarrowsOrganizationIntent(t *testing.T) {
	reader := Reader{Path: writeFixture(t, `{
  "schema_version":1,"generation":1,"updated_at":"2026-01-01T00:00:00Z",
  "intents":{"github-scale-set-job:v2:2:22222222-2222-4222-8222-222222222222":{"key":"github-scale-set-job:v2:2:22222222-2222-4222-8222-222222222222","scale_set_id":2,"job_id":"22222222-2222-4222-8222-222222222222","runner_request_id":7,
    "scale_set_name":"nddev-linux-standard","owner":"example-org",
    "repository":"example-org/example-actions","workflow_ref":"x","event_name":"workflow_dispatch",
    "queue_time":"2026-01-01T00:00:00Z","state":"acquired","priority":1,
    "updated_at":"2026-01-01T00:00:00Z","expires_at":"2099-01-01T00:00:00Z"}},
  "repositories":{"example-org":{"repository":"example-org","weight":1,"pass":1}},
  "terminal_jobs":{}
}`)}
	repository, err := reader.RepositoryForScaleSet(context.Background(), "example-org", "nddev-linux-standard")
	if err != nil {
		t.Fatal(err)
	}
	if repository != "example-org/example-actions" {
		t.Fatalf("repository = %q", repository)
	}
}

// A journal written before the tenant field existed described the fleet's own
// tenant, so reading it must keep meaning that rather than becoming unowned.
func TestIntentWithoutOwnerReadsAsTheFleetsOwn(t *testing.T) {
	if got := (Intent{}).OwnerAccount(); got != "example-org" {
		t.Fatalf("absent owner = %q, want the fleet's own account", got)
	}
	if got := (Intent{Owner: "example-media"}).OwnerAccount(); got != "example-media" {
		t.Fatalf("declared owner = %q", got)
	}
}
