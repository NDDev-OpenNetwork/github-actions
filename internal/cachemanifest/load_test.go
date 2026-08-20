package cachemanifest

import (
	"strings"
	"testing"
)

func TestPublicExampleManifestIsValidAndFingerprintable(t *testing.T) {
	manifest, err := Load("../../config/cache-artifacts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := manifest.Fingerprint()
	if err != nil || !strings.HasPrefix(fingerprint, "sha256:") || len(fingerprint) != len("sha256:")+64 {
		t.Fatalf("fingerprint=%q err=%v", fingerprint, err)
	}
}
