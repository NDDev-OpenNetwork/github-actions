package provider

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"time"

	incus "github.com/lxc/incus/v7/client"
)

const (
	// Unprivileged GitHub jobs run as uid runner. The Incus guest API that
	// exposes user.nddev.image-fingerprint answers HTTP 401 to every non-root
	// caller, so a fail-closed guest-config read cannot attest the image.
	// The root assignment writes this world-readable file instead. /run is
	// tmpfs; the parent directory is created here so current images do not
	// wait on a bake.
	imageIdentityDirectory = "/run/nddev"
	imageIdentityPath      = "/run/nddev/image-fingerprint"
)

var imageFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (l *Incus) publishJobImageIdentity(ctx context.Context, cli InstanceServerInterface, instanceName, flavor string) error {
	imagePolicy, err := l.workerImagePolicy(flavor)
	if err != nil {
		return err
	}
	return l.injectImageIdentity(ctx, cli, instanceName, imagePolicy.Fingerprint)
}

func (l *Incus) injectImageIdentity(ctx context.Context, cli InstanceServerInterface, instanceName, fingerprint string) error {
	if !imageFingerprintPattern.MatchString(fingerprint) {
		return fmt.Errorf("inject image identity: fingerprint %q is not a 64-hex digest", fingerprint)
	}
	body := []byte(fingerprint + "\n")
	deadline := time.Now().Add(cacheInjectionTimeout)
	for {
		err := l.writeImageIdentity(cli, instanceName, body)
		if err == nil {
			return nil
		}
		instance, _, inspectErr := cli.GetInstanceFull(instanceName)
		if inspectErr == nil && instance.State != nil && instance.State.Status != "Running" {
			return fmt.Errorf("inject image identity: instance stopped: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("inject image identity: guest agent did not accept %s within %s: %w", imageIdentityPath, cacheInjectionTimeout, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("inject image identity: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func (l *Incus) writeImageIdentity(cli InstanceServerInterface, instanceName string, body []byte) error {
	// The directory is world-traversable so uid runner can open the file.
	// An already-present directory is not a failure: the next bake will
	// create it from tmpfiles, and a retry of this write meets it again.
	_ = cli.CreateInstanceFile(instanceName, imageIdentityDirectory, incus.InstanceFileArgs{
		Content: bytes.NewReader(nil), UID: 0, GID: 0, Mode: 0o755, Type: "directory", WriteMode: "overwrite",
	})
	return cli.CreateInstanceFile(instanceName, imageIdentityPath, incus.InstanceFileArgs{
		Content: bytes.NewReader(body), UID: 0, GID: 0, Mode: 0o644, Type: "file", WriteMode: "overwrite",
	})
}
