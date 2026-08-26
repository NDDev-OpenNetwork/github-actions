package cachebroker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
	"github.com/NDDev-OpenNetwork/github-actions/internal/telemetryattrs"
)

const (
	ClaimPath              = "/api/v1/cache/claim"
	HealthPath             = "/healthz"
	maximumRequestBytes    = 4096
	maximumCredentialBytes = 512
)

type ClaimRequest struct {
	InstanceName  string `json:"instance_name"`
	RunnerName    string `json:"runner_name"`
	Repository    string `json:"repository"`
	RepositoryID  int64  `json:"repository_id,omitempty"`
	WorkflowRunID int64  `json:"workflow_run_id,omitempty"`
	RunAttempt    int64  `json:"run_attempt,omitempty"`
	JobName       string `json:"job_name,omitempty"`
	WorkflowRef   string `json:"workflow_ref,omitempty"`
	CommitSHA     string `json:"commit_sha,omitempty"`
	Token         string `json:"claim_token"`
}

type Delivery struct {
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

type Handler struct {
	Config          Config
	Store           Store
	QueueCorrelator *queueintent.Correlator
	Logger          *slog.Logger
	Now             func() time.Time
}

func (h Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request != nil && request.URL != nil && request.URL.Path == HealthPath {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := h.Config.Validate(); err != nil {
			http.Error(writer, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		if _, err := h.Store.Read(request.Context()); err != nil {
			http.Error(writer, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		if h.QueueCorrelator != nil {
			if err := h.QueueCorrelator.Ready(request.Context()); err != nil {
				http.Error(writer, "unhealthy", http.StatusServiceUnavailable)
				return
			}
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok\n"))
		return
	}
	if request == nil || request.URL == nil || request.URL.Path != ClaimPath {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maximumRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var claimRequest ClaimRequest
	if err := decoder.Decode(&claimRequest); err != nil {
		deny(logger, writer, request, http.StatusBadRequest, "invalid request")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		deny(logger, writer, request, http.StatusBadRequest, "trailing request data")
		return
	}
	if claimRequest.InstanceName != claimRequest.RunnerName || !instancePattern.MatchString(claimRequest.InstanceName) {
		deny(logger, writer, request, http.StatusForbidden, "runner identity mismatch")
		return
	}
	if _, _, err := splitRepository(claimRequest.Repository); err != nil {
		deny(logger, writer, request, http.StatusBadRequest, "invalid repository")
		return
	}
	if err := validateJobCorrelation(claimRequest); err != nil {
		deny(logger, writer, request, http.StatusBadRequest, "invalid job correlation")
		return
	}
	token, err := base64.RawURLEncoding.DecodeString(claimRequest.Token)
	if err != nil || len(token) != ClaimTokenBytes {
		deny(logger, writer, request, http.StatusForbidden, "invalid claim")
		return
	}
	defer clear(token)
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	claim, err := h.Store.Verify(ctx, claimRequest.InstanceName, token)
	if err != nil {
		deny(logger, writer, request, http.StatusForbidden, "claim refused")
		return
	}
	if claimRequest.WorkflowRunID > 0 {
		if h.QueueCorrelator != nil {
			result, bindErr := h.QueueCorrelator.BindRunning(ctx, queueintent.RunningCorrelation{
				RunnerName: claimRequest.RunnerName, Repository: claimRequest.Repository,
				WorkflowRunID: claimRequest.WorkflowRunID, JobDisplayName: claimRequest.JobName,
				WorkflowRef: claimRequest.WorkflowRef,
			})
			if bindErr != nil {
				logger.WarnContext(ctx, "queue running correlation deferred",
					telemetryattrs.InstanceName, claimRequest.InstanceName,
					telemetryattrs.GitHubRepository, claimRequest.Repository,
					telemetryattrs.GitHubWorkflowRunID, claimRequest.WorkflowRunID,
					"error", bindErr)
			} else {
				logger.InfoContext(ctx, "queue running correlation bound",
					telemetryattrs.InstanceName, claimRequest.InstanceName,
					telemetryattrs.GitHubRepository, claimRequest.Repository,
					telemetryattrs.GitHubWorkflowRunID, claimRequest.WorkflowRunID,
					"queue_job_uuid", result.Key,
					"journal_generation", result.Generation,
					"changed", result.Changed)
			}
		}
		logger.InfoContext(ctx, "job correlation accepted",
			telemetryattrs.InstanceName, claimRequest.InstanceName,
			telemetryattrs.RunnerName, claimRequest.RunnerName,
			telemetryattrs.GitHubRepository, claimRequest.Repository,
			telemetryattrs.GitHubRepositoryID, claimRequest.RepositoryID,
			telemetryattrs.GitHubWorkflowRunID, claimRequest.WorkflowRunID,
			telemetryattrs.GitHubRunAttempt, claimRequest.RunAttempt,
			telemetryattrs.GitHubJobName, claimRequest.JobName,
			telemetryattrs.GitHubWorkflowRef, claimRequest.WorkflowRef,
			telemetryattrs.GitHubCommitSHA, claimRequest.CommitSHA,
		)
	}
	repositoryConfig, identity, exists := h.Config.Delivery(claimRequest.Repository, claim.Role)
	if !exists {
		if _, err := h.Store.Consume(ctx, claimRequest.InstanceName, token, claimRequest.Repository); err != nil {
			deny(logger, writer, request, http.StatusConflict, "claim already bound")
			return
		}
		logger.InfoContext(ctx, "cache claim bound without optional delivery", "instance", claim.InstanceName, "repository", claimRequest.Repository, "role", claim.Role)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	delivery, err := loadDelivery(h.Config, repositoryConfig.Bucket, identity, claimRequest.InstanceName)
	if err != nil {
		logger.ErrorContext(ctx, "cache broker delivery unavailable", "instance", claim.InstanceName, "repository", claimRequest.Repository, "error", err)
		http.Error(writer, "cache delivery unavailable", http.StatusServiceUnavailable)
		return
	}
	defer clearDelivery(&delivery)
	if _, err := h.Store.Consume(ctx, claimRequest.InstanceName, token, claimRequest.Repository); err != nil {
		deny(logger, writer, request, http.StatusConflict, "claim already consumed")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(delivery); err != nil {
		logger.ErrorContext(ctx, "encode cache delivery", "error", err)
		return
	}
	logger.InfoContext(ctx, "cache claim delivered", "repository", repositoryConfig.Name,
		"role", identity.Role, "mode", identity.Mode,
		"delivery_id", delivery.DeliveryID)
}

func validateJobCorrelation(request ClaimRequest) error {
	present := request.RepositoryID != 0 || request.WorkflowRunID != 0 || request.RunAttempt != 0 ||
		request.JobName != "" || request.WorkflowRef != "" || request.CommitSHA != ""
	if !present {
		return nil
	}
	if request.RepositoryID <= 0 || request.WorkflowRunID <= 0 || request.RunAttempt <= 0 ||
		!boundedText(request.JobName) || !boundedText(request.WorkflowRef) ||
		len(request.CommitSHA) != 40 {
		return errors.New("job correlation is incomplete")
	}
	for _, character := range request.CommitSHA {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return errors.New("commit sha is invalid")
		}
	}
	return nil
}

func boundedText(value string) bool {
	return value != "" && len(value) <= 1024 && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func loadDelivery(config Config, bucket string, identity Identity, instance string) (Delivery, error) {
	access, err := readPrivateRegular(identity.AccessKeyFile)
	if err != nil {
		return Delivery{}, fmt.Errorf("read cache access key: %w", err)
	}
	secret, err := readPrivateRegular(identity.SecretKeyFile)
	if err != nil {
		clear(access)
		return Delivery{}, fmt.Errorf("read cache secret: %w", err)
	}
	ca, err := os.ReadFile(config.CAFile)
	if err != nil {
		clear(access)
		clear(secret)
		return Delivery{}, fmt.Errorf("read cache CA: %w", err)
	}
	accessText := strings.TrimSpace(string(access))
	secret = bytes.TrimSpace(secret)
	clear(access)
	if len(accessText) != 20 || !strings.HasPrefix(accessText, "AKIA") || len(secret) != 64 {
		clear(secret)
		return Delivery{}, errors.New("cache credential shape is invalid")
	}
	digest := sha256.Sum256([]byte("nddev-job-cache-delivery-v1\x00" + instance + "\x00" + identity.Prefix))
	delivery := Delivery{SchemaVersion: 1, DeliveryID: hex.EncodeToString(digest[:]), Role: identity.Role, Mode: identity.Mode,
		Endpoint: config.Endpoint, Region: config.Region, Bucket: bucket, PrefixRoot: identity.Prefix,
		AccessKey: accessText, SecretKeyB64: base64.StdEncoding.EncodeToString(secret), CAPEMB64: base64.StdEncoding.EncodeToString(ca)}
	clear(secret)
	return delivery, nil
}

func readPrivateRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o027 != 0 {
		return nil, errors.New("credential must be a non-world-readable regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximumCredentialBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > maximumCredentialBytes {
		clear(raw)
		return nil, errors.New("credential size is invalid")
	}
	return raw, nil
}

func deny(logger *slog.Logger, writer http.ResponseWriter, request *http.Request, status int, reason string) {
	logger.WarnContext(request.Context(), "cache claim denied", "status", status, "reason", reason, "remote_address", request.RemoteAddr)
	http.Error(writer, http.StatusText(status), status)
}
func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
func clearDelivery(delivery *Delivery) {
	if delivery == nil {
		return
	}
	delivery.AccessKey = ""
	delivery.SecretKeyB64 = ""
	delivery.CAPEMB64 = ""
}
