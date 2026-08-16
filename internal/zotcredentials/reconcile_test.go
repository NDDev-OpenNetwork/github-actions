package zotcredentials

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMigrateBootstrapCredentialsAndConvergeIdempotently(t *testing.T) {
	t.Parallel()

	directory, options := bootstrapFixture(t)
	randomData := make([]byte, passwordBytes*len(managedIdentities))
	for index := range randomData {
		randomData[index] = byte(index + 1)
	}
	runner := fastRunner(bytes.NewReader(randomData))

	options.Apply = false
	plan, err := runner.Run(options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applied || plan.StateBefore != bootstrapState || plan.StateAfter != bootstrapState ||
		!slices.Equal(plan.Actions, []string{"write_repository_scoped_credentials", "replace_zot_htpasswd_last"}) {
		t.Fatalf("unexpected migration plan: %#v", plan)
	}
	for _, identity := range managedIdentities {
		if _, err := os.Lstat(filepath.Join(directory, identity.PasswordFile)); !os.IsNotExist(err) {
			t.Fatalf("dry run created %s: %v", identity.PasswordFile, err)
		}
	}

	options.Apply = true
	result, err := runner.Run(options)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.StateBefore != bootstrapState || result.StateAfter != managedState {
		t.Fatalf("unexpected apply result: %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range managedIdentities {
		password, err := readCredentialLine(filepath.Join(directory, identity.PasswordFile), os.Getuid(), os.Getgid())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, password) {
			t.Fatalf("redacted result contains %s password", identity.Role)
		}
		clear(password)
	}

	options.Apply = false
	converged, err := runner.Run(options)
	if err != nil {
		t.Fatal(err)
	}
	if converged.Applied || converged.StateBefore != managedState || converged.StateAfter != managedState || len(converged.Actions) != 0 {
		t.Fatalf("managed credentials did not converge idempotently: %#v", converged)
	}
}

func TestFreshCredentialDirectoryCanBeProvisioned(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	options := testOptions(directory)
	randomData := make([]byte, passwordBytes*len(managedIdentities))
	for index := range randomData {
		randomData[index] = byte(index + 11)
	}
	result, err := fastRunner(bytes.NewReader(randomData)).Run(options)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.StateBefore != freshState || result.StateAfter != managedState {
		t.Fatalf("unexpected fresh result: %#v", result)
	}
}

func TestRejectsUnmanagedOrIncompleteCredentialState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		users []string
	}{
		{name: "unexpected", users: []string{"admin"}},
		{name: "partial managed", users: []string{managedIdentities[0].Username}},
		{name: "mixed bootstrap", users: []string{"gha-buildkit", managedIdentities[0].Username}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			writeHTPasswd(t, filepath.Join(directory, "zot.htpasswd"), test.users)
			_, err := (Runner{}).Run(testOptions(directory))
			if err == nil || !strings.Contains(err.Error(), "unmanaged or incomplete") {
				t.Fatalf("expected fail-closed state error, got %v", err)
			}
		})
	}
}

func TestDuplicateRandomCredentialFailsBeforeMutation(t *testing.T) {
	t.Parallel()

	_, options := bootstrapFixture(t)
	before, err := os.ReadFile(options.HTPasswdPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fastRunner(bytes.NewReader(make([]byte, passwordBytes*len(managedIdentities)))).Run(options)
	if err == nil || !strings.Contains(err.Error(), "duplicate Zot credential") {
		t.Fatalf("expected duplicate credential error, got %v", err)
	}
	after, err := os.ReadFile(options.HTPasswdPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("htpasswd changed after credential generation failure")
	}
}

func TestRefusesSymlinkCredentialTargetAndKeepsBootstrapActive(t *testing.T) {
	t.Parallel()

	directory, options := bootstrapFixture(t)
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := managedIdentities[0]
	if err := os.Symlink(target, filepath.Join(directory, first.UsernameFile)); err != nil {
		t.Fatal(err)
	}
	randomData := make([]byte, passwordBytes*len(managedIdentities))
	for index := range randomData {
		randomData[index] = byte(index + 31)
	}
	runner := fastRunner(bytes.NewReader(randomData))
	_, err := runner.Run(options)
	if err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
	state, err := runner.inspect(options)
	if err != nil || state != bootstrapState {
		t.Fatalf("bootstrap htpasswd was not retained: state=%q err=%v", state, err)
	}
	outside, err := os.ReadFile(target)
	if err != nil || string(outside) != "unchanged\n" {
		t.Fatalf("symlink target changed: %q, %v", outside, err)
	}
}

func TestOptionsRequireExactOwnedMode0700DirectoryAndLocalHTPasswd(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	options := testOptions(directory)
	if _, err := (Runner{}).Run(options); err == nil || !strings.Contains(err.Error(), "mode-0700") {
		t.Fatalf("expected directory mode failure, got %v", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	options.HTPasswdPath = filepath.Join(t.TempDir(), "zot.htpasswd")
	if _, err := (Runner{}).Run(options); err == nil || !strings.Contains(err.Error(), "inside the secrets directory") {
		t.Fatalf("expected htpasswd path failure, got %v", err)
	}
}

func TestIdentitiesReturnsAnIndependentCopy(t *testing.T) {
	t.Parallel()

	identities := Identities()
	identities[0].Username = "mutated"
	if Identities()[0].Username == "mutated" {
		t.Fatal("managed identities escaped through a mutable slice")
	}
}

func bootstrapFixture(t *testing.T) (string, Options) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeHTPasswd(t, filepath.Join(directory, "zot.htpasswd"), bootstrapUsernames)
	return directory, testOptions(directory)
}

func testOptions(directory string) Options {
	return Options{
		SecretsDirectory: directory,
		HTPasswdPath:     filepath.Join(directory, "zot.htpasswd"),
		Apply:            true,
		OwnerUID:         os.Getuid(),
		OwnerGID:         os.Getgid(),
	}
}

func writeHTPasswd(t *testing.T, path string, usernames []string) {
	t.Helper()
	var contents strings.Builder
	for _, username := range usernames {
		hash, err := fakeHashPassword([]byte("fixture-password-which-is-not-a-runtime-secret"))
		if err != nil {
			t.Fatal(err)
		}
		contents.WriteString(username)
		contents.WriteByte(':')
		contents.Write(hash)
		contents.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(contents.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fastRunner(random io.Reader) Runner {
	return Runner{Random: random, HashPassword: fakeHashPassword, CompareHash: fakeCompareHash}
}

func fakeHashPassword(password []byte) ([]byte, error) {
	if len(password) == 0 {
		return nil, errors.New("empty password")
	}
	alphabet := "./ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	marker := alphabet[int(password[0])%len(alphabet)]
	return []byte("$2b$12$" + strings.Repeat(string(marker), 53)), nil
}

func fakeCompareHash(hash, password []byte) error {
	expected, err := fakeHashPassword(password)
	if err != nil {
		return err
	}
	defer clear(expected)
	if !bytes.Equal(hash, expected) {
		return errors.New("hash mismatch")
	}
	return nil
}
