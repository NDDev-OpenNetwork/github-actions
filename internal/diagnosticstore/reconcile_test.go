package diagnosticstore

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
)

type fakeRequester struct {
	bucket    bool
	quota     int64
	usage     int64
	lifecycle []byte
}

func (f *fakeRequester) Do(_ context.Context, _ rustfscache.Credential, method, path, _ string, body []byte) (rustfscache.Response, error) {
	switch {
	case method == http.MethodHead && path == "/example-diagnostics":
		if !f.bucket {
			return rustfscache.Response{StatusCode: http.StatusNotFound}, nil
		}
		return rustfscache.Response{StatusCode: http.StatusOK}, nil
	case method == http.MethodPut && path == "/example-diagnostics":
		f.bucket = true
		return rustfscache.Response{StatusCode: http.StatusOK}, nil
	case method == http.MethodGet && path == "/rustfs/admin/v3/quota/example-diagnostics":
		raw, _ := json.Marshal(quotaInfo{Quota: f.quota, QuotaType: "HARD"})
		return rustfscache.Response{StatusCode: http.StatusOK, Body: raw}, nil
	case method == http.MethodGet && path == "/rustfs/admin/v3/quota-stats/example-diagnostics":
		raw, _ := json.Marshal(quotaStats{Bucket: "example-diagnostics", QuotaLimit: f.quota,
			CurrentUsage: f.usage, RemainingQuota: f.quota - f.usage,
			UsagePercentage: float64(f.usage) * 100 / float64(f.quota)})
		return rustfscache.Response{StatusCode: http.StatusOK, Body: raw}, nil
	case method == http.MethodPut && path == "/rustfs/admin/v3/quota/example-diagnostics":
		var quota quotaInfo
		_ = json.Unmarshal(body, &quota)
		f.quota = quota.Quota
		return rustfscache.Response{StatusCode: http.StatusOK}, nil
	case method == http.MethodGet && path == "/example-diagnostics?lifecycle":
		if len(f.lifecycle) == 0 {
			return rustfscache.Response{StatusCode: http.StatusNotFound}, nil
		}
		return rustfscache.Response{StatusCode: http.StatusOK, Body: f.lifecycle}, nil
	case method == http.MethodPut && path == "/example-diagnostics?lifecycle":
		f.lifecycle = append([]byte(nil), body...)
		return rustfscache.Response{StatusCode: http.StatusOK}, nil
	default:
		return rustfscache.Response{StatusCode: http.StatusBadRequest}, nil
	}
}

func diagnosticStoreFixture(t *testing.T) (Config, *fakeRequester) {
	t.Helper()
	directory := t.TempDir()
	access := filepath.Join(directory, "access")
	secret := filepath.Join(directory, "secret")
	if err := os.WriteFile(access, []byte("root-access\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("012345678901234567890123456789012345678901234567\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := Config{SchemaVersion: 1, Endpoint: "https://192.0.2.1:9002", Region: "us-east-1", CAFile: "/tmp/ca.pem",
		RootAccessKeyFile: access, RootSecretKeyFile: secret, Bucket: "example-diagnostics", Prefix: "diagnostics/v1",
		QuotaBytes: 8 * 1024 * 1024 * 1024, RetentionDays: 7, MinimumHeadroom: 1024 * 1024 * 1024}
	return config, &fakeRequester{}
}

func TestPlanApplyAndReadBack(t *testing.T) {
	config, remote := diagnosticStoreFixture(t)
	plan, err := (Runner{Requester: remote}).Run(context.Background(), config, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.StateBefore != "absent" || plan.Applied || len(plan.Actions) == 0 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	applied, err := (Runner{Requester: remote}).Run(context.Background(), config, true)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.StateAfter != "managed" || remote.quota != config.QuotaBytes {
		t.Fatalf("unexpected apply: %+v", applied)
	}
	check, err := (Runner{Requester: remote}).Run(context.Background(), config, false)
	if err != nil {
		t.Fatal(err)
	}
	if check.StateBefore != "managed" || len(check.Actions) != 0 {
		t.Fatalf("unexpected check: %+v", check)
	}
	if check.HeadroomState != "sufficient" || check.RemainingQuotaBytes != config.QuotaBytes {
		t.Fatalf("usage capacity was not reported: %+v", check)
	}
}

func TestReportsLowRemoteHeadroom(t *testing.T) {
	config, remote := diagnosticStoreFixture(t)
	remote.bucket, remote.quota = true, config.QuotaBytes
	remote.usage = config.QuotaBytes - config.MinimumHeadroom + 1
	remote.lifecycle = lifecycleDocument(config)
	result, err := (Runner{Requester: remote}).Run(context.Background(), config, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.HeadroomState != "below-minimum" || len(result.Actions) != 1 || result.Actions[0] != "increase_quota_or_reduce_retention" {
		t.Fatalf("low headroom was not actionable: %+v", result)
	}
}

func TestRejectsQuotaWithoutHeadroom(t *testing.T) {
	config := Config{SchemaVersion: 1, Endpoint: "https://192.0.2.1:9002", Region: "us-east-1", CAFile: "/tmp/ca",
		RootAccessKeyFile: "/tmp/access", RootSecretKeyFile: "/tmp/secret", Bucket: "example-diagnostics", Prefix: "diagnostics/v1",
		QuotaBytes: 2 * 1024 * 1024 * 1024, RetentionDays: 7, MinimumHeadroom: 2 * 1024 * 1024 * 1024}
	if err := config.Validate(); err == nil {
		t.Fatal("expected invalid headroom")
	}
}

func TestCanonicalExampleLoads(t *testing.T) {
	config, err := Load(filepath.Join("..", "..", "config", "diagnostic-storage.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if config.Bucket != "example-diagnostics" || config.QuotaBytes != 8*1024*1024*1024 {
		t.Fatalf("unexpected config: %+v", config)
	}
}
