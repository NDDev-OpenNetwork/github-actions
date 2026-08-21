package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/cachebroker"
	"github.com/NDDev-OpenNetwork/github-actions/internal/cachenamespace"
	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
	commonParams "github.com/cloudbase/garm-provider-common/params"
	incus "github.com/lxc/incus/v7/client"
)

const (
	cacheAssignmentDirectory = "/home/runner/.gha-cache"
	cacheAssignmentPath      = cacheAssignmentDirectory + "/provider-assignment.json"
	cacheReadyPath           = cacheAssignmentDirectory + "/provider-assignment.ready"
	cacheConsumedPath        = cacheAssignmentDirectory + "/provider-assignment.consumed"
	cacheHookPath            = cacheAssignmentDirectory + "/job-start.sh"
	cacheCAPath              = cacheAssignmentDirectory + "/rustfs-ca.pem"
	cacheCABundlePath        = cacheAssignmentDirectory + "/rustfs-ca-bundle.pem"
	// The Docker-capable image is larger and brings its daemon up before the
	// guest agent settles, so on rotational storage a cold create was measured
	// at 77s end to end and lost the race with a 75s window. This still sits
	// well inside the five-minute scale set bootstrap timeout.
	cacheInjectionTimeout = 150 * time.Second
)

type cacheDeliveryLoader func(string) (rustfscache.Delivery, error)
type cacheRepositoryLoader func() (string, error)
type cacheClaimLoader func() (cachebroker.Store, string, []byte, error)

type workerCacheClaim struct {
	SchemaVersion int    `json:"schema_version"`
	InstanceName  string `json:"instance_name"`
	ClaimEndpoint string `json:"claim_endpoint"`
	ClaimToken    string `json:"claim_token"`
	CAPEMB64      string `json:"ca_pem_b64"`
}

func productionCacheClaim() (cachebroker.Store, string, []byte, error) {
	config, err := rustfscache.Load(rustfscache.DefaultConfigPath)
	if err != nil {
		return cachebroker.Store{}, "", nil, err
	}
	ca, err := os.ReadFile(config.CAFile)
	if err != nil {
		return cachebroker.Store{}, "", nil, fmt.Errorf("read cache claim CA: %w", err)
	}
	return cachebroker.Store{Path: config.ClaimJournalFile, LockPath: config.ClaimJournalLockFile}, config.ClaimEndpoint, ca, nil
}

func productionCacheRepository() (string, error) {
	config, err := rustfscache.Load(rustfscache.DefaultConfigPath)
	if err != nil {
		return "", err
	}
	return config.Repository()
}

type workerCacheAssignment struct {
	SchemaVersion int    `json:"schema_version"`
	DeliveryID    string `json:"delivery_id"`
	Role          string `json:"role"`
	Mode          string `json:"mode"`
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	PrefixRoot    string `json:"prefix_root"`
	AccessKey     string `json:"access_key"`
	SecretKeyB64  string `json:"secret_key_b64"`
	CAPEMB64      string `json:"ca_pem_b64"`
}

var (
	cacheAccessKeyPattern  = regexp.MustCompile(`^AKIA[0-9A-F]{16}$`)
	cacheSecretPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{64}$`)
	cacheDeliveryIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	cacheBucketPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]-cache$`)
)

func productionCacheDelivery(role string) (rustfscache.Delivery, error) {
	config, err := rustfscache.Load(rustfscache.DefaultConfigPath)
	if err != nil {
		return rustfscache.Delivery{}, err
	}
	if err := config.ValidateProductionPaths(); err != nil {
		return rustfscache.Delivery{}, err
	}
	group, err := user.LookupGroup(cacheCredentialGroup)
	if err != nil {
		return rustfscache.Delivery{}, fmt.Errorf("resolve %q group the deployment grants on the credential directory: %w", cacheCredentialGroup, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return rustfscache.Delivery{}, fmt.Errorf("group %q has a non-numeric gid %q", cacheCredentialGroup, group.Gid)
	}
	return rustfscache.LoadDelivery(config, role, 0, gid, 0, 0)
}

// cacheCredentialGroup is the group deploy/fleet-host/gha-fleet.tmpfiles grants
// on /etc/garm/cache. It used to be os.Getegid() -- the caller's own effective
// group -- so the probe asked "is this owned by root and by whatever group I am
// in", which is a different question and one that happened to be true only for
// the service account. Run as root, which is how the runbook runs it, it
// demanded 0:0 and failed on a correctly deployed directory (#230).
const cacheCredentialGroup = "garm"

func cacheRoleForPool(pool platformconfig.Pool) (string, bool, error) {
	switch {
	case pool.Trust == "trusted" && pool.Capabilities.CacheWriteScope == "trusted":
		return "trusted-writer", true, nil
	case pool.Trust == "untrusted" && pool.Capabilities.CacheWriteScope == "isolated":
		return "untrusted-writer", true, nil
	case pool.Trust == "release" && pool.Capabilities.CacheWriteScope == "none":
		return "release-reader", true, nil
	case pool.Capabilities.CacheWriteScope == "none":
		return "", false, nil
	default:
		return "", false, fmt.Errorf(
			"pool %q has no fail-closed cache identity mapping for trust=%q write_scope=%q",
			pool.Name,
			pool.Trust,
			pool.Capabilities.CacheWriteScope,
		)
	}
}

func cacheDeliveryID(jobName string) string {
	digest := sha256.Sum256([]byte("nddev-worker-cache-delivery-v1\x00" + jobName))
	return hex.EncodeToString(digest[:])
}

func (l *Incus) cacheDeliveryConfigured(bootstrap commonParams.BootstrapInstance) (string, bool, error) {
	pool, exists := l.platform.Pool(bootstrap.Flavor)
	if !exists {
		return "", false, fmt.Errorf("pool policy %q does not exist", bootstrap.Flavor)
	}
	role, enabled, err := cacheRoleForPool(pool)
	if err != nil || !enabled || l.cacheDelivery == nil {
		return role, enabled && l.cacheDelivery != nil, err
	}
	if l.cacheRepository == nil {
		return role, false, nil
	}
	repository, err := canonicalRepositoryIdentity(bootstrap.RepoURL)
	if err != nil {
		if l.isRegisteredRepositoryURL(bootstrap.RepoURL) {
			// Organization JobAssigned is account-only until a runner exists.
			// Creating a credential-free worker is safe inside the reviewed account
			// boundary; guessing which repository cache to grant is not.
			return role, false, nil
		}
		return role, false, fmt.Errorf("derive cache repository identity: %w", err)
	}
	// The current RustFS credentials are scoped to one exact repository. Do not
	// hand that namespace to another repository and hope its workflow notices:
	// a cache mismatch is an optional optimization miss, not authorization to
	// cross the repository boundary. Estate-generated per-repository identities
	// can widen this safely without changing the one-job delivery protocol.
	cacheRepository, err := l.cacheRepository()
	if err != nil {
		return role, false, fmt.Errorf("load configured cache repository identity: %w", err)
	}
	if repository != cacheRepository {
		return role, false, nil
	}
	return role, true, nil
}

func (l *Incus) renderCacheAssignment(bootstrap commonParams.BootstrapInstance) ([]byte, bool, error) {
	role, enabled, err := l.cacheDeliveryConfigured(bootstrap)
	if err != nil || !enabled {
		return nil, enabled, err
	}
	delivery, err := l.cacheDelivery(role)
	if err != nil {
		return nil, true, fmt.Errorf("load %s cache delivery: %w", role, err)
	}
	defer delivery.Clear()
	if err := validateCacheDelivery(role, delivery); err != nil {
		return nil, true, err
	}
	assignment := workerCacheAssignment{
		SchemaVersion: 1,
		DeliveryID:    cacheDeliveryID(bootstrap.Name),
		Role:          delivery.Role,
		Mode:          delivery.Mode,
		Endpoint:      delivery.Endpoint,
		Region:        delivery.Region,
		Bucket:        delivery.Bucket,
		PrefixRoot:    delivery.Prefix,
		AccessKey:     delivery.AccessKey,
		SecretKeyB64:  base64.StdEncoding.EncodeToString(delivery.SecretKey),
		CAPEMB64:      base64.StdEncoding.EncodeToString(delivery.CAPEM),
	}
	raw, err := json.Marshal(assignment)
	if err != nil {
		return nil, true, fmt.Errorf("encode cache delivery: %w", err)
	}
	return raw, true, nil
}

// workerCacheRole is one delivery a worker may receive: which role, in which
// mode, scoped to which namespace root.
type workerCacheRole struct {
	Role   string
	Mode   string
	Prefix string
}

// workerCacheRoles is the closed set of deliveries a worker may receive. It is
// one declaration consumed three times -- by the Go check below and by both jq
// expressions that run inside the guest -- because the three used to be three
// copies of the same list, and a role could have been accepted here on terms the
// guest did not enforce.
//
// The promoter role is deliberately absent: it writes to the promoted namespace
// from the host, and no worker is ever given it.
func workerCacheRoles(repository string) []workerCacheRole {
	return []workerCacheRole{
		{Role: "trusted-writer", Mode: "read-write", Prefix: mustCachePrefix(repository, cachenamespace.Trusted)},
		{Role: "untrusted-writer", Mode: "read-write", Prefix: mustCachePrefix(repository, cachenamespace.Untrusted)},
		{Role: "release-reader", Mode: "read-only", Prefix: mustCachePrefix(repository, cachenamespace.Promoted)},
	}
}

func mustCachePrefix(repository string, class cachenamespace.TrustClass) string {
	prefix, err := cachenamespace.PrefixRootFor(repository, class)
	if err != nil {
		panic(err)
	}
	return prefix
}

// cacheRoleClausePlaceholder is substituted into the guest scripts rather than
// formatted into them. Those scripts are full of printf format verbs, and
// putting them through Sprintf would consume those as arguments.
const cacheRoleClausePlaceholder = "@@NDDEV_CACHE_ROLE_CLAUSE@@"

// cacheRoleJQClause renders the role check the guest performs, from the same
// list the host checks against.
func cacheRoleJQClause() string {
	entries := []struct{ role, mode, class string }{
		{"trusted-writer", "read-write", "trusted"},
		{"untrusted-writer", "read-write", "untrusted"},
		{"release-reader", "read-only", "promoted"},
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf(
			`    (.role == %q and .mode == %q and (.prefix_root | test("^[^/]+/[^/]+/trust/%s$")))`,
			entry.role, entry.mode, entry.class))
	}
	return strings.Join(lines, " or\n")
}

// cacheJobStartedHook is the hook as it is delivered, with the role clause
// resolved.
func cacheJobStartedHook() string {
	return strings.ReplaceAll(cacheJobStartedHookTemplate, cacheRoleClausePlaceholder, cacheRoleJQClause())
}

func validateCacheDelivery(role string, delivery rustfscache.Delivery) error {
	repository, _, err := cachenamespace.ParsePrefixRoot(delivery.Prefix)
	if err != nil {
		return fmt.Errorf("loaded %s cache delivery does not match the worker trust contract", role)
	}
	wanted := make(map[string]workerCacheRole, len(workerCacheRoles(repository)))
	for _, entry := range workerCacheRoles(repository) {
		wanted[entry.Role] = entry
	}
	expected, exists := wanted[role]
	if !exists || delivery.Role != role || delivery.Mode != expected.Mode || delivery.Prefix != expected.Prefix ||
		rustfscache.ValidateEndpoint(delivery.Endpoint) != nil || delivery.Region != "us-east-1" ||
		!cacheBucketPattern.MatchString(delivery.Bucket) || !cacheAccessKeyPattern.MatchString(delivery.AccessKey) ||
		!cacheSecretPattern.Match(delivery.SecretKey) || len(delivery.CAPEM) == 0 {
		return fmt.Errorf("loaded %s cache delivery does not match the worker trust contract", role)
	}
	return nil
}

func renderCacheSetupScript() []byte {
	hook := base64.StdEncoding.EncodeToString([]byte(cacheJobStartedHook()))
	return []byte(strings.ReplaceAll(fmt.Sprintf(`#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

assignment=%q
ready=%q
hook=%q
ca_path=%q
runner_root=/home/runner/actions-runner
wait_seconds="${NDDEV_CACHE_WAIT_SECONDS:-70}"

install -d -o root -g root -m 0700 %q

case "${wait_seconds}" in
  ''|*[!0-9]*) echo "cache delivery wait must be an integer" >&2; exit 1 ;;
esac
deadline=$((SECONDS + wait_seconds))
while [[ ! -e "${ready}" ]]; do
  if (( SECONDS >= deadline )); then
    echo "timed out waiting for one-job cache delivery" >&2
    exit 1
  fi
  sleep 1
done

test ! -L "${assignment}"
test -f "${assignment}"
test "$(stat --format='%%u:%%g:%%a:%%h:%%F' -- "${assignment}")" = '0:0:400:1:regular file'
test ! -L "${ready}"
test -f "${ready}"
test "$(stat --format='%%u:%%g:%%a:%%h:%%F' -- "${ready}")" = '0:0:400:1:regular empty file'

jq -e '
  if .schema_version == 1 then
    (keys | sort) == (["access_key","bucket","ca_pem_b64","delivery_id","endpoint","mode","prefix_root","region","role","schema_version","secret_key_b64"] | sort) and
    (.delivery_id | test("^[0-9a-f]{64}$")) and
    (.endpoint | startswith("https://") and (endswith(":9002") or endswith(":9003"))) and
    .region == "us-east-1" and
    (.bucket | test("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]-cache$")) and
    (.access_key | test("^AKIA[0-9A-F]{16}$")) and
    (.secret_key_b64 | test("^[A-Za-z0-9+/]{86}==$")) and
    (.ca_pem_b64 | test("^[A-Za-z0-9+/=]+$")) and
    (
@@NDDEV_CACHE_ROLE_CLAUSE@@
    )
  elif .schema_version == 2 then
    (keys | sort) == (["ca_pem_b64","claim_endpoint","claim_token","instance_name","schema_version"] | sort) and
    (.instance_name | test("^[a-z][a-z0-9-]{5,63}$")) and
    (.claim_endpoint | startswith("https://") and endswith("/api/v1/cache/claim")) and
    (.claim_token | test("^[A-Za-z0-9_-]{43}$")) and
    (.ca_pem_b64 | test("^[A-Za-z0-9+/=]+$"))
  else false end
' "${assignment}" >/dev/null

ca_temp="$(mktemp /tmp/nddev-cache-ca.XXXXXXXXXX)"
trap 'rm -f -- "${ca_temp}"' EXIT
jq -r '.ca_pem_b64' "${assignment}" | base64 --decode >"${ca_temp}"
openssl x509 -in "${ca_temp}" -noout >/dev/null
install -o runner -g runner -m 0400 "${ca_temp}" "${ca_path}"
bundle_temp="$(mktemp /tmp/nddev-cache-ca-bundle.XXXXXXXXXX)"
trap 'rm -f -- "${ca_temp}" "${bundle_temp}"' EXIT
cat /etc/ssl/certs/ca-certificates.crt "${ca_temp}" >"${bundle_temp}"
install -o runner -g runner -m 0400 "${bundle_temp}" %q

printf '%%s' %q | base64 --decode >"${hook}"
chown root:root "${hook}"
chmod 0755 "${hook}"
install -d -o runner -g runner -m 0755 "${runner_root}"
{
  printf 'ACTIONS_RUNNER_HOOK_JOB_STARTED=%%s\n' "${hook}"
  printf 'SSL_CERT_FILE=%%s\n' %q
  printf 'CURL_CA_BUNDLE=%%s\n' %q
  printf 'AWS_CA_BUNDLE=%%s\n' %q
} >"${runner_root}/.env"
chown runner:runner "${runner_root}/.env"
chmod 0600 "${runner_root}/.env"
chown runner:runner "${assignment}"
chmod 0400 "${assignment}"
chown runner:runner %q
chmod 0700 %q
`, cacheAssignmentPath, cacheReadyPath, cacheHookPath, cacheCAPath,
		cacheAssignmentDirectory, cacheCABundlePath, hook,
		cacheCABundlePath, cacheCABundlePath, cacheCABundlePath,
		cacheAssignmentDirectory, cacheAssignmentDirectory),
		cacheRoleClausePlaceholder, cacheRoleJQClause()))
}

const cacheJobStartedHookTemplate = `#!/usr/bin/env bash
set -Eeuo pipefail
set +x
umask 077

assignment=/home/runner/.gha-cache/provider-assignment.json
ready=/home/runner/.gha-cache/provider-assignment.ready
consumed=/home/runner/.gha-cache/provider-assignment.consumed
ca_path=/home/runner/.gha-cache/rustfs-ca.pem
cleanup() {
  rm -f -- "${assignment}" "${ready}"
}
trap cleanup EXIT

test ! -L "${assignment}"
test -f "${assignment}"
expected_metadata="$(id -u):$(id -g):400:1:regular file"
test "$(stat --format='%u:%g:%a:%h:%F' -- "${assignment}")" = "${expected_metadata}"
test -n "${GITHUB_ENV:-}"
test "${GITHUB_ENV#/}" != "${GITHUB_ENV}"
test ! -L "${GITHUB_ENV}"
test -f "${GITHUB_ENV}"
test "$(stat --format='%u:%g' -- "${GITHUB_ENV}")" = "$(id -u):$(id -g)"

if [[ "$(jq -r '.schema_version' "${assignment}")" == 2 ]]; then
  test -n "${GITHUB_REPOSITORY:-}"
  test -n "${RUNNER_NAME:-}"
  instance_name="$(jq -r '.instance_name' "${assignment}")"
  test "${RUNNER_NAME}" = "${instance_name}"
  claim_endpoint="$(jq -r '.claim_endpoint' "${assignment}")"
  claim_token="$(jq -r '.claim_token' "${assignment}")"
  response="$(mktemp /tmp/nddev-cache-claim.XXXXXXXXXX)"
  chmod 0600 "${response}"
  trap 'rm -f -- "${assignment}" "${ready}" "${response}"' EXIT
  if ! jq -nc --arg instance "${instance_name}" --arg runner "${RUNNER_NAME}" \
      --arg repository "${GITHUB_REPOSITORY}" --arg token "${claim_token}" \
      '{instance_name:$instance,runner_name:$runner,repository:$repository,claim_token:$token}' |
      curl --silent --show-error --fail --max-time 10 --cacert "${ca_path}" \
        --header 'Content-Type: application/json' --data-binary @- "${claim_endpoint}" >"${response}"; then
    printf 'repository-scoped compiler cache claim is unavailable; continuing without cache\n' >&2
    exit 0
  fi
  unset claim_token
  if [[ ! -s "${response}" ]]; then
    printf 'repository-scoped compiler cache is not configured; continuing without cache\n'
    exit 0
  fi
  jq -e '.schema_version == 1' "${response}" >/dev/null
  install -o "$(id -u)" -g "$(id -g)" -m 0400 "${response}" "${assignment}"
fi

jq -e '
  (keys | sort) == (["access_key","bucket","ca_pem_b64","delivery_id","endpoint","mode","prefix_root","region","role","schema_version","secret_key_b64"] | sort) and
  .schema_version == 1 and
  (.delivery_id | test("^[0-9a-f]{64}$")) and
  (.endpoint | startswith("https://") and (endswith(":9002") or endswith(":9003"))) and
  .region == "us-east-1" and
  (.bucket | test("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]-cache$")) and
  (.access_key | test("^AKIA[0-9A-F]{16}$")) and
  (
@@NDDEV_CACHE_ROLE_CLAUSE@@
  )
' "${assignment}" >/dev/null

delivery_id="$(jq -r '.delivery_id' "${assignment}")"
role="$(jq -r '.role' "${assignment}")"
mode="$(jq -r '.mode' "${assignment}")"
endpoint="$(jq -r '.endpoint' "${assignment}")"
region="$(jq -r '.region' "${assignment}")"
bucket="$(jq -r '.bucket' "${assignment}")"
prefix="$(jq -r '.prefix_root' "${assignment}")"
access_key="$(jq -r '.access_key' "${assignment}")"
secret_key="$(jq -r '.secret_key_b64' "${assignment}" | base64 --decode)"
test "${#secret_key}" -eq 64
[[ "${secret_key}" =~ ^[A-Za-z0-9_-]{64}$ ]]

printf '::add-mask::%s\n' "${access_key}"
printf '::add-mask::%s\n' "${secret_key}"
{
  printf 'AWS_ACCESS_KEY_ID=%s\n' "${access_key}"
  printf 'AWS_SECRET_ACCESS_KEY=%s\n' "${secret_key}"
  printf 'AWS_REGION=%s\n' "${region}"
  printf 'AWS_DEFAULT_REGION=%s\n' "${region}"
  printf 'SCCACHE_BUCKET=%s\n' "${bucket}"
  printf 'SCCACHE_ENDPOINT=%s\n' "${endpoint}"
  printf 'SCCACHE_REGION=%s\n' "${region}"
  printf 'SCCACHE_S3_USE_SSL=true\n'
  printf 'NDDEV_CACHE_DELIVERY_ID=%s\n' "${delivery_id}"
  printf 'NDDEV_CACHE_ROLE=%s\n' "${role}"
  printf 'NDDEV_CACHE_MODE=%s\n' "${mode}"
  printf 'NDDEV_CACHE_PREFIX_ROOT=%s\n' "${prefix}"
} >>"${GITHUB_ENV}"

printf '%s\n' "${delivery_id}" >"${consumed}"
chmod 0600 "${consumed}"
unset secret_key
`

func (l *Incus) injectColdCacheAssignment(ctx context.Context, instanceName string, bootstrap commonParams.BootstrapInstance) error {
	raw, enabled, err := l.renderCacheAssignment(bootstrap)
	if err != nil {
		return err
	}
	var claimStore *cachebroker.Store
	if !enabled {
		raw, claimStore, enabled, err = l.renderCacheClaim(ctx, instanceName, bootstrap)
		if err != nil || !enabled {
			return err
		}
	}
	defer clear(raw)
	claimCommitted := claimStore != nil
	defer func() {
		if claimCommitted {
			_ = claimStore.Remove(context.Background(), instanceName)
		}
	}()
	cli, err := l.getCLI(ctx)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(cacheInjectionTimeout)
	for {
		err = cli.CreateInstanceFile(instanceName, cacheAssignmentPath, incus.InstanceFileArgs{
			Content: bytes.NewReader(raw), UID: 0, GID: 0, Mode: 0o400, Type: "file", WriteMode: "overwrite",
		})
		if err == nil {
			break
		}
		instance, _, inspectErr := cli.GetInstanceFull(instanceName)
		if inspectErr == nil && instance.State != nil && instance.State.Status != "Running" {
			return fmt.Errorf("inject one-job cache assignment: instance stopped during canceled create")
		}
		if time.Now().After(deadline) {
			// Carry the last rejection. Without it every cause — agent not up
			// yet, missing parent directory, a guest filesystem error — reads
			// as the same sentence, and the operator has to reproduce the push
			// by hand to learn which one it was.
			return fmt.Errorf("inject one-job cache assignment: guest agent did not accept the bounded file within %s: %w", cacheInjectionTimeout, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("inject one-job cache assignment: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	if err := cli.CreateInstanceFile(instanceName, cacheReadyPath, incus.InstanceFileArgs{
		Content: bytes.NewReader(nil), UID: 0, GID: 0, Mode: 0o400, Type: "file", WriteMode: "overwrite",
	}); err != nil {
		return fmt.Errorf("publish one-job cache assignment readiness")
	}
	claimCommitted = false
	return nil
}

func (l *Incus) renderCacheClaim(ctx context.Context, instanceName string, bootstrap commonParams.BootstrapInstance) ([]byte, *cachebroker.Store, bool, error) {
	pool, exists := l.platform.Pool(bootstrap.Flavor)
	if !exists {
		return nil, nil, false, fmt.Errorf("pool policy %q does not exist", bootstrap.Flavor)
	}
	role, enabled, err := cacheRoleForPool(pool)
	if err != nil || !enabled || l.cacheClaim == nil {
		return nil, nil, false, err
	}
	store, endpoint, ca, err := l.cacheClaim()
	if err != nil {
		return nil, nil, true, fmt.Errorf("load cache claim contract: %w", err)
	}
	defer clear(ca)
	if err := cachebroker.ValidateClaimEndpoint(endpoint); err != nil {
		return nil, nil, true, err
	}
	random := l.cacheClaimRandom
	if random == nil {
		random = rand.Reader
	}
	token := make([]byte, cachebroker.ClaimTokenBytes)
	if _, err := io.ReadFull(random, token); err != nil {
		return nil, nil, true, fmt.Errorf("generate cache claim: %w", err)
	}
	defer clear(token)
	if err := store.Add(ctx, instanceName, pool.Name, role, token); err != nil {
		return nil, nil, true, fmt.Errorf("persist cache claim: %w", err)
	}
	claim := workerCacheClaim{SchemaVersion: 2, InstanceName: instanceName, ClaimEndpoint: endpoint, ClaimToken: base64.RawURLEncoding.EncodeToString(token), CAPEMB64: base64.StdEncoding.EncodeToString(ca)}
	raw, err := json.Marshal(claim)
	if err != nil {
		_ = store.Remove(context.Background(), instanceName)
		return nil, nil, true, err
	}
	return raw, &store, true, nil
}

func (l *Incus) coldCacheDeliveryPresent(ctx context.Context, instanceName string, bootstrap commonParams.BootstrapInstance) (bool, error) {
	role, enabled, err := l.cacheDeliveryConfigured(bootstrap)
	if err != nil || !enabled {
		return !enabled, err
	}
	imagePolicy, err := l.workerImagePolicy(bootstrap.Flavor)
	if err != nil {
		return false, err
	}
	runnerUID, runnerGID := imagePolicy.RunnerUID, imagePolicy.RunnerGID
	cli, err := l.getCLI(ctx)
	if err != nil {
		return false, err
	}
	expected := cacheDeliveryID(bootstrap.Name) + "\n"
	content, response, err := cli.GetInstanceFile(instanceName, cacheConsumedPath)
	if err != nil {
		if !isNotFoundError(err) {
			return false, fmt.Errorf("inspect cache delivery consumption: %w", err)
		}
	} else {
		defer content.Close()
		evidence, err := io.ReadAll(io.LimitReader(content, 129))
		if err != nil {
			return false, fmt.Errorf("read cache delivery consumption: %w", err)
		}
		if response == nil || response.Type != "file" || response.Mode != 0o600 ||
			!cacheDeliveryOwnerMatches(response, runnerUID, runnerGID, false) ||
			len(evidence) > 128 || string(evidence) != expected {
			return false, fmt.Errorf("cache delivery consumption evidence is invalid")
		}
		return true, nil
	}

	content, response, err = cli.GetInstanceFile(instanceName, cacheReadyPath)
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect cache delivery readiness: %w", err)
	}
	ready, err := io.ReadAll(io.LimitReader(content, 2))
	closeErr := content.Close()
	if err != nil || closeErr != nil || response == nil || response.Type != "file" || response.Mode != 0o400 ||
		response.UID != 0 || response.GID != 0 || len(ready) != 0 {
		return false, fmt.Errorf("cache delivery readiness evidence is invalid")
	}
	content, response, err = cli.GetInstanceFile(instanceName, cacheAssignmentPath)
	if err != nil {
		return false, fmt.Errorf("inspect ready cache assignment: %w", err)
	}
	assignmentRaw, err := io.ReadAll(io.LimitReader(content, rustfscache.MaximumConfigBytes+1))
	closeErr = content.Close()
	defer clear(assignmentRaw)
	if err != nil || closeErr != nil || len(assignmentRaw) > rustfscache.MaximumConfigBytes || response == nil ||
		response.Type != "file" || response.Mode != 0o400 ||
		!cacheDeliveryOwnerMatches(response, runnerUID, runnerGID, true) {
		return false, fmt.Errorf("ready cache assignment evidence is invalid")
	}
	assignment, err := parseWorkerCacheAssignment(assignmentRaw)
	if err != nil || assignment.SchemaVersion != 1 ||
		assignment.DeliveryID != cacheDeliveryID(bootstrap.Name) || assignment.Role != role {
		return false, fmt.Errorf("ready cache assignment is not bound to the requested job")
	}
	return true, nil
}

func cacheDeliveryOwnerMatches(response *incus.InstanceFileResponse, runnerUID, runnerGID int64, allowRoot bool) bool {
	if response == nil {
		return false
	}
	if allowRoot && response.UID == 0 && response.GID == 0 {
		return true
	}
	return response.UID == runnerUID && response.GID == runnerGID
}

func parseWorkerCacheAssignment(raw []byte) (workerCacheAssignment, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var assignment workerCacheAssignment
	if err := decoder.Decode(&assignment); err != nil {
		return workerCacheAssignment{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return workerCacheAssignment{}, fmt.Errorf("cache assignment must contain exactly one JSON document")
	}
	secret, err := base64.StdEncoding.DecodeString(assignment.SecretKeyB64)
	if err != nil {
		return workerCacheAssignment{}, fmt.Errorf("decode cache assignment secret")
	}
	defer clear(secret)
	ca, err := base64.StdEncoding.DecodeString(assignment.CAPEMB64)
	if err != nil {
		return workerCacheAssignment{}, fmt.Errorf("decode cache assignment CA")
	}
	defer clear(ca)
	delivery := rustfscache.Delivery{
		Role: assignment.Role, Endpoint: assignment.Endpoint, Region: assignment.Region,
		Bucket: assignment.Bucket, Prefix: assignment.PrefixRoot, Mode: assignment.Mode,
		AccessKey: assignment.AccessKey, SecretKey: secret, CAPEM: ca,
	}
	if assignment.SchemaVersion != 1 || !cacheDeliveryIDPattern.MatchString(assignment.DeliveryID) {
		return workerCacheAssignment{}, fmt.Errorf("cache assignment identity is invalid")
	}
	if err := validateCacheDelivery(assignment.Role, delivery); err != nil {
		return workerCacheAssignment{}, err
	}
	return assignment, nil
}

func cacheSetupPreInstallScript() []byte {
	return renderCacheSetupScript()
}

func mergeCacheIntoWarmAssignment(assignment, cache []byte) []byte {
	if len(cache) == 0 {
		return assignment
	}
	setupB64 := base64.StdEncoding.EncodeToString(renderCacheSetupScript())
	cacheB64 := base64.StdEncoding.EncodeToString(cache)
	prefix := fmt.Sprintf(`CACHE_SETUP_B64=%q
CACHE_ASSIGNMENT_B64=%q
cache_setup="$(mktemp /tmp/gha-cache-setup.XXXXXXXXXX)"
cleanup_cache_setup() {
  rm -f -- "${cache_setup}"
}
trap cleanup_cache_setup EXIT
printf '%%s' "${CACHE_SETUP_B64}" | base64 --decode >"${cache_setup}"
chmod 0700 "${cache_setup}"
sudo install -d -o root -g root -m 0700 %q
printf '%%s' "${CACHE_ASSIGNMENT_B64}" | base64 --decode | sudo tee %q >/dev/null
sudo chown root:root %q
sudo chmod 0400 %q
sudo install -o root -g root -m 0400 /dev/null %q
sudo env NDDEV_CACHE_WAIT_SECONDS=0 /bin/bash "${cache_setup}"
rm -f -- "${cache_setup}"
trap - EXIT
`, setupB64, cacheB64, cacheAssignmentDirectory, cacheAssignmentPath,
		cacheAssignmentPath, cacheAssignmentPath, cacheReadyPath)
	needle := []byte("# NDDEV_CACHE_SETUP_INSERTION_POINT")
	index := bytes.Index(assignment, needle)
	if index < 0 {
		return nil
	}
	merged := make([]byte, 0, len(assignment)+len(prefix))
	merged = append(merged, assignment[:index]...)
	merged = append(merged, prefix...)
	merged = append(merged, assignment[index:]...)
	return merged
}
