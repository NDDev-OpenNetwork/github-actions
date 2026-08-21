package queueadmission

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

func TestValidateAgainstPortablePlatform(t *testing.T) {
	platform, err := config.Load("../../config/example-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	queue, err := Load("../../config/example-queue-admission.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.ValidateAgainstPlatform(platform); err != nil {
		t.Fatal(err)
	}
	queue.ScaleSets["nddev-linux-standard"] = ScaleSetResources{CPUUnits: 1, MemoryMiB: 1, Priority: 1}
	if err := queue.ValidateAgainstPlatform(platform); err == nil {
		t.Fatal("resource drift was accepted")
	}
	queue, err = Load("../../config/example-queue-admission.json")
	if err != nil {
		t.Fatal(err)
	}
	queue.ScaleSets["nddev-linux-standard"] = ScaleSetResources{CPUUnits: 2, MemoryMiB: 4096, Priority: 3}
	if err := queue.Validate(); err == nil {
		t.Fatal("a fourth priority level was accepted")
	}
}
