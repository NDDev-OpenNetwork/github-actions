package stagetransaction

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"time"
)

var stageIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type Stage struct {
	ID      string       `json:"id"`
	Command string       `json:"command"`
	Args    []string     `json:"args,omitempty"`
	Dir     string       `json:"dir"`
	Env     []string     `json:"env,omitempty"`
	Expect  *Expectation `json:"expect,omitempty"`
}

type Expectation struct {
	ExitStatus int      `json:"exit_status"`
	Signal     string   `json:"signal,omitempty"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
	Absent     []string `json:"absent,omitempty"`
}

type Plan struct {
	EvidenceRoot string
	OutputLimit  int
	Stages       []Stage
	Cleanup      func() error
	// StageTimeout bounds one stage. A stage transaction publishes a receipt
	// that says what happened; a stage with no deadline can leave the
	// transaction with no receipt at all, which is the one outcome the design
	// has no way to describe.
	StageTimeout time.Duration
	// GracePeriod is how long a cancelled stage has to close its output before
	// its process group is killed. It exists because a stage can exit while a
	// grandchild it forked still holds stdout open, and waiting on that is
	// indistinguishable from a stage that has not finished.
	GracePeriod time.Duration
}

type Receipt struct {
	SchemaVersion  int    `json:"schema_version"`
	Sequence       int    `json:"sequence"`
	StageID        string `json:"stage_id"`
	Event          string `json:"event"`
	ArgvSHA256     string `json:"argv_sha256"`
	StartedUnixNS  int64  `json:"started_unix_ns,omitempty"`
	FinishedUnixNS int64  `json:"finished_unix_ns,omitempty"`
	ExitStatus     *int   `json:"exit_status,omitempty"`
	Signal         string `json:"signal,omitempty"`
	StdoutSHA256   string `json:"stdout_sha256,omitempty"`
	StderrSHA256   string `json:"stderr_sha256,omitempty"`
	Error          string `json:"error,omitempty"`
	RawError       string `json:"raw_error,omitempty"`
	VerifierOK     *bool  `json:"verifier_ok,omitempty"`
	VerifierError  string `json:"verifier_error,omitempty"`
}

func Run(ctx context.Context, plan Plan) (result error) {
	if err := validatePlan(plan); err != nil {
		return err
	}
	receiptPath := filepath.Join(plan.EvidenceRoot, "stages.jsonl")
	sequence := 0
	defer func() {
		if plan.Cleanup != nil {
			result = errors.Join(result, plan.Cleanup())
		}
	}()
	for _, stage := range plan.Stages {
		sequence++
		digest := argvDigest(stage)
		started := time.Now().UnixNano()
		if err := appendReceipt(receiptPath, Receipt{SchemaVersion: 1, Sequence: sequence, StageID: stage.ID, Event: "started", ArgvSHA256: digest, StartedUnixNS: started}); err != nil {
			return err
		}
		stdoutPath := filepath.Join(plan.EvidenceRoot, fmt.Sprintf("%03d-%s.stdout", sequence, stage.ID))
		stderrPath := filepath.Join(plan.EvidenceRoot, fmt.Sprintf("%03d-%s.stderr", sequence, stage.ID))
		stdout, err := os.OpenFile(stdoutPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("stage %s stdout: %w", stage.ID, err)
		}
		stderr, err := os.OpenFile(stderrPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return errors.Join(fmt.Errorf("stage %s stderr: %w", stage.ID, err), stdout.Close())
		}
		stdoutCapture := &boundedHashWriter{writer: stdout, limit: plan.OutputLimit}
		stderrCapture := &boundedHashWriter{writer: stderr, limit: plan.OutputLimit}
		stageCtx, cancelStage := context.WithTimeout(ctx, plan.StageTimeout)
		command := exec.CommandContext(stageCtx, stage.Command, stage.Args...)
		command.Dir, command.Env = stage.Dir, append([]string(nil), stage.Env...)
		command.Stdout, command.Stderr = stdoutCapture, stderrCapture
		// Its own process group, so a stage that forks can be signalled whole.
		// Without this the cancellation reaches the direct child only, and the
		// grandchildren it left behind keep running and keep the pipes open.
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error { return signalGroup(command, syscall.SIGTERM) }
		// Bounds the wait on output. Without it Wait blocks until every writer
		// closes, so one grandchild holding stdout suspends the transaction
		// indefinitely -- after the stage itself has already exited.
		command.WaitDelay = plan.GracePeriod
		runErr := command.Run()
		// Whatever survived the grace period is killed as a group. Go escalates
		// to the process, which is the group leader and not the group.
		if killErr := signalGroup(command, syscall.SIGKILL); killErr != nil {
			runErr = errors.Join(runErr, killErr)
		}
		cancelStage()
		closeErr := errors.Join(stdout.Sync(), stderr.Sync(), stdout.Close(), stderr.Close())
		exitStatus, signal := processStatus(runErr)
		finished := Receipt{SchemaVersion: 1, Sequence: sequence, StageID: stage.ID, Event: "finished", ArgvSHA256: digest, StartedUnixNS: started, FinishedUnixNS: time.Now().UnixNano(), ExitStatus: &exitStatus, Signal: signal, StdoutSHA256: stdoutCapture.digest(), StderrSHA256: stderrCapture.digest()}
		rawErr := errors.Join(runErr, stdoutCapture.err, stderrCapture.err, closeErr)
		if rawErr != nil {
			finished.RawError = rawErr.Error()
		}
		verifierOK, verifierErr := verify(stage.Expect, exitStatus, signal, stdoutCapture.hash.Bytes(), stderrCapture.hash.Bytes())
		finished.VerifierOK = &verifierOK
		if verifierErr != nil {
			finished.VerifierError = verifierErr.Error()
		}
		stageErr := verifierErr
		if stage.Expect == nil {
			stageErr = rawErr
		}
		if stageErr != nil {
			finished.Error = stageErr.Error()
		}
		if err := appendReceipt(receiptPath, finished); err != nil {
			return errors.Join(stageErr, err)
		}
		if stageErr != nil {
			return fmt.Errorf("stage %s: %w", stage.ID, stageErr)
		}
	}
	return nil
}

func verify(expectation *Expectation, exitStatus int, signal string, stdout, stderr []byte) (bool, error) {
	if expectation == nil {
		return exitStatus == 0 && signal == "", nil
	}
	if exitStatus != expectation.ExitStatus || signal != expectation.Signal {
		return false, fmt.Errorf("raw outcome exit=%d signal=%q, want exit=%d signal=%q", exitStatus, signal, expectation.ExitStatus, expectation.Signal)
	}
	if string(stdout) != expectation.Stdout || string(stderr) != expectation.Stderr {
		return false, errors.New("raw streams differ from exact expected bytes")
	}
	for _, path := range expectation.Absent {
		if !filepath.IsAbs(path) {
			return false, errors.New("absence assertion path is not absolute")
		}
		if _, err := os.Lstat(path); err == nil {
			return false, fmt.Errorf("forbidden side effect exists at %q", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return true, nil
}

type boundedHashWriter struct {
	writer  io.Writer
	limit   int
	written int
	hash    bytes.Buffer
	err     error
}

func (w *boundedHashWriter) Write(value []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.written+len(value) > w.limit {
		w.err = errors.New("stage capture overflow")
		return 0, w.err
	}
	n, err := w.writer.Write(value)
	if err == nil {
		w.hash.Write(value[:n])
		w.written += n
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

func (w *boundedHashWriter) digest() string {
	sum := sha256.Sum256(w.hash.Bytes())
	return hex.EncodeToString(sum[:])
}

// signalGroup sends one signal to the whole process group a stage runs in.
// A group that has already exited is not an error: the reap is racing the
// child's own exit by design, and losing that race is the ordinary case.
func signalGroup(command *exec.Cmd, signal syscall.Signal) error {
	if command.Process == nil {
		return nil
	}
	err := syscall.Kill(-command.Process.Pid, signal)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("signal stage process group with %v: %w", signal, err)
}

func validatePlan(plan Plan) error {
	if !filepath.IsAbs(plan.EvidenceRoot) || filepath.Clean(plan.EvidenceRoot) != plan.EvidenceRoot || plan.OutputLimit <= 0 || len(plan.Stages) == 0 {
		return errors.New("stage transaction plan is incomplete")
	}
	// Both budgets are required rather than defaulted. A default deadline is a
	// deadline nobody chose, and the whole point of the transaction is that what
	// it did is stated rather than assumed.
	if plan.StageTimeout <= 0 {
		return errors.New("stage transaction plan states no stage timeout")
	}
	if plan.GracePeriod <= 0 {
		return errors.New("stage transaction plan states no grace period for a cancelled stage")
	}
	info, err := os.Lstat(plan.EvidenceRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.Join(errors.New("stage evidence root is not private directory"), err)
	}
	entries, err := os.ReadDir(plan.EvidenceRoot)
	if err != nil || len(entries) != 0 {
		return errors.Join(errors.New("stage evidence root is not empty"), err)
	}
	seen := map[string]bool{}
	for _, stage := range plan.Stages {
		if !stageIDPattern.MatchString(stage.ID) || seen[stage.ID] || !filepath.IsAbs(stage.Command) || !filepath.IsAbs(stage.Dir) {
			return errors.New("stage identity command or directory is invalid")
		}
		seen[stage.ID] = true
	}
	return nil
}

func argvDigest(stage Stage) string {
	env := append([]string(nil), stage.Env...)
	sort.Strings(env)
	raw, _ := json.Marshal(struct {
		Command, Dir string
		Args, Env    []string
	}{stage.Command, stage.Dir, stage.Args, env})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func appendReceipt(path string, receipt Receipt) (result error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func processStatus(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				return 128 + int(status.Signal()), status.Signal().String()
			}
			return status.ExitStatus(), ""
		}
	}
	return -1, ""
}
