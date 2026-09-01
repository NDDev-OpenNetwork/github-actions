package provider

import (
	"errors"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
)

// The three failure shapes the 2026-09-01 journals taught, pinned so they
// stay deferrals and consumptions rather than failed reconciles.
func TestWarmCreateFailureClassification(t *testing.T) {
	if !warmInstanceRetiredDuringCreate(errors.New(
		`attempt count exceeded: fetching instance: fetching instance: "Instance not found": not found`,
	)) {
		t.Fatal("a wrapped mid-wait deletion was not read as consumed")
	}
	if warmInstanceRetiredDuringCreate(errors.New("connection refused")) {
		t.Fatal("an unrelated failure was read as consumed")
	}
	if !isPlacementRefusal(errors.New(
		"launching warm instance: creating instance: Failed instance placement scriptlet: Failed to run: fail: insufficient-memory: no fleet member has room for this worker",
	)) {
		t.Fatal("a scriptlet capacity refusal was not read as a deferral")
	}
	if isPlacementRefusal(errors.New("image not found")) {
		t.Fatal("an unrelated create failure was read as a placement refusal")
	}
	if admission.ReasonPlacementRefused == "" {
		t.Fatal("placement refusal reason must be named")
	}
}
