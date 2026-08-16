package imagebuild

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/imageplan"
)

type Artifacts struct {
	Directory     string `json:"directory"`
	Checksums     string `json:"checksums"`
	Signature     string `json:"signature"`
	Metadata      string `json:"metadata"`
	Disk          string `json:"disk"`
	Runner        string `json:"runner"`
	CompilerCache string `json:"compiler_cache"`
	// Toolchains maps each pinned toolchain name to its verified local archive.
	Toolchains map[string]string `json:"toolchains"`
	VerifiedBy string            `json:"verified_by"`
}

func (a Artifacts) Cleanup() error {
	base := filepath.Base(a.Directory)
	if a.Directory == "" || filepath.Dir(a.Directory) != "/var/tmp" || !strings.HasPrefix(base, "gha-fleet-image-") {
		return fmt.Errorf("refusing to clean unexpected artifact directory %q", a.Directory)
	}
	return os.RemoveAll(a.Directory)
}

func FetchArtifacts(ctx context.Context, plan imageplan.Plan) (Artifacts, error) {
	keyInfo, err := os.Lstat(plan.Source.KeyringPath)
	if err != nil {
		return Artifacts{}, fmt.Errorf("inspect Ubuntu image keyring: %w", err)
	}
	if !keyInfo.Mode().IsRegular() {
		return Artifacts{}, fmt.Errorf("ubuntu image keyring %q is not a regular file", plan.Source.KeyringPath)
	}
	directory, err := os.MkdirTemp("/var/tmp", "gha-fleet-image-")
	if err != nil {
		return Artifacts{}, fmt.Errorf("create artifact directory: %w", err)
	}
	artifacts := Artifacts{
		Directory:     directory,
		Checksums:     filepath.Join(directory, plan.Source.ChecksumsFile),
		Signature:     filepath.Join(directory, plan.Source.SignatureFile),
		Metadata:      filepath.Join(directory, plan.Source.MetadataFile),
		Disk:          filepath.Join(directory, plan.Source.DiskFile),
		Runner:        filepath.Join(directory, plan.Runner.Archive),
		CompilerCache: filepath.Join(directory, plan.CompilerCache.Archive),
		Toolchains:    make(map[string]string, len(plan.Toolchains)),
	}
	for _, toolchain := range plan.Toolchains {
		artifacts.Toolchains[toolchain.Name] = filepath.Join(directory, toolchain.Archive)
	}
	failed := true
	defer func() {
		if failed {
			_ = artifacts.Cleanup()
		}
	}()

	client := downloadClient()
	if _, err := download(ctx, client, joinURL(plan.Source.BaseURL, plan.Source.ChecksumsFile), artifacts.Checksums, "", 1<<20); err != nil {
		return Artifacts{}, fmt.Errorf("download signed checksums: %w", err)
	}
	if _, err := download(ctx, client, joinURL(plan.Source.BaseURL, plan.Source.SignatureFile), artifacts.Signature, "", 1<<20); err != nil {
		return Artifacts{}, fmt.Errorf("download checksum signature: %w", err)
	}
	if err := verifySignature(ctx, plan.Source.KeyringPath, artifacts.Signature, artifacts.Checksums, plan.Source.SignerFingerprint); err != nil {
		return Artifacts{}, err
	}
	checksums, err := parseChecksums(artifacts.Checksums)
	if err != nil {
		return Artifacts{}, err
	}
	for name, pinned := range map[string]string{
		plan.Source.MetadataFile: plan.Source.MetadataSHA256,
		plan.Source.DiskFile:     plan.Source.DiskSHA256,
	} {
		if signed := checksums[name]; signed != pinned {
			return Artifacts{}, fmt.Errorf("signed checksum for %s is %q, pinned %q", name, signed, pinned)
		}
	}
	if _, err := download(ctx, client, joinURL(plan.Source.BaseURL, plan.Source.MetadataFile), artifacts.Metadata, plan.Source.MetadataSHA256, 32<<20); err != nil {
		return Artifacts{}, fmt.Errorf("download source metadata: %w", err)
	}
	if _, err := download(ctx, client, joinURL(plan.Source.BaseURL, plan.Source.DiskFile), artifacts.Disk, plan.Source.DiskSHA256, 2<<30); err != nil {
		return Artifacts{}, fmt.Errorf("download source disk: %w", err)
	}
	if _, err := download(ctx, client, plan.Runner.DownloadURL, artifacts.Runner, plan.Runner.SHA256, 512<<20); err != nil {
		return Artifacts{}, fmt.Errorf("download actions runner: %w", err)
	}
	if _, err := download(ctx, client, plan.CompilerCache.DownloadURL, artifacts.CompilerCache, plan.CompilerCache.ArchiveSHA256, 64<<20); err != nil {
		return Artifacts{}, fmt.Errorf("download compiler cache: %w", err)
	}
	for _, toolchain := range plan.Toolchains {
		if _, err := download(ctx, client, toolchain.DownloadURL, artifacts.Toolchains[toolchain.Name], toolchain.ArchiveSHA256, 768<<20); err != nil {
			return Artifacts{}, fmt.Errorf("download %s toolchain: %w", toolchain.Name, err)
		}
	}
	artifacts.VerifiedBy = plan.Source.SignerFingerprint
	failed = false
	return artifacts, nil
}

func downloadClient() *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if request.URL.Scheme != "https" || !allowedDownloadHost(request.URL.Hostname()) {
				return fmt.Errorf("redirect to untrusted URL %s", request.URL.Redacted())
			}
			return nil
		},
	}
}

func allowedDownloadHost(host string) bool {
	switch host {
	// go.dev redirects release archives to dl.google.com, and static.rust-lang.org
	// is Rust's own release host. Both are reached only for a manifest-pinned
	// archive whose SHA-256 is verified before it is used.
	case "cloud-images.ubuntu.com", "github.com", "objects.githubusercontent.com",
		"go.dev", "dl.google.com", "static.rust-lang.org":
		return true
	}
	return strings.HasSuffix(host, ".githubusercontent.com")
}

func joinURL(base, name string) string {
	return strings.TrimSuffix(base, "/") + "/" + url.PathEscape(name)
}

func download(ctx context.Context, client *http.Client, rawURL, destination, expectedSHA string, maxBytes int64) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned %s", request.URL.Redacted(), response.Status)
	}
	if response.ContentLength > maxBytes {
		return "", fmt.Errorf("artifact Content-Length %d exceeds %d bytes", response.ContentLength, maxBytes)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written > maxBytes {
		return "", fmt.Errorf("artifact exceeds %d bytes", maxBytes)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if expectedSHA != "" && actual != expectedSHA {
		return "", fmt.Errorf("SHA-256 %s does not match pinned %s", actual, expectedSHA)
	}
	return actual, nil
}

func verifySignature(ctx context.Context, keyring, signature, checksums, expectedFingerprint string) error {
	command := exec.CommandContext(ctx, "gpgv", "--status-fd=1", "--keyring", keyring, signature, checksums)
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("verify Canonical checksum signature: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	valid := false
	for _, line := range strings.Split(stdout.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "[GNUPG:]" && fields[1] == "VALIDSIG" && fields[2] == expectedFingerprint {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("valid signature from pinned fingerprint %s was not present", expectedFingerprint)
	}
	return nil
}

func parseChecksums(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open signed checksums: %w", err)
	}
	defer file.Close()
	result := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		result[name] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read signed checksums: %w", err)
	}
	return result, nil
}
