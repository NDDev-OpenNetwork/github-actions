package rustfscache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	testRootAccess = "ROOTACCESSKEY1234"
	testRootSecret = "root-secret-material-with-more-than-thirty-two-bytes"
)

func TestRunnerCreatesAndVerifiesTrustSeparatedIdentities(t *testing.T) {
	t.Parallel()

	config, options := prepareRunnerTest(t)
	remote := newFakeRustFS(config)
	random := make([]byte, secretBytes*len(config.Identities))
	for block := range config.Identities {
		for index := 0; index < secretBytes; index++ {
			random[block*secretBytes+index] = byte(block + 1)
		}
	}
	runner := Runner{
		Requester: remote,
		Random:    bytes.NewReader(random),
		Sleep:     func(context.Context, time.Duration) error { return nil },
	}

	plan, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatalf("plan fresh state: %v", err)
	}
	if plan.Applied || plan.LocalState != "fresh" || plan.StateBefore != "fresh" || plan.StateAfter != "fresh" || len(plan.Actions) == 0 {
		t.Fatalf("unexpected fresh plan: %+v", plan)
	}
	if len(remote.users) != 0 || remote.bucket {
		t.Fatal("plan mutated remote state")
	}

	options.Apply = true
	result, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatalf("apply fresh state: %v", err)
	}
	if !result.Applied || result.LocalState != "fresh" || result.StateBefore != "fresh" || result.StateAfter != "managed" {
		t.Fatalf("unexpected apply result: %+v", result)
	}
	if !remote.bucket || remote.quota != config.QuotaBytes {
		t.Fatalf("bucket=%v quota=%d", remote.bucket, remote.quota)
	}
	if got := strings.Count(string(remote.lifecycle), "<Rule>"); got != 3 {
		t.Fatalf("lifecycle rule count = %d: %s", got, remote.lifecycle)
	}
	if len(remote.objects) != 0 {
		t.Fatalf("effective-policy probes left %d objects", len(remote.objects))
	}

	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	accessPattern := regexp.MustCompile(`^AKIA[0-9A-F]{16}$`)
	seenSecrets := make(map[string]struct{}, len(result.Identities))
	for _, identity := range result.Identities {
		if !accessPattern.MatchString(identity.AccessKey) {
			t.Errorf("invalid access key for %s: %q", identity.Role, identity.AccessKey)
		}
		for _, path := range []string{identity.AccessKeyFile, identity.SecretKeyFile} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %s: %v", path, err)
			}
			if info.Mode().Perm() != credentialFileMode {
				t.Errorf("mode for %s = %04o", path, info.Mode().Perm())
			}
		}
		secret, err := os.ReadFile(identity.SecretKeyFile)
		if err != nil {
			t.Fatalf("read %s secret: %v", identity.Role, err)
		}
		secret = bytes.TrimSpace(secret)
		if len(secret) != 64 {
			t.Errorf("secret length for %s = %d", identity.Role, len(secret))
		}
		if _, duplicate := seenSecrets[string(secret)]; duplicate {
			t.Errorf("duplicate secret for %s", identity.Role)
		}
		seenSecrets[string(secret)] = struct{}{}
		if bytes.Contains(encodedResult, secret) {
			t.Errorf("result leaked %s secret", identity.Role)
		}
		clear(secret)
	}

	options.Apply = false
	secondPlan, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatalf("plan managed state: %v", err)
	}
	if secondPlan.Applied || secondPlan.LocalState != "managed" || secondPlan.StateBefore != "managed" || secondPlan.StateAfter != "managed" || len(secondPlan.Actions) != 0 {
		t.Fatalf("unexpected idempotent plan: %+v", secondPlan)
	}
	options.Apply = true
	managedApply, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatalf("apply managed state: %v", err)
	}
	if !managedApply.Applied || managedApply.StateBefore != "managed" || managedApply.StateAfter != "managed" ||
		len(managedApply.Actions) != 1 || managedApply.Actions[0] != "verify_effective_boundaries" {
		t.Fatalf("unexpected managed apply: %+v", managedApply)
	}
	if len(remote.objects) != 0 {
		t.Fatalf("managed effective-policy probes left %d objects", len(remote.objects))
	}
	options.Apply = false

	policyName := config.SortedIdentities()[0].Policy
	remote.policyEnvelopeNames[policyName] = "unexpected-policy-name"
	driftPlan, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatalf("plan policy envelope drift: %v", err)
	}
	if driftPlan.StateAfter != "provisioning" {
		t.Fatalf("policy envelope drift was not detected: %+v", driftPlan)
	}
	delete(remote.policyEnvelopeNames, policyName)

	remote.quota--
	driftPlan, err = runner.Run(context.Background(), options)
	if err != nil {
		t.Fatalf("plan quota drift: %v", err)
	}
	if driftPlan.LocalState != "managed" || driftPlan.StateBefore != "provisioning" || driftPlan.StateAfter != "provisioning" || len(driftPlan.Actions) == 0 {
		t.Fatalf("quota drift was not detected: %+v", driftPlan)
	}
	remote.quota++
	originalLifecycle := append([]byte(nil), remote.lifecycle...)
	remote.lifecycle = []byte("<LifecycleConfiguration/>")
	driftPlan, err = runner.Run(context.Background(), options)
	if err != nil {
		t.Fatalf("plan lifecycle drift: %v", err)
	}
	if driftPlan.StateAfter != "provisioning" {
		t.Fatalf("lifecycle drift was not detected: %+v", driftPlan)
	}
	remote.lifecycle = originalLifecycle
}

func TestRunnerRejectsRemoteIdentityWithoutLocalSecret(t *testing.T) {
	t.Parallel()

	config, options := prepareRunnerTest(t)
	remote := newFakeRustFS(config)
	identity := config.SortedIdentities()[0]
	accessKey := accessKeyForRole(identity.Role)
	remote.users[accessKey] = fakeUser{secret: "remote-only-secret", policy: identity.Policy, status: "enabled"}

	_, err := (Runner{Requester: remote}).Run(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "exists without local credential") {
		t.Fatalf("Run error = %v", err)
	}
}

func TestRunnerRejectsManagedIdentityWithWrongRemoteSecret(t *testing.T) {
	t.Parallel()

	config, options := prepareRunnerTest(t)
	remote := newFakeRustFS(config)
	random := make([]byte, secretBytes*len(config.Identities))
	for block := range config.Identities {
		for index := 0; index < secretBytes; index++ {
			random[block*secretBytes+index] = byte(block + 1)
		}
	}
	runner := Runner{Requester: remote, Random: bytes.NewReader(random), Sleep: func(context.Context, time.Duration) error { return nil }}
	options.Apply = true
	result, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatalf("initial apply: %v", err)
	}
	accessKey := result.Identities[0].AccessKey
	user := remote.users[accessKey]
	user.secret = "wrong-remote-secret"
	remote.users[accessKey] = user
	options.Apply = false

	_, err = runner.Run(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "bucket-location access failed") {
		t.Fatalf("Run error = %v", err)
	}
}

func TestRunnerRejectsPartialLocalCredentialSet(t *testing.T) {
	t.Parallel()

	config, options := prepareRunnerTest(t)
	path := filepath.Join(config.CredentialsDirectory, "rustfs-promoter-access-key")
	if err := os.WriteFile(path, []byte(accessKeyForRole("promoter")+"\n"), credentialFileMode); err != nil {
		t.Fatalf("write partial credential: %v", err)
	}

	_, err := (Runner{Requester: newFakeRustFS(config)}).Run(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "credential pair is incomplete") {
		t.Fatalf("Run error = %v", err)
	}
}

func TestCredentialWriteRollsBackIncompleteFreshSet(t *testing.T) {
	t.Parallel()

	config, options := prepareRunnerTest(t)
	credentials, err := generateManagedCredentials(options, bytes.NewReader(bytes.Repeat([]byte{0x5a}, secretBytes*len(config.Identities))))
	if err == nil {
		// Identical random blocks are intentionally rejected. Generate distinct
		// blocks for the actual rollback probe.
		clearManagedCredentials(credentials)
		t.Fatal("generateManagedCredentials accepted duplicate secrets")
	}
	random := make([]byte, secretBytes*len(config.Identities))
	for block := range config.Identities {
		for index := 0; index < secretBytes; index++ {
			random[block*secretBytes+index] = byte(0x20 + block)
		}
	}
	credentials, err = generateManagedCredentials(options, bytes.NewReader(random))
	if err != nil {
		t.Fatalf("generate credentials: %v", err)
	}
	defer clearManagedCredentials(credentials)
	blockingPath := credentials[1].secretKeyFile
	if err := os.Mkdir(blockingPath, 0o700); err != nil {
		t.Fatalf("create blocking path: %v", err)
	}

	if err := writeManagedCredentials(options, credentials); err == nil {
		t.Fatal("writeManagedCredentials unexpectedly succeeded")
	}
	for _, credential := range credentials {
		for _, path := range []string{credential.accessKeyFile, credential.secretKeyFile} {
			if path == blockingPath {
				continue
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("rollback left %s: %v", path, err)
			}
		}
	}
	if info, err := os.Stat(blockingPath); err != nil || !info.IsDir() {
		t.Fatalf("blocking path was modified: info=%v err=%v", info, err)
	}
}

func TestResponseErrorDoesNotReflectBody(t *testing.T) {
	t.Parallel()

	secret := "should-never-be-reflected"
	err := responseError("operation", Response{StatusCode: http.StatusBadRequest, Body: []byte(secret)})
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "body_sha256=") {
		t.Fatalf("unsafe response error: %v", err)
	}
}

func TestAtomicWriteNeverReplacesExistingCredential(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "credential")
	if err := os.WriteFile(path, []byte("original\n"), credentialFileMode); err != nil {
		t.Fatalf("write original: %v", err)
	}
	if err := atomicWrite(path, []byte("replacement\n"), os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("atomicWrite unexpectedly replaced an existing path")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if string(content) != "original\n" {
		t.Fatalf("existing credential changed: %q", content)
	}
}

func prepareRunnerTest(t *testing.T) (Config, Options) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o750); err != nil {
		t.Fatalf("chmod credential directory: %v", err)
	}
	config := testConfig(t, directory)
	if err := os.WriteFile(config.RootAccessKeyFile, []byte(testRootAccess+"\n"), 0o600); err != nil {
		t.Fatalf("write root access key: %v", err)
	}
	if err := os.WriteFile(config.RootSecretKeyFile, []byte(testRootSecret+"\n"), 0o600); err != nil {
		t.Fatalf("write root secret key: %v", err)
	}
	uid, gid := os.Getuid(), os.Getgid()
	return config, Options{
		Config: config, RootOwnerUID: uid, RootOwnerGID: gid, OwnerUID: uid, OwnerGID: gid,
	}
}

type fakeUser struct {
	secret string
	policy string
	status string
}

type fakeRustFS struct {
	config              Config
	identitiesByKey     map[string]Identity
	users               map[string]fakeUser
	policies            map[string][]byte
	policyEnvelopeNames map[string]string
	objects             map[string][]byte
	bucket              bool
	quota               int64
	lifecycle           []byte
}

func newFakeRustFS(config Config) *fakeRustFS {
	identities := make(map[string]Identity, len(config.Identities))
	for _, identity := range config.Identities {
		identities[accessKeyForRole(identity.Role)] = identity
	}
	return &fakeRustFS{
		config: config, identitiesByKey: identities, users: make(map[string]fakeUser),
		policies: make(map[string][]byte), policyEnvelopeNames: make(map[string]string),
		objects: make(map[string][]byte),
	}
}

func (f *fakeRustFS) Do(
	_ context.Context,
	credential Credential,
	method, requestPath, _ string,
	body []byte,
) (Response, error) {
	parsed, err := url.Parse(requestPath)
	if err != nil {
		return Response{}, err
	}
	root := credential.AccessKey == testRootAccess && string(credential.SecretKey) == testRootSecret
	admin := func() (Response, bool) {
		if !strings.HasPrefix(parsed.Path, "/rustfs/admin/v3/") {
			return Response{}, false
		}
		if !root {
			return status(http.StatusForbidden), true
		}
		switch {
		case method == http.MethodGet && parsed.Path == "/rustfs/admin/v3/user-info":
			accessKey := parsed.Query().Get("accessKey")
			user, exists := f.users[accessKey]
			if !exists {
				return status(http.StatusNotFound), true
			}
			encoded, _ := json.Marshal(userInfo{PolicyName: user.policy, Status: user.status})
			return Response{StatusCode: http.StatusOK, Body: encoded}, true
		case method == http.MethodGet && parsed.Path == "/rustfs/admin/v3/info-canned-policy":
			policyName := parsed.Query().Get("name")
			policy, exists := f.policies[policyName]
			if !exists {
				return status(http.StatusNotFound), true
			}
			envelopeName := policyName
			if override := f.policyEnvelopeNames[policyName]; override != "" {
				envelopeName = override
			}
			encoded, _ := json.Marshal(policyInfo{PolicyName: envelopeName, Policy: append([]byte(nil), policy...)})
			return Response{StatusCode: http.StatusOK, Body: encoded}, true
		case method == http.MethodPut && parsed.Path == "/rustfs/admin/v3/add-canned-policy":
			f.policies[parsed.Query().Get("name")] = append([]byte(nil), body...)
			return status(http.StatusOK), true
		case method == http.MethodPut && parsed.Path == "/rustfs/admin/v3/add-user":
			var request struct {
				SecretKey string `json:"secretKey"`
				Status    string `json:"status"`
			}
			if json.Unmarshal(body, &request) != nil || request.SecretKey == "" || request.Status != "enabled" {
				return status(http.StatusBadRequest), true
			}
			f.users[parsed.Query().Get("accessKey")] = fakeUser{secret: request.SecretKey, status: request.Status}
			return status(http.StatusOK), true
		case method == http.MethodPut && parsed.Path == "/rustfs/admin/v3/set-user-or-group-policy":
			accessKey := parsed.Query().Get("userOrGroup")
			user, exists := f.users[accessKey]
			if !exists || parsed.Query().Get("isGroup") != "false" {
				return status(http.StatusNotFound), true
			}
			user.policy = parsed.Query().Get("policyName")
			f.users[accessKey] = user
			return status(http.StatusOK), true
		case method == http.MethodGet && parsed.Path == "/rustfs/admin/v3/get-bucket-quota" && parsed.Query().Get("bucket") == f.config.Bucket:
			if f.quota == 0 {
				return status(http.StatusNotFound), true
			}
			encoded, _ := json.Marshal(quotaInfo{Quota: f.quota, QuotaTypeCompat: "hard"})
			return Response{StatusCode: http.StatusOK, Body: encoded}, true
		case method == http.MethodPut && parsed.Path == "/rustfs/admin/v3/quota/"+f.config.Bucket:
			var request struct {
				Quota int64 `json:"quota"`
			}
			if json.Unmarshal(body, &request) != nil {
				return status(http.StatusBadRequest), true
			}
			f.quota = request.Quota
			return status(http.StatusOK), true
		default:
			return status(http.StatusNotFound), true
		}
	}
	if response, handled := admin(); handled {
		return response, nil
	}

	bucketPath := "/" + f.config.Bucket
	if parsed.Path == bucketPath && parsed.RawQuery == "" && method == http.MethodHead {
		if !root {
			return status(http.StatusForbidden), nil
		}
		if !f.bucket {
			return status(http.StatusNotFound), nil
		}
		return status(http.StatusOK), nil
	}
	if parsed.Path == bucketPath && parsed.RawQuery == "lifecycle" && method == http.MethodGet {
		if !root {
			return status(http.StatusForbidden), nil
		}
		if len(f.lifecycle) == 0 {
			return status(http.StatusNotFound), nil
		}
		return Response{StatusCode: http.StatusOK, Body: append([]byte(nil), f.lifecycle...)}, nil
	}
	if parsed.Path == bucketPath && parsed.RawQuery == "lifecycle" && method == http.MethodPut {
		if !root {
			return status(http.StatusForbidden), nil
		}
		f.lifecycle = append([]byte(nil), body...)
		return status(http.StatusOK), nil
	}
	if parsed.Path == bucketPath && parsed.RawQuery == "" && method == http.MethodPut {
		if !root {
			return status(http.StatusForbidden), nil
		}
		if f.bucket {
			return status(http.StatusConflict), nil
		}
		f.bucket = true
		return status(http.StatusOK), nil
	}
	if parsed.Path == bucketPath && parsed.RawQuery == "location" && method == http.MethodGet {
		if root {
			return status(http.StatusOK), nil
		}
		user, exists := f.users[credential.AccessKey]
		identity, managed := f.identitiesByKey[credential.AccessKey]
		if exists && managed && user.status == "enabled" && user.secret == string(credential.SecretKey) && user.policy == identity.Policy {
			return status(http.StatusOK), nil
		}
		return status(http.StatusForbidden), nil
	}
	if !strings.HasPrefix(parsed.Path, bucketPath+"/") {
		return status(http.StatusNotFound), nil
	}
	key := strings.TrimPrefix(parsed.Path, bucketPath+"/")
	if root {
		return f.objectRequest(method, key, body, true, true, true), nil
	}
	user, exists := f.users[credential.AccessKey]
	identity, managed := f.identitiesByKey[credential.AccessKey]
	authenticated := exists && managed && user.status == "enabled" && user.secret == string(credential.SecretKey) && user.policy == identity.Policy
	insidePrefix := strings.HasPrefix(key, identity.Prefix+"/")
	canRead := authenticated && insidePrefix
	canWrite := canRead && identity.Mode == "read-write"
	return f.objectRequest(method, key, body, canRead, canWrite, false), nil
}

func (f *fakeRustFS) objectRequest(method, key string, body []byte, canRead, canWrite, canDelete bool) Response {
	switch method {
	case http.MethodGet:
		if !canRead {
			return status(http.StatusForbidden)
		}
		value, exists := f.objects[key]
		if !exists {
			return status(http.StatusNotFound)
		}
		return Response{StatusCode: http.StatusOK, Body: append([]byte(nil), value...)}
	case http.MethodPut:
		if !canWrite {
			return status(http.StatusForbidden)
		}
		f.objects[key] = append([]byte(nil), body...)
		return status(http.StatusOK)
	case http.MethodDelete:
		if !canDelete {
			return status(http.StatusForbidden)
		}
		delete(f.objects, key)
		return status(http.StatusNoContent)
	default:
		return status(http.StatusMethodNotAllowed)
	}
}

func status(code int) Response {
	return Response{StatusCode: code, Body: []byte(fmt.Sprintf("status-%d", code))}
}
