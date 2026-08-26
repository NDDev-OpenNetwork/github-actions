package diagnosticstore

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
)

type Result struct {
	SchemaVersion        int       `json:"schema_version"`
	Applied              bool      `json:"applied"`
	StateBefore          string    `json:"state_before"`
	StateAfter           string    `json:"state_after"`
	Bucket               string    `json:"bucket"`
	QuotaBytes           int64     `json:"quota_bytes"`
	RetentionDays        int       `json:"retention_days"`
	CurrentUsageBytes    int64     `json:"current_usage_bytes"`
	RemainingQuotaBytes  int64     `json:"remaining_quota_bytes"`
	UsagePercentage      float64   `json:"usage_percentage"`
	MinimumHeadroomBytes int64     `json:"minimum_headroom_bytes"`
	HeadroomState        string    `json:"headroom_state"`
	UsageObservedAt      time.Time `json:"usage_observed_at"`
	Actions              []string  `json:"actions"`
	ObjectCount          int       `json:"object_count"`
	OldestObjectModified time.Time `json:"oldest_object_modified_at,omitempty"`
	ExpirationEligible   int       `json:"expiration_eligible_objects"`
	NextExpirationAt     time.Time `json:"next_expiration_at,omitempty"`
}

type Runner struct {
	Requester rustfscache.Requester
	Now       func() time.Time
}

type quotaInfo struct {
	Quota     int64  `json:"quota"`
	QuotaType string `json:"quota_type"`
}

type quotaStats struct {
	Bucket          string  `json:"bucket"`
	QuotaLimit      int64   `json:"quota_limit"`
	CurrentUsage    int64   `json:"current_usage"`
	RemainingQuota  int64   `json:"remaining_quota"`
	UsagePercentage float64 `json:"usage_percentage"`
}

type lifecycleConfiguration struct {
	XMLName xml.Name        `xml:"LifecycleConfiguration"`
	Rules   []lifecycleRule `xml:"Rule"`
}

type lifecycleRule struct {
	ID         string `xml:"ID"`
	Prefix     string `xml:"Filter>Prefix"`
	Status     string `xml:"Status"`
	Expiration struct {
		Days int `xml:"Days"`
	} `xml:"Expiration"`
}

type listBucketResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
}

type objectInventory struct {
	Count              int
	OldestModified     time.Time
	ExpirationEligible int
	NextExpiration     time.Time
}

func (r Runner) Run(ctx context.Context, config Config, apply bool) (Result, error) {
	if err := config.Validate(); err != nil {
		return Result{}, err
	}
	if r.Requester == nil {
		return Result{}, fmt.Errorf("RustFS requester is required")
	}
	root, err := readCredential(config)
	if err != nil {
		return Result{}, err
	}
	defer clear(root.SecretKey)
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	state, usage, err := r.inspect(ctx, root, config)
	if err != nil {
		return Result{}, err
	}
	result := Result{SchemaVersion: SchemaVersion, StateBefore: state, StateAfter: state,
		Bucket: config.Bucket, QuotaBytes: config.QuotaBytes, RetentionDays: config.RetentionDays,
		MinimumHeadroomBytes: config.MinimumHeadroom, HeadroomState: "unavailable"}
	if state != "absent" {
		result.CurrentUsageBytes = usage.CurrentUsage
		result.RemainingQuotaBytes = usage.RemainingQuota
		result.UsagePercentage = usage.UsagePercentage
		result.UsageObservedAt = now
		inventory, inventoryErr := r.listObjects(ctx, root, config, now)
		if inventoryErr != nil {
			return Result{}, inventoryErr
		}
		applyInventory(&result, inventory)
		result.HeadroomState = "sufficient"
		if usage.RemainingQuota < config.MinimumHeadroom {
			result.HeadroomState = "below-minimum"
			result.Actions = append(result.Actions, "increase_quota_or_reduce_retention")
		}
	}
	if state != "managed" {
		result.Actions = append(result.Actions, "create_or_verify_bucket", "apply_hard_quota", "apply_prefix_lifecycle", "verify_read_back")
	}
	if !apply {
		return result, nil
	}
	if state != "managed" {
		if err := r.apply(ctx, root, config); err != nil {
			return Result{}, err
		}
	}
	after, usageAfter, err := r.inspect(ctx, root, config)
	if err != nil {
		return Result{}, fmt.Errorf("verify diagnostic storage: %w", err)
	}
	if after != "managed" {
		return Result{}, fmt.Errorf("diagnostic storage did not converge")
	}
	result.Applied, result.StateAfter = true, after
	result.CurrentUsageBytes = usageAfter.CurrentUsage
	result.RemainingQuotaBytes = usageAfter.RemainingQuota
	result.UsagePercentage = usageAfter.UsagePercentage
	result.UsageObservedAt = now
	inventory, err := r.listObjects(ctx, root, config, now)
	if err != nil {
		return Result{}, err
	}
	applyInventory(&result, inventory)
	if usageAfter.RemainingQuota < config.MinimumHeadroom {
		result.HeadroomState = "below-minimum"
	} else {
		result.HeadroomState = "sufficient"
	}
	return result, nil
}

func (r Runner) listObjects(ctx context.Context, root rustfscache.Credential, config Config, now time.Time) (objectInventory, error) {
	const maximumPages = 1024
	inventory := objectInventory{}
	token := ""
	for page := 0; page < maximumPages; page++ {
		query := url.Values{"list-type": {"2"}, "prefix": {config.Prefix + "/"}}
		if token != "" {
			query.Set("continuation-token", token)
		}
		response, err := r.Requester.Do(ctx, root, http.MethodGet, "/"+config.Bucket+"?"+query.Encode(), "", nil)
		if err != nil {
			return objectInventory{}, fmt.Errorf("list diagnostic objects: %w", err)
		}
		if response.StatusCode != http.StatusOK {
			return objectInventory{}, responseError("list diagnostic objects", response)
		}
		var listed listBucketResult
		if err := xml.Unmarshal(response.Body, &listed); err != nil {
			return objectInventory{}, fmt.Errorf("decode diagnostic object listing: %w", err)
		}
		for _, object := range listed.Contents {
			modified, err := time.Parse(time.RFC3339Nano, object.LastModified)
			if err != nil {
				return objectInventory{}, fmt.Errorf("decode diagnostic object timestamp: %w", err)
			}
			modified = modified.UTC()
			expires := lifecycleExpirationAt(modified, config.RetentionDays)
			inventory.Count++
			if inventory.OldestModified.IsZero() || modified.Before(inventory.OldestModified) {
				inventory.OldestModified = modified
			}
			if !now.Before(expires) {
				inventory.ExpirationEligible++
			}
			if inventory.NextExpiration.IsZero() || expires.Before(inventory.NextExpiration) {
				inventory.NextExpiration = expires
			}
		}
		if !listed.IsTruncated {
			return inventory, nil
		}
		if listed.NextContinuationToken == "" {
			return objectInventory{}, fmt.Errorf("truncated diagnostic object listing omitted continuation token")
		}
		token = listed.NextContinuationToken
	}
	return objectInventory{}, fmt.Errorf("diagnostic object listing exceeded %d pages", maximumPages)
}

func lifecycleExpirationAt(modified time.Time, retentionDays int) time.Time {
	candidate := modified.UTC().Add(time.Duration(retentionDays) * 24 * time.Hour)
	midnight := candidate.Truncate(24 * time.Hour)
	if candidate.Equal(midnight) {
		return midnight
	}
	return midnight.Add(24 * time.Hour)
}

func applyInventory(result *Result, inventory objectInventory) {
	result.ObjectCount = inventory.Count
	result.OldestObjectModified = inventory.OldestModified
	result.ExpirationEligible = inventory.ExpirationEligible
	result.NextExpirationAt = inventory.NextExpiration
}

func (r Runner) inspect(ctx context.Context, root rustfscache.Credential, config Config) (string, quotaStats, error) {
	bucket, err := r.Requester.Do(ctx, root, http.MethodHead, "/"+config.Bucket, "", nil)
	if err != nil {
		return "", quotaStats{}, fmt.Errorf("inspect diagnostic bucket: %w", err)
	}
	if bucket.StatusCode == http.StatusNotFound {
		return "absent", quotaStats{}, nil
	}
	if bucket.StatusCode != http.StatusOK {
		return "", quotaStats{}, responseError("inspect diagnostic bucket", bucket)
	}
	quotaResponse, err := r.Requester.Do(ctx, root, http.MethodGet, "/rustfs/admin/v3/quota/"+config.Bucket, "", nil)
	if err != nil {
		return "", quotaStats{}, fmt.Errorf("inspect diagnostic quota: %w", err)
	}
	if quotaResponse.StatusCode != http.StatusOK && quotaResponse.StatusCode != http.StatusNotFound {
		return "", quotaStats{}, responseError("inspect diagnostic quota", quotaResponse)
	}
	var quota quotaInfo
	quotaOK := quotaResponse.StatusCode == http.StatusOK && json.Unmarshal(quotaResponse.Body, &quota) == nil &&
		quota.Quota == config.QuotaBytes && quota.QuotaType == "HARD"
	statsResponse, err := r.Requester.Do(ctx, root, http.MethodGet, "/rustfs/admin/v3/quota-stats/"+config.Bucket, "", nil)
	if err != nil {
		return "", quotaStats{}, fmt.Errorf("inspect diagnostic quota usage: %w", err)
	}
	if statsResponse.StatusCode != http.StatusOK {
		return "", quotaStats{}, responseError("inspect diagnostic quota usage", statsResponse)
	}
	var usage quotaStats
	if err := json.Unmarshal(statsResponse.Body, &usage); err != nil || usage.Bucket != config.Bucket || usage.QuotaLimit != config.QuotaBytes ||
		usage.CurrentUsage < 0 || usage.RemainingQuota < 0 || usage.CurrentUsage > usage.QuotaLimit ||
		usage.RemainingQuota != usage.QuotaLimit-usage.CurrentUsage || usage.UsagePercentage < 0 || usage.UsagePercentage > 100 {
		return "", quotaStats{}, fmt.Errorf("diagnostic quota usage response is inconsistent")
	}
	lifecycleResponse, err := r.Requester.Do(ctx, root, http.MethodGet, "/"+config.Bucket+"?lifecycle", "", nil)
	if err != nil {
		return "", quotaStats{}, fmt.Errorf("inspect diagnostic lifecycle: %w", err)
	}
	if lifecycleResponse.StatusCode != http.StatusOK && lifecycleResponse.StatusCode != http.StatusNotFound {
		return "", quotaStats{}, responseError("inspect diagnostic lifecycle", lifecycleResponse)
	}
	if quotaOK && lifecycleResponse.StatusCode == http.StatusOK && lifecycleEquivalent(lifecycleResponse.Body, config) {
		return "managed", usage, nil
	}
	return "drifted", usage, nil
}

func (r Runner) apply(ctx context.Context, root rustfscache.Credential, config Config) error {
	response, err := r.Requester.Do(ctx, root, http.MethodPut, "/"+config.Bucket, "", nil)
	if err != nil || (response.StatusCode != http.StatusOK && response.StatusCode != http.StatusConflict) {
		return requestError("create diagnostic bucket", response, err)
	}
	quota, _ := json.Marshal(map[string]any{"quota": config.QuotaBytes, "quota_type": "HARD"})
	response, err = r.Requester.Do(ctx, root, http.MethodPut, "/rustfs/admin/v3/quota/"+config.Bucket, "application/json", quota)
	if err != nil || response.StatusCode != http.StatusOK {
		return requestError("apply diagnostic quota", response, err)
	}
	response, err = r.Requester.Do(ctx, root, http.MethodPut, "/"+config.Bucket+"?lifecycle", "application/xml", lifecycleDocument(config))
	if err != nil || response.StatusCode != http.StatusOK {
		return requestError("apply diagnostic lifecycle", response, err)
	}
	return nil
}

func lifecycleDocument(config Config) []byte {
	value := lifecycleConfiguration{Rules: []lifecycleRule{{ID: "gha-diagnostics-retention-v1", Prefix: config.Prefix + "/", Status: "Enabled"}}}
	value.Rules[0].Expiration.Days = config.RetentionDays
	raw, _ := xml.Marshal(value)
	return append([]byte(xml.Header), raw...)
}

func lifecycleEquivalent(raw []byte, config Config) bool {
	var actual lifecycleConfiguration
	if xml.Unmarshal(raw, &actual) != nil || len(actual.Rules) != 1 {
		return false
	}
	wanted := lifecycleConfiguration{Rules: []lifecycleRule{{ID: "gha-diagnostics-retention-v1", Prefix: config.Prefix + "/", Status: "Enabled"}}}
	wanted.Rules[0].Expiration.Days = config.RetentionDays
	return actual.Rules[0] == wanted.Rules[0]
}

func readCredential(config Config) (rustfscache.Credential, error) {
	for label, path := range map[string]string{"access key": config.RootAccessKeyFile, "secret key": config.RootSecretKeyFile} {
		info, err := os.Lstat(path)
		if err != nil {
			return rustfscache.Credential{}, fmt.Errorf("inspect RustFS root %s: %w", label, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !credentialModeAllowed(path, info.Mode().Perm()) {
			return rustfscache.Credential{}, fmt.Errorf("RustFS root %s must be a regular non-symlink file with safe credential permissions", label)
		}
	}
	access, err := os.ReadFile(config.RootAccessKeyFile)
	if err != nil {
		return rustfscache.Credential{}, fmt.Errorf("read RustFS root access key: %w", err)
	}
	secret, err := os.ReadFile(config.RootSecretKeyFile)
	if err != nil {
		return rustfscache.Credential{}, fmt.Errorf("read RustFS root secret key: %w", err)
	}
	credential := rustfscache.Credential{AccessKey: strings.TrimSpace(string(access)), SecretKey: []byte(strings.TrimSpace(string(secret)))}
	clear(access)
	clear(secret)
	if credential.AccessKey == "" || len(credential.SecretKey) < 32 {
		clear(credential.SecretKey)
		return rustfscache.Credential{}, fmt.Errorf("RustFS root credential is invalid")
	}
	return credential, nil
}

func credentialModeAllowed(path string, mode os.FileMode) bool {
	if strings.HasPrefix(path, "/run/credentials/") {
		// systemd LoadCredential uses 0440 inside its isolated, read-only service
		// credential mount. Group read is intentional there; group write/execute
		// and every world bit remain forbidden.
		return mode&0o037 == 0 && mode&0o400 != 0
	}
	return mode&0o077 == 0 && mode&0o400 != 0
}

func requestError(operation string, response rustfscache.Response, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return responseError(operation, response)
}

func responseError(operation string, response rustfscache.Response) error {
	body := strings.TrimSpace(string(response.Body))
	if len(body) > 256 {
		body = body[:256]
	}
	return fmt.Errorf("%s returned HTTP %d: %s", operation, response.StatusCode, body)
}

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
