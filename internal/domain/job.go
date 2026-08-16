package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// WebhookJobIdentity is the stable identity available from workflow_job
// webhooks. RunAttempt is explicit because reruns need a fresh disposable VM.
type WebhookJobIdentity struct {
	RepositoryID  int64 `json:"repository_id"`
	WorkflowJobID int64 `json:"workflow_job_id"`
	RunAttempt    int64 `json:"run_attempt"`
}

type JobKey string

func (identity WebhookJobIdentity) Validate() error {
	switch {
	case identity.RepositoryID <= 0:
		return fmt.Errorf("repository ID must be positive")
	case identity.WorkflowJobID <= 0:
		return fmt.Errorf("workflow job ID must be positive")
	case identity.RunAttempt <= 0:
		return fmt.Errorf("run attempt must be positive")
	default:
		return nil
	}
}

func (identity WebhookJobIdentity) Key() (JobKey, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	canonical := fmt.Sprintf("github-webhook-job:v1:%d:%d:%d", identity.RepositoryID, identity.WorkflowJobID, identity.RunAttempt)
	return keyFromCanonical(canonical), nil
}

// ScaleSetJobIdentity is the identity exposed by the GitHub runner scale-set
// message queue. JobID is the opaque identity present from JobAssigned through
// JobCompleted; runnerRequestId appears only later on JobAvailable.
type ScaleSetJobIdentity struct {
	ScaleSetID int64  `json:"scale_set_id"`
	JobID      string `json:"job_id"`
}

func (identity ScaleSetJobIdentity) Validate() error {
	switch {
	case identity.ScaleSetID <= 0:
		return fmt.Errorf("scale set ID must be positive")
	case identity.JobID == "":
		return fmt.Errorf("job ID must not be empty")
	default:
		return nil
	}
}

func (identity ScaleSetJobIdentity) Key() (JobKey, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	canonical := fmt.Sprintf("github-scale-set-job:v2:%d:%s", identity.ScaleSetID, identity.JobID)
	return keyFromCanonical(canonical), nil
}

func keyFromCanonical(canonical string) JobKey {
	digest := sha256.Sum256([]byte(canonical))
	return JobKey("ghjob:v1:" + hex.EncodeToString(digest[:]))
}

func (key JobKey) String() string {
	return string(key)
}

func (key JobKey) IsZero() bool {
	return key == ""
}
