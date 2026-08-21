package diagnosticstore

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
)

type Result struct {
	SchemaVersion int      `json:"schema_version"`
	Applied       bool     `json:"applied"`
	StateBefore   string   `json:"state_before"`
	StateAfter    string   `json:"state_after"`
	Bucket        string   `json:"bucket"`
	QuotaBytes    int64    `json:"quota_bytes"`
	RetentionDays int      `json:"retention_days"`
	Actions       []string `json:"actions"`
}

type Runner struct{ Requester rustfscache.Requester }

type quotaInfo struct {
	Quota     int64  `json:"quota"`
	QuotaType string `json:"quota_type"`
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
	state, err := r.inspect(ctx, root, config)
	if err != nil {
		return Result{}, err
	}
	result := Result{SchemaVersion: SchemaVersion, StateBefore: state, StateAfter: state,
		Bucket: config.Bucket, QuotaBytes: config.QuotaBytes, RetentionDays: config.RetentionDays}
	if state != "managed" {
		result.Actions = []string{"create_or_verify_bucket", "apply_hard_quota", "apply_prefix_lifecycle", "verify_read_back"}
	}
	if !apply {
		return result, nil
	}
	if state != "managed" {
		if err := r.apply(ctx, root, config); err != nil {
			return Result{}, err
		}
	}
	after, err := r.inspect(ctx, root, config)
	if err != nil {
		return Result{}, fmt.Errorf("verify diagnostic storage: %w", err)
	}
	if after != "managed" {
		return Result{}, fmt.Errorf("diagnostic storage did not converge")
	}
	result.Applied, result.StateAfter = true, after
	return result, nil
}

func (r Runner) inspect(ctx context.Context, root rustfscache.Credential, config Config) (string, error) {
	bucket, err := r.Requester.Do(ctx, root, http.MethodHead, "/"+config.Bucket, "", nil)
	if err != nil {
		return "", fmt.Errorf("inspect diagnostic bucket: %w", err)
	}
	if bucket.StatusCode == http.StatusNotFound {
		return "absent", nil
	}
	if bucket.StatusCode != http.StatusOK {
		return "", responseError("inspect diagnostic bucket", bucket)
	}
	quotaResponse, err := r.Requester.Do(ctx, root, http.MethodGet, "/rustfs/admin/v3/quota/"+config.Bucket, "", nil)
	if err != nil {
		return "", fmt.Errorf("inspect diagnostic quota: %w", err)
	}
	if quotaResponse.StatusCode != http.StatusOK && quotaResponse.StatusCode != http.StatusNotFound {
		return "", responseError("inspect diagnostic quota", quotaResponse)
	}
	var quota quotaInfo
	quotaOK := quotaResponse.StatusCode == http.StatusOK && json.Unmarshal(quotaResponse.Body, &quota) == nil &&
		quota.Quota == config.QuotaBytes && quota.QuotaType == "HARD"
	lifecycleResponse, err := r.Requester.Do(ctx, root, http.MethodGet, "/"+config.Bucket+"?lifecycle", "", nil)
	if err != nil {
		return "", fmt.Errorf("inspect diagnostic lifecycle: %w", err)
	}
	if lifecycleResponse.StatusCode != http.StatusOK && lifecycleResponse.StatusCode != http.StatusNotFound {
		return "", responseError("inspect diagnostic lifecycle", lifecycleResponse)
	}
	if quotaOK && lifecycleResponse.StatusCode == http.StatusOK && lifecycleEquivalent(lifecycleResponse.Body, config) {
		return "managed", nil
	}
	return "drifted", nil
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
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return rustfscache.Credential{}, fmt.Errorf("RustFS root %s must be a regular non-symlink file without group/world permissions", label)
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
