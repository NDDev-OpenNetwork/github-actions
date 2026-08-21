package rustfscache

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/NDDev-OpenNetwork/github-actions/internal/cachenamespace"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	secretBytes            = 48
	accessKeyPrefix        = "AKIA"
	credentialFileMode     = 0o640
	maximumCredentialBytes = 512
	quotaReadyAttempts     = 180
)

type Options struct {
	Config       Config
	Apply        bool
	RootOwnerUID int
	RootOwnerGID int
	OwnerUID     int
	OwnerGID     int
}

type IdentityResult struct {
	Role          string `json:"role"`
	AccessKey     string `json:"access_key"`
	Policy        string `json:"policy"`
	Prefix        string `json:"prefix"`
	Mode          string `json:"mode"`
	AccessKeyFile string `json:"access_key_file"`
	SecretKeyFile string `json:"secret_key_file"`
}

type Result struct {
	SchemaVersion int              `json:"schema_version"`
	Applied       bool             `json:"applied"`
	LocalState    string           `json:"local_state"`
	StateBefore   string           `json:"state_before"`
	StateAfter    string           `json:"state_after"`
	Bucket        string           `json:"bucket"`
	QuotaBytes    int64            `json:"quota_bytes"`
	Actions       []string         `json:"actions"`
	Identities    []IdentityResult `json:"identities"`
}

type Runner struct {
	Requester Requester
	Random    io.Reader
	Sleep     func(context.Context, time.Duration) error
}

type managedCredential struct {
	identity      Identity
	accessKey     string
	secretKey     []byte
	accessKeyFile string
	secretKeyFile string
}

type userInfo struct {
	PolicyName string `json:"policyName"`
	Status     string `json:"status"`
}

type policyInfo struct {
	PolicyName string          `json:"policy_name"`
	Policy     json.RawMessage `json:"policy"`
}

type quotaInfo struct {
	Quota     int64  `json:"quota"`
	QuotaType string `json:"quota_type"`
}

type lifecycleConfiguration struct {
	Rules []lifecycleRule `xml:"Rule"`
}

type lifecycleRule struct {
	ID         string `xml:"ID"`
	Prefix     string `xml:"Filter>Prefix"`
	Status     string `xml:"Status"`
	Expiration struct {
		Days int `xml:"Days"`
	} `xml:"Expiration"`
}

// deniedCounterpart is the namespace a credential must be refused, derived from
// the one it holds. Writing the pair by hand is how an isolation proof turns
// into a test of a class nothing was ever granted.
func deniedCounterpart(prefix string) (string, error) {
	repository, class, err := cachenamespace.ParsePrefixRoot(prefix)
	if err != nil {
		return "", err
	}
	counterpart, err := cachenamespace.Counterpart(class)
	if err != nil {
		return "", err
	}
	return cachenamespace.PrefixRootFor(repository, counterpart)
}

func (r Runner) Run(ctx context.Context, options Options) (Result, error) {
	if err := validateOptions(options); err != nil {
		return Result{}, err
	}
	if r.Requester == nil {
		return Result{}, fmt.Errorf("RustFS requester is required")
	}
	root, err := readRootCredential(options.Config, options.RootOwnerUID, options.RootOwnerGID)
	if err != nil {
		return Result{}, err
	}
	defer clear(root.SecretKey)

	credentials, localState, err := loadManagedCredentials(options)
	if err != nil {
		return Result{}, err
	}
	defer clearManagedCredentials(credentials)
	remoteState, err := r.inspectRemote(ctx, root, options.Config, credentials, localState)
	if err != nil {
		return Result{}, err
	}
	result := resultFor(options.Config, credentials, localState, remoteState)
	if localState == "managed" && remoteState == "managed" {
		if err := r.verifyReadOnlyPolicy(ctx, options.Config, credentials); err != nil {
			return Result{}, fmt.Errorf("verify read-only RustFS cache policy: %w", err)
		}
		if options.Apply {
			if err := r.verifyEffectivePolicy(ctx, root, options.Config, credentials); err != nil {
				return Result{}, fmt.Errorf("verify effective RustFS cache policy: %w", err)
			}
			result.Applied = true
			result.Actions = []string{"verify_effective_boundaries"}
		}
		return result, nil
	}
	if !options.Apply {
		return result, nil
	}
	if localState == "fresh" {
		credentials, err = generateManagedCredentials(options, r.Random)
		if err != nil {
			return Result{}, err
		}
		defer clearManagedCredentials(credentials)
		if err := writeManagedCredentials(options, credentials); err != nil {
			return Result{}, err
		}
	}
	if err := r.applyRemote(ctx, root, options.Config, credentials); err != nil {
		return Result{}, err
	}
	stateAfter, err := r.inspectRemote(ctx, root, options.Config, credentials, "managed")
	if err != nil {
		return Result{}, fmt.Errorf("verify RustFS cache identities: %w", err)
	}
	if stateAfter != "managed" {
		return Result{}, fmt.Errorf("RustFS cache identities did not converge")
	}
	if err := r.verifyEffectivePolicy(ctx, root, options.Config, credentials); err != nil {
		return Result{}, fmt.Errorf("verify effective RustFS cache policy: %w", err)
	}
	result.StateAfter = stateAfter
	result.Applied = true
	return result, nil
}

func validateOptions(options Options) error {
	if err := options.Config.Validate(); err != nil {
		return err
	}
	if options.RootOwnerUID < 0 || options.RootOwnerGID < 0 {
		return fmt.Errorf("root credential owner IDs must be non-negative")
	}
	if options.OwnerUID < 0 || options.OwnerGID < 0 {
		return fmt.Errorf("credential owner IDs must be non-negative")
	}
	if err := validateCredentialDirectory(options.Config.CredentialsDirectory, options.OwnerUID, options.OwnerGID); err != nil {
		return err
	}
	return nil
}

func readRootCredential(config Config, ownerUID, ownerGID int) (Credential, error) {
	access, err := readCredentialFile(config.RootAccessKeyFile, ownerUID, ownerGID, 0o600)
	if err != nil {
		return Credential{}, fmt.Errorf("read RustFS root access key: %w", err)
	}
	secret, err := readCredentialFile(config.RootSecretKeyFile, ownerUID, ownerGID, 0o600)
	if err != nil {
		return Credential{}, fmt.Errorf("read RustFS root secret key: %w", err)
	}
	if len(access) < 12 || len(access) > 128 || len(secret) < 32 || len(secret) > 256 {
		clear(secret)
		return Credential{}, fmt.Errorf("RustFS root credential has an invalid length")
	}
	return Credential{AccessKey: string(access), SecretKey: secret}, nil
}

func loadManagedCredentials(options Options) ([]managedCredential, string, error) {
	credentials := credentialSkeletons(options.Config)
	present := 0
	for index := range credentials {
		accessExists, err := regularFileExists(credentials[index].accessKeyFile)
		if err != nil {
			return nil, "", err
		}
		secretExists, err := regularFileExists(credentials[index].secretKeyFile)
		if err != nil {
			return nil, "", err
		}
		if accessExists != secretExists {
			return nil, "", fmt.Errorf("RustFS %s credential pair is incomplete", credentials[index].identity.Role)
		}
		if !accessExists {
			continue
		}
		present++
		access, err := readCredentialFile(credentials[index].accessKeyFile, options.OwnerUID, options.OwnerGID, credentialFileMode)
		if err != nil {
			return nil, "", fmt.Errorf("read %s access key: %w", credentials[index].identity.Role, err)
		}
		secret, err := readCredentialFile(credentials[index].secretKeyFile, options.OwnerUID, options.OwnerGID, credentialFileMode)
		if err != nil {
			return nil, "", fmt.Errorf("read %s secret key: %w", credentials[index].identity.Role, err)
		}
		if string(access) != credentials[index].accessKey || len(secret) != 64 {
			clear(secret)
			return nil, "", fmt.Errorf("RustFS %s local credential drifted", credentials[index].identity.Role)
		}
		credentials[index].secretKey = secret
	}
	if present == 0 {
		return credentials, "fresh", nil
	}
	if present != len(credentials) {
		clearManagedCredentials(credentials)
		return nil, "", fmt.Errorf("RustFS managed credential set is partial")
	}
	return credentials, "managed", nil
}

func credentialSkeletons(config Config) []managedCredential {
	identities := config.SortedIdentities()
	credentials := make([]managedCredential, 0, len(identities))
	for _, identity := range identities {
		credentials = append(credentials, managedCredential{
			identity:      identity,
			accessKey:     accessKeyForIdentity(config, identity),
			accessKeyFile: filepath.Join(config.CredentialsDirectory, "rustfs-"+identity.Role+"-access-key"),
			secretKeyFile: filepath.Join(config.CredentialsDirectory, "rustfs-"+identity.Role+"-secret-key"),
		})
	}
	return credentials
}

func accessKeyForIdentity(config Config, identity Identity) string {
	material := "nddev-rustfs-cache-access-v2\x00" + config.Bucket + "\x00" + identity.Prefix + "\x00" + identity.Role
	// Preserve the already-deployed user identities for the original bucket.
	// Every additional bucket uses v2 and therefore cannot collide by role.
	if config.Bucket == "github-actions-cache" {
		return accessKeyForRole(identity.Role)
	}
	digest := sha256.Sum256([]byte(material))
	return accessKeyPrefix + strings.ToUpper(hex.EncodeToString(digest[:8]))
}

func accessKeyForRole(role string) string {
	digest := sha256.Sum256([]byte("nddev-rustfs-cache-access-v1\x00" + role))
	return accessKeyPrefix + strings.ToUpper(hex.EncodeToString(digest[:8]))
}

func generateManagedCredentials(options Options, random io.Reader) ([]managedCredential, error) {
	if random == nil {
		random = rand.Reader
	}
	credentials := credentialSkeletons(options.Config)
	seen := make(map[string]struct{}, len(credentials))
	for index := range credentials {
		randomBytes := make([]byte, secretBytes)
		if _, err := io.ReadFull(random, randomBytes); err != nil {
			clear(randomBytes)
			clearManagedCredentials(credentials)
			return nil, fmt.Errorf("generate %s secret: %w", credentials[index].identity.Role, err)
		}
		secret := []byte(base64.RawURLEncoding.EncodeToString(randomBytes))
		clear(randomBytes)
		if _, duplicate := seen[string(secret)]; duplicate {
			clear(secret)
			clearManagedCredentials(credentials)
			return nil, fmt.Errorf("generated duplicate RustFS secret")
		}
		seen[string(secret)] = struct{}{}
		credentials[index].secretKey = secret
	}
	return credentials, nil
}

func writeManagedCredentials(options Options, credentials []managedCredential) error {
	created := make([]string, 0, len(credentials)*2)
	fail := func(writeErr error) error {
		cleanupErr := removeCreatedCredentials(options.Config.CredentialsDirectory, created)
		return errors.Join(writeErr, cleanupErr)
	}
	for _, credential := range credentials {
		if err := atomicWrite(credential.accessKeyFile, []byte(credential.accessKey+"\n"), options.OwnerUID, options.OwnerGID); err != nil {
			return fail(fmt.Errorf("write %s access key: %w", credential.identity.Role, err))
		}
		created = append(created, credential.accessKeyFile)
		secretFile := make([]byte, len(credential.secretKey)+1)
		copy(secretFile, credential.secretKey)
		secretFile[len(secretFile)-1] = '\n'
		err := atomicWrite(credential.secretKeyFile, secretFile, options.OwnerUID, options.OwnerGID)
		clear(secretFile)
		if err != nil {
			return fail(fmt.Errorf("write %s secret key: %w", credential.identity.Role, err))
		}
		created = append(created, credential.secretKeyFile)
	}
	return nil
}

func removeCreatedCredentials(directory string, paths []string) error {
	var cleanupErrors []error
	for index := len(paths) - 1; index >= 0; index-- {
		if err := os.Remove(paths[index]); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove incomplete credential %s: %w", paths[index], err))
		}
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("open credential directory after rollback: %w", err))
	} else {
		if syncErr := directoryFile.Sync(); syncErr != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("sync credential directory after rollback: %w", syncErr))
		}
		if closeErr := directoryFile.Close(); closeErr != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("close credential directory after rollback: %w", closeErr))
		}
	}
	return errors.Join(cleanupErrors...)
}

func (r Runner) inspectRemote(
	ctx context.Context,
	root Credential,
	config Config,
	credentials []managedCredential,
	localState string,
) (string, error) {
	bucketResponse, err := r.Requester.Do(ctx, root, http.MethodHead, "/"+config.Bucket, "", nil)
	if err != nil {
		return "", fmt.Errorf("inspect RustFS cache bucket: %w", err)
	}
	bucketPresent := bucketResponse.StatusCode == http.StatusOK
	if bucketResponse.StatusCode != http.StatusOK && bucketResponse.StatusCode != http.StatusNotFound {
		return "", responseError("inspect RustFS cache bucket", bucketResponse)
	}
	if bucketPresent && localState == "fresh" {
		return "", fmt.Errorf("remote RustFS cache bucket exists without local credential state")
	}
	resourcesManaged := false
	if bucketPresent {
		quotaResponse, err := r.Requester.Do(ctx, root, http.MethodGet,
			"/rustfs/admin/v3/quota/"+config.Bucket, "", nil)
		if err != nil {
			return "", fmt.Errorf("inspect RustFS cache quota: %w", err)
		}
		if quotaResponse.StatusCode != http.StatusOK && quotaResponse.StatusCode != http.StatusNotFound {
			return "", responseError("inspect RustFS cache quota", quotaResponse)
		}
		var quota quotaInfo
		quotaManaged := quotaResponse.StatusCode == http.StatusOK && json.Unmarshal(quotaResponse.Body, &quota) == nil &&
			quota.Quota == config.QuotaBytes && quota.QuotaType == "HARD"

		lifecycleResponse, err := r.Requester.Do(ctx, root, http.MethodGet,
			"/"+config.Bucket+"?lifecycle", "", nil)
		if err != nil {
			return "", fmt.Errorf("inspect RustFS cache lifecycle: %w", err)
		}
		if lifecycleResponse.StatusCode != http.StatusOK && lifecycleResponse.StatusCode != http.StatusNotFound {
			return "", responseError("inspect RustFS cache lifecycle", lifecycleResponse)
		}
		resourcesManaged = quotaManaged && lifecycleResponse.StatusCode == http.StatusOK &&
			lifecycleEquivalent(lifecycleResponse.Body, config)
	}

	present := 0
	for _, credential := range credentials {
		response, err := r.Requester.Do(ctx, root, http.MethodGet,
			"/rustfs/admin/v3/user-info?accessKey="+url.QueryEscape(credential.accessKey), "", nil)
		if err != nil {
			return "", fmt.Errorf("inspect %s RustFS user: %w", credential.identity.Role, err)
		}
		if response.StatusCode == http.StatusNotFound {
			continue
		}
		if response.StatusCode != http.StatusOK {
			return "", responseError("inspect RustFS user", response)
		}
		present++
		if localState == "fresh" {
			return "", fmt.Errorf("remote RustFS %s identity exists without local credential", credential.identity.Role)
		}
		var info userInfo
		if err := json.Unmarshal(response.Body, &info); err != nil {
			return "", fmt.Errorf("decode %s RustFS user: %w", credential.identity.Role, err)
		}
		if info.PolicyName != credential.identity.Policy || info.Status != "enabled" {
			return "provisioning", nil
		}
		policyResponse, err := r.Requester.Do(ctx, root, http.MethodGet,
			"/rustfs/admin/v3/info-canned-policy?name="+url.QueryEscape(credential.identity.Policy), "", nil)
		if err != nil {
			return "", fmt.Errorf("inspect %s RustFS policy: %w", credential.identity.Role, err)
		}
		if policyResponse.StatusCode == http.StatusNotFound {
			return "provisioning", nil
		}
		if policyResponse.StatusCode != http.StatusOK {
			return "", responseError("inspect RustFS policy", policyResponse)
		}
		var policy policyInfo
		if err := json.Unmarshal(policyResponse.Body, &policy); err != nil {
			return "", fmt.Errorf("decode %s RustFS policy: %w", credential.identity.Role, err)
		}
		if policy.PolicyName != credential.identity.Policy ||
			!jsonEquivalent(policy.Policy, policyDocument(config.Bucket, credential.identity)) {
			return "provisioning", nil
		}
	}
	if present == 0 {
		if localState == "managed" {
			return "provisioning", nil
		}
		if bucketPresent {
			return "provisioning", nil
		}
		return "fresh", nil
	}
	if present != len(credentials) || !resourcesManaged {
		return "provisioning", nil
	}
	return "managed", nil
}

func (r Runner) verifyReadOnlyPolicy(
	ctx context.Context,
	config Config,
	credentials []managedCredential,
) error {
	for _, credential := range credentials {
		user := Credential{AccessKey: credential.accessKey, SecretKey: credential.secretKey}
		response, err := r.Requester.Do(ctx, user, http.MethodGet, "/"+config.Bucket+"?location", "", nil)
		if err != nil || response.StatusCode != http.StatusOK {
			return fmt.Errorf("%s bucket-location access failed: %w", credential.identity.Role, requestFailure(err, response))
		}
		ownPath := "/" + config.Bucket + "/" + credential.identity.Prefix +
			"/_reconcile/read-only-" + strings.ToLower(credential.accessKey)
		response, err = r.Requester.Do(ctx, user, http.MethodGet, ownPath, "", nil)
		if err != nil || (response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotFound) {
			return fmt.Errorf("%s own-prefix read was not authorized: %w", credential.identity.Role, requestFailure(err, response))
		}
		// The class a credential must be refused is derived from its own,
		// so the isolation proof cannot quietly become a test of a class the
		// credential was never granted anyway.
		deniedPrefix, err := deniedCounterpart(credential.identity.Prefix)
		if err != nil {
			return err
		}
		deniedPath := "/" + config.Bucket + "/" + deniedPrefix +
			"/_reconcile/read-only-denied-" + strings.ToLower(credential.accessKey)
		response, err = r.Requester.Do(ctx, user, http.MethodGet, deniedPath, "", nil)
		if err != nil || response.StatusCode != http.StatusForbidden {
			return fmt.Errorf("%s cross-prefix read was not denied: %w", credential.identity.Role, requestFailure(err, response))
		}
	}
	return nil
}

func resultFor(config Config, credentials []managedCredential, localState, remoteState string) Result {
	identities := make([]IdentityResult, 0, len(credentials))
	for _, credential := range credentials {
		identities = append(identities, IdentityResult{
			Role: credential.identity.Role, AccessKey: credential.accessKey, Policy: credential.identity.Policy,
			Prefix: credential.identity.Prefix, Mode: credential.identity.Mode,
			AccessKeyFile: credential.accessKeyFile, SecretKeyFile: credential.secretKeyFile,
		})
	}
	result := Result{
		SchemaVersion: SchemaVersion, LocalState: localState, StateBefore: remoteState, StateAfter: remoteState,
		Bucket: config.Bucket, QuotaBytes: config.QuotaBytes, Identities: identities,
	}
	if localState != "managed" || remoteState != "managed" {
		result.Actions = []string{"create_or_verify_bucket", "apply_quota_and_lifecycle", "create_or_repair_trust_identities", "verify_effective_boundaries"}
	}
	return result
}

func (r Runner) applyRemote(ctx context.Context, root Credential, config Config, credentials []managedCredential) error {
	response, err := r.Requester.Do(ctx, root, http.MethodPut, "/"+config.Bucket, "", nil)
	if err != nil {
		return fmt.Errorf("create RustFS cache bucket: %w", err)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusConflict {
		return responseError("create RustFS cache bucket", response)
	}
	if err := r.waitQuotaReady(ctx, root, config); err != nil {
		return err
	}
	quota, _ := json.Marshal(map[string]any{"quota": config.QuotaBytes, "quota_type": "HARD"})
	response, err = r.Requester.Do(ctx, root, http.MethodPut,
		"/rustfs/admin/v3/quota/"+config.Bucket, "application/json", quota)
	if err != nil || response.StatusCode != http.StatusOK {
		if err != nil {
			return fmt.Errorf("apply RustFS cache quota: %w", err)
		}
		return responseError("apply RustFS cache quota", response)
	}
	lifecycle := lifecycleDocument(config)
	response, err = r.Requester.Do(ctx, root, http.MethodPut, "/"+config.Bucket+"?lifecycle", "application/xml", lifecycle)
	if err != nil || response.StatusCode != http.StatusOK {
		if err != nil {
			return fmt.Errorf("apply RustFS cache lifecycle: %w", err)
		}
		return responseError("apply RustFS cache lifecycle", response)
	}

	for _, credential := range credentials {
		policy := policyDocument(config.Bucket, credential.identity)
		response, err = r.Requester.Do(ctx, root, http.MethodPut,
			"/rustfs/admin/v3/add-canned-policy?name="+url.QueryEscape(credential.identity.Policy), "application/json", policy)
		if err != nil || response.StatusCode != http.StatusOK {
			if err != nil {
				return fmt.Errorf("apply %s RustFS policy: %w", credential.identity.Role, err)
			}
			return responseError("apply RustFS policy", response)
		}
		info, err := r.Requester.Do(ctx, root, http.MethodGet,
			"/rustfs/admin/v3/user-info?accessKey="+url.QueryEscape(credential.accessKey), "", nil)
		if err != nil {
			return fmt.Errorf("inspect %s RustFS user before apply: %w", credential.identity.Role, err)
		}
		if info.StatusCode == http.StatusNotFound {
			userBody, _ := json.Marshal(map[string]string{"secretKey": string(credential.secretKey), "status": "enabled"})
			response, err = r.Requester.Do(ctx, root, http.MethodPut,
				"/rustfs/admin/v3/add-user?accessKey="+url.QueryEscape(credential.accessKey), "application/json", userBody)
			clear(userBody)
			if err != nil || response.StatusCode != http.StatusOK {
				if err != nil {
					return fmt.Errorf("create %s RustFS user: %w", credential.identity.Role, err)
				}
				return responseError("create RustFS user", response)
			}
		} else if info.StatusCode != http.StatusOK {
			return responseError("inspect RustFS user before apply", info)
		}
		response, err = r.Requester.Do(ctx, root, http.MethodPut,
			"/rustfs/admin/v3/set-user-or-group-policy?policyName="+url.QueryEscape(credential.identity.Policy)+
				"&userOrGroup="+url.QueryEscape(credential.accessKey)+"&isGroup=false", "", nil)
		if err != nil || response.StatusCode != http.StatusOK {
			if err != nil {
				return fmt.Errorf("attach %s RustFS policy: %w", credential.identity.Role, err)
			}
			return responseError("attach RustFS policy", response)
		}
	}
	return nil
}

func (r Runner) waitQuotaReady(ctx context.Context, root Credential, config Config) error {
	sleep := r.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, duration time.Duration) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	for attempt := 0; attempt < quotaReadyAttempts; attempt++ {
		response, err := r.Requester.Do(ctx, root, http.MethodGet,
			"/rustfs/admin/v3/quota-stats/"+config.Bucket, "", nil)
		if err != nil {
			return fmt.Errorf("check RustFS cache quota readiness: %w", err)
		}
		if response.StatusCode == http.StatusOK {
			return nil
		}
		if response.StatusCode != http.StatusServiceUnavailable {
			return responseError("check RustFS cache quota readiness", response)
		}
		if err := sleep(ctx, time.Second); err != nil {
			return fmt.Errorf("wait for RustFS cache quota readiness: %w", err)
		}
	}
	return fmt.Errorf("RustFS cache quota usage did not become authoritative")
}

func (r Runner) verifyEffectivePolicy(
	ctx context.Context,
	root Credential,
	config Config,
	credentials []managedCredential,
) error {
	for _, credential := range credentials {
		key := credential.identity.Prefix + "/_reconcile/" + strings.ToLower(credential.accessKey) + ".txt"
		requestPath := "/" + config.Bucket + "/" + key
		payload := []byte("nddev-rustfs-cache-boundary-v1\n")
		user := Credential{AccessKey: credential.accessKey, SecretKey: credential.secretKey}
		if credential.identity.Mode == "read-write" {
			response, err := r.Requester.Do(ctx, user, http.MethodPut, requestPath, "application/octet-stream", payload)
			if err != nil || response.StatusCode != http.StatusOK {
				return fmt.Errorf("%s allowed write failed: %w", credential.identity.Role, requestFailure(err, response))
			}
		} else {
			response, err := r.Requester.Do(ctx, root, http.MethodPut, requestPath, "application/octet-stream", payload)
			if err != nil || response.StatusCode != http.StatusOK {
				return fmt.Errorf("prepare %s read probe: %w", credential.identity.Role, requestFailure(err, response))
			}
		}
		response, err := r.Requester.Do(ctx, user, http.MethodGet, requestPath, "", nil)
		if err != nil || response.StatusCode != http.StatusOK || string(response.Body) != string(payload) {
			return fmt.Errorf("%s allowed read failed: %w", credential.identity.Role, requestFailure(err, response))
		}
		if credential.identity.Mode == "read-only" {
			response, err = r.Requester.Do(ctx, user, http.MethodPut, requestPath, "application/octet-stream", payload)
			if err != nil || response.StatusCode != http.StatusForbidden {
				return fmt.Errorf("%s write was not denied: %w", credential.identity.Role, requestFailure(err, response))
			}
		}
		// The class a credential must be refused is derived from its own,
		// so the isolation proof cannot quietly become a test of a class the
		// credential was never granted anyway.
		deniedPrefix, err := deniedCounterpart(credential.identity.Prefix)
		if err != nil {
			return err
		}
		deniedPath := "/" + config.Bucket + "/" + deniedPrefix + "/_reconcile/denied-" + strings.ToLower(credential.accessKey)
		response, err = r.Requester.Do(ctx, user, http.MethodPut, deniedPath, "application/octet-stream", payload)
		if err != nil || response.StatusCode != http.StatusForbidden {
			return fmt.Errorf("%s cross-namespace write was not denied: %w", credential.identity.Role, requestFailure(err, response))
		}
		response, err = r.Requester.Do(ctx, user, http.MethodDelete, requestPath, "", nil)
		if err != nil || response.StatusCode != http.StatusForbidden {
			return fmt.Errorf("%s delete was not denied: %w", credential.identity.Role, requestFailure(err, response))
		}
		response, err = r.Requester.Do(ctx, root, http.MethodDelete, requestPath, "", nil)
		if err != nil || (response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK) {
			return fmt.Errorf("clean %s probe: %w", credential.identity.Role, requestFailure(err, response))
		}
	}
	return nil
}

func policyDocument(bucket string, identity Identity) []byte {
	actions := []string{"s3:GetObject"}
	if identity.Mode == "read-write" {
		actions = append(actions, "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts", "s3:PutObject")
		sort.Strings(actions)
	}
	document := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{
				"Effect": "Allow", "Action": []string{"s3:GetBucketLocation"},
				"Resource": []string{"arn:aws:s3:::" + bucket},
			},
			map[string]any{
				"Effect": "Allow", "Action": actions,
				"Resource": []string{"arn:aws:s3:::" + bucket + "/" + identity.Prefix + "/*"},
			},
		},
	}
	encoded, _ := json.Marshal(document)
	return encoded
}

func lifecycleDocument(config Config) []byte {
	retention := make(map[string]int)
	for _, identity := range config.Identities {
		retention[identity.Prefix] = identity.RetentionDays
	}
	prefixes := make([]string, 0, len(retention))
	for prefix := range retention {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	var document strings.Builder
	document.WriteString(`<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	for _, prefix := range prefixes {
		identifier, identifierErr := cachenamespace.Identifier(prefix)
		if identifierErr != nil {
			continue
		}
		fmt.Fprintf(&document,
			"<Rule><ID>%s-cache-expiry</ID><Filter><Prefix>%s/</Prefix></Filter><Status>Enabled</Status><Expiration><Days>%d</Days></Expiration></Rule>",
			identifier, prefix, retention[prefix])
	}
	document.WriteString(`</LifecycleConfiguration>`)
	return []byte(document.String())
}

func lifecycleEquivalent(document []byte, config Config) bool {
	var decoded lifecycleConfiguration
	if xml.Unmarshal(document, &decoded) != nil {
		return false
	}
	expected := make(map[string]int)
	for _, identity := range config.Identities {
		expected[identity.Prefix+"/"] = identity.RetentionDays
	}
	if len(decoded.Rules) != len(expected) {
		return false
	}
	seen := make(map[string]struct{}, len(decoded.Rules))
	for _, rule := range decoded.Rules {
		days, exists := expected[rule.Prefix]
		base, identifierErr := cachenamespace.Identifier(strings.TrimSuffix(rule.Prefix, "/"))
		if identifierErr != nil {
			return false
		}
		identifier := base + "-cache-expiry"
		if !exists || rule.Status != "Enabled" || rule.Expiration.Days != days || rule.ID != identifier {
			return false
		}
		if _, duplicate := seen[rule.Prefix]; duplicate {
			return false
		}
		seen[rule.Prefix] = struct{}{}
	}
	return true
}

func jsonEquivalent(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftJSON, _ := json.Marshal(leftValue)
	rightJSON, _ := json.Marshal(rightValue)
	return string(leftJSON) == string(rightJSON)
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("credential path %s is not a regular file", path)
	}
	return true, nil
}

func readCredentialFile(path string, uid, gid int, mode os.FileMode) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode ||
		int(stat.Uid) != uid || int(stat.Gid) != gid || stat.Nlink != 1 || info.Size() < 1 || info.Size() > maximumCredentialBytes {
		return nil, fmt.Errorf("credential must be a singly linked regular file owned by %d:%d with mode %04o", uid, gid, mode)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytesContainControl(raw) {
		clear(raw)
		return nil, fmt.Errorf("credential is empty or contains control characters")
	}
	return raw, nil
}

func atomicWrite(path string, content []byte, uid, gid int) (err error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".rustfs-cache-credential-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	installed := false
	defer func() {
		if err == nil || !installed {
			return
		}
		cleanupErr := os.Remove(path)
		if cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove uncommitted credential %s: %w", path, cleanupErr))
			return
		}
		if directoryFile, openErr := os.Open(directory); openErr == nil {
			syncErr := directoryFile.Sync()
			closeErr := directoryFile.Close()
			err = errors.Join(err, syncErr, closeErr)
		} else {
			err = errors.Join(err, openErr)
		}
	}()
	if err := temporary.Chmod(credentialFileMode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chown(uid, gid); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// Link into place instead of renaming so a concurrent or unexpected target
	// can never be overwritten. The directory is root-writable only, but this
	// invariant keeps the operation fail-closed independently of that policy.
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	installed = true
	if err := os.Remove(temporaryPath); err != nil {
		return err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	if err := directoryFile.Sync(); err != nil {
		_ = directoryFile.Close()
		return err
	}
	if err := directoryFile.Close(); err != nil {
		return err
	}
	return nil
}

func responseError(operation string, response Response) error {
	digest := sha256.Sum256(response.Body)
	return fmt.Errorf("%s returned HTTP %d (body_bytes=%d body_sha256=%x)",
		operation, response.StatusCode, len(response.Body), digest)
}

func requestFailure(err error, response Response) error {
	if err != nil {
		return err
	}
	return responseError("request", response)
}

func bytesContainControl(value []byte) bool {
	for _, current := range value {
		if current < 0x21 || current > 0x7e {
			return true
		}
	}
	return false
}

func clearManagedCredentials(credentials []managedCredential) {
	for index := range credentials {
		clear(credentials[index].secretKey)
	}
}
