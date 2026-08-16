//go:build linux

package hostdeps

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Every entry here was found the hard way, by a reconcile failing against the
// Incus API rather than against this check. Ubuntu splits what a full VM needs
// across packages that `--no-install-recommends` does not pull in, and each
// absence surfaces as a different late error: no SPICE module leaves the VM
// driver unusable, no UEFI firmware makes QEMU fail its feature checks so the
// server never advertises the driver at all, and no dnsmasq makes the managed
// bridge fail to create after the storage pool has already been built.
//
// Catching them here is the difference between one refusal naming the package
// and three separate half-provisioned states.
var incusVMHostPackages = []string{
	"dnsmasq-base",
	"ovmf",
	"qemu-system-modules-spice",
}

// VerifyIncusVM proves that the distro-split QEMU modules required by Incus
// full VMs are installed before an operation reaches the Incus API.
func VerifyIncusVM(ctx context.Context) (map[string]string, error) {
	args := []string{"-W", "-f=${binary:Package}\t${db:Status-Abbrev}\t${Version}\n"}
	args = append(args, incusVMHostPackages...)
	command := exec.CommandContext(ctx, "dpkg-query", args...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("query required Incus VM host packages: %w: %s", err, message)
	}
	versions, err := parsePackageReport(stdout.Bytes())
	if err != nil {
		return nil, err
	}
	for _, name := range incusVMHostPackages {
		if versions[name] == "" {
			return nil, fmt.Errorf("required Incus VM host package %q is not installed", name)
		}
	}
	return versions, nil
}
