package zotcredentials

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/crypto/bcrypt"
)

const (
	SchemaVersion       = 1
	passwordBytes       = 48
	bcryptCost          = 12
	maxHTPasswdBytes    = 64 * 1024
	managedState        = "managed"
	bootstrapState      = "bootstrap"
	freshState          = "fresh"
	DefaultSecretsDir   = "/etc/gha-fleet/secrets"
	DefaultHTPasswdPath = "/etc/gha-fleet/secrets/zot.htpasswd"
)

var (
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)
	passwordPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{64}$`)
	bcryptPattern   = regexp.MustCompile(`^\$2[aby]\$12\$[./A-Za-z0-9]{53}$`)
)

type Identity struct {
	Role         string `json:"role"`
	Username     string `json:"username"`
	UsernameFile string `json:"username_file"`
	PasswordFile string `json:"password_file"`
}

var managedIdentities = []Identity{
	{
		Role:         "promoter",
		Username:     "gha-zot-example-user-github-actions-promoter",
		UsernameFile: "zot-github-actions-promoter-username",
		PasswordFile: "zot-github-actions-promoter-password",
	},
	{
		Role:         "release-reader",
		Username:     "gha-zot-example-user-github-actions-release",
		UsernameFile: "zot-github-actions-release-username",
		PasswordFile: "zot-github-actions-release-password",
	},
	{
		Role:         "trusted-writer",
		Username:     "gha-zot-example-user-github-actions-trusted",
		UsernameFile: "zot-github-actions-trusted-username",
		PasswordFile: "zot-github-actions-trusted-password",
	},
	{
		Role:         "untrusted-writer",
		Username:     "gha-zot-example-user-github-actions-untrusted",
		UsernameFile: "zot-github-actions-untrusted-username",
		PasswordFile: "zot-github-actions-untrusted-password",
	},
	// The BuildKit layer cache carries the same poisoning boundary as the
	// object cache, so its writers are separate identities in separate
	// registry namespaces (buildcache/<owner>/<repo>/<class>).
	{
		Role:         "buildcache-trusted-writer",
		Username:     "gha-zot-example-user-github-actions-buildcache-trusted",
		UsernameFile: "zot-github-actions-buildcache-trusted-username",
		PasswordFile: "zot-github-actions-buildcache-trusted-password",
	},
	{
		Role:         "buildcache-untrusted-writer",
		Username:     "gha-zot-example-user-github-actions-buildcache-untrusted",
		UsernameFile: "zot-github-actions-buildcache-untrusted-username",
		PasswordFile: "zot-github-actions-buildcache-untrusted-password",
	},
}

func Identities() []Identity {
	return append([]Identity(nil), managedIdentities...)
}

var bootstrapUsernames = []string{"gha-buildkit", "gha-cache-reader"}

type Options struct {
	SecretsDirectory string
	HTPasswdPath     string
	Apply            bool
	OwnerUID         int
	OwnerGID         int
}

type Result struct {
	SchemaVersion int        `json:"schema_version"`
	Applied       bool       `json:"applied"`
	StateBefore   string     `json:"state_before"`
	StateAfter    string     `json:"state_after"`
	Actions       []string   `json:"actions"`
	Identities    []Identity `json:"identities"`
}

type Runner struct {
	Random       io.Reader
	HashPassword func([]byte) ([]byte, error)
	CompareHash  func([]byte, []byte) error
}

type credential struct {
	identity Identity
	password []byte
	hash     []byte
}

func (r Runner) Run(options Options) (Result, error) {
	if err := validateOptions(options); err != nil {
		return Result{}, err
	}
	stateBefore, err := r.inspect(options)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		SchemaVersion: SchemaVersion,
		StateBefore:   stateBefore,
		StateAfter:    stateBefore,
		Identities:    Identities(),
	}
	if stateBefore == managedState {
		return result, nil
	}
	result.Actions = []string{"write_repository_scoped_credentials", "replace_zot_htpasswd_last"}
	if !options.Apply {
		return result, nil
	}

	credentials, err := r.generateCredentials()
	if err != nil {
		return Result{}, err
	}
	defer clearCredentials(credentials)
	for _, current := range credentials {
		if err := atomicWrite(
			filepath.Join(options.SecretsDirectory, current.identity.UsernameFile),
			append([]byte(current.identity.Username), '\n'), options.OwnerUID, options.OwnerGID,
		); err != nil {
			return Result{}, fmt.Errorf("write %s username: %w", current.identity.Role, err)
		}
		if err := atomicWrite(
			filepath.Join(options.SecretsDirectory, current.identity.PasswordFile),
			append(append([]byte(nil), current.password...), '\n'), options.OwnerUID, options.OwnerGID,
		); err != nil {
			return Result{}, fmt.Errorf("write %s password: %w", current.identity.Role, err)
		}
	}
	var htpasswd strings.Builder
	for _, current := range credentials {
		htpasswd.WriteString(current.identity.Username)
		htpasswd.WriteByte(':')
		htpasswd.Write(current.hash)
		htpasswd.WriteByte('\n')
	}
	if err := atomicWrite(options.HTPasswdPath, []byte(htpasswd.String()), options.OwnerUID, options.OwnerGID); err != nil {
		return Result{}, fmt.Errorf("replace Zot htpasswd: %w", err)
	}

	stateAfter, err := r.inspect(options)
	if err != nil {
		return Result{}, fmt.Errorf("verify applied Zot credentials: %w", err)
	}
	if stateAfter != managedState {
		return Result{}, fmt.Errorf("applied Zot credentials did not converge")
	}
	result.Applied = true
	result.StateAfter = stateAfter
	return result, nil
}

func (r Runner) generateCredentials() ([]credential, error) {
	random := r.Random
	if random == nil {
		random = rand.Reader
	}
	hashPassword := r.HashPassword
	if hashPassword == nil {
		hashPassword = bcryptHashPassword
	}
	credentials := make([]credential, 0, len(managedIdentities))
	for _, identity := range managedIdentities {
		randomBytes := make([]byte, passwordBytes)
		if _, err := io.ReadFull(random, randomBytes); err != nil {
			clear(randomBytes)
			clearCredentials(credentials)
			return nil, fmt.Errorf("generate %s password: %w", identity.Role, err)
		}
		password := []byte(base64.RawURLEncoding.EncodeToString(randomBytes))
		clear(randomBytes)
		for _, previous := range credentials {
			if bytes.Equal(previous.password, password) {
				clear(password)
				clearCredentials(credentials)
				return nil, fmt.Errorf("generated duplicate Zot credential")
			}
		}
		hash, err := hashPassword(password)
		if err != nil {
			clear(password)
			clearCredentials(credentials)
			return nil, fmt.Errorf("hash %s password: %w", identity.Role, err)
		}
		if !bcryptPattern.Match(hash) {
			clear(password)
			clear(hash)
			clearCredentials(credentials)
			return nil, fmt.Errorf("hash %s password: bcrypt cost must be 12", identity.Role)
		}
		credentials = append(credentials, credential{identity: identity, password: password, hash: hash})
	}
	return credentials, nil
}

func validateOptions(options Options) error {
	if !filepath.IsAbs(options.SecretsDirectory) || filepath.Clean(options.SecretsDirectory) != options.SecretsDirectory || options.SecretsDirectory == "/" {
		return fmt.Errorf("secrets directory must be an absolute clean non-root path")
	}
	if options.HTPasswdPath != filepath.Join(options.SecretsDirectory, "zot.htpasswd") {
		return fmt.Errorf("htpasswd path must be zot.htpasswd inside the secrets directory")
	}
	if options.OwnerUID < 0 || options.OwnerGID < 0 {
		return fmt.Errorf("credential owner IDs must be non-negative")
	}
	info, err := os.Lstat(options.SecretsDirectory)
	if err != nil {
		return fmt.Errorf("inspect secrets directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("secrets directory must be a real mode-0700 directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != options.OwnerUID || int(stat.Gid) != options.OwnerGID {
		return fmt.Errorf("secrets directory owner does not match the requested credential owner")
	}
	return nil
}

func (r Runner) inspect(options Options) (string, error) {
	users, exists, err := loadHTPasswd(options.HTPasswdPath, options.OwnerUID, options.OwnerGID)
	if err != nil {
		return "", err
	}
	if !exists {
		return freshState, nil
	}
	usernames := sortedKeys(users)
	if equalStrings(usernames, bootstrapUsernames) {
		return bootstrapState, nil
	}
	wanted := make([]string, 0, len(managedIdentities))
	for _, identity := range managedIdentities {
		wanted = append(wanted, identity.Username)
	}
	sort.Strings(wanted)
	if !equalStrings(usernames, wanted) {
		return "", fmt.Errorf("zot htpasswd contains unmanaged or incomplete identities")
	}
	compareHash := r.CompareHash
	if compareHash == nil {
		compareHash = bcrypt.CompareHashAndPassword
	}
	for _, identity := range managedIdentities {
		username, err := readCredentialLine(filepath.Join(options.SecretsDirectory, identity.UsernameFile), options.OwnerUID, options.OwnerGID)
		if err != nil {
			return "", fmt.Errorf("read %s username: %w", identity.Role, err)
		}
		password, err := readCredentialLine(filepath.Join(options.SecretsDirectory, identity.PasswordFile), options.OwnerUID, options.OwnerGID)
		if err != nil {
			clear(username)
			return "", fmt.Errorf("read %s password: %w", identity.Role, err)
		}
		validContent := string(username) == identity.Username && passwordPattern.Match(password)
		clear(username)
		if !validContent {
			clear(password)
			return "", fmt.Errorf("%s credential files have invalid content", identity.Role)
		}
		err = compareHash(users[identity.Username], password)
		clear(password)
		if err != nil {
			return "", fmt.Errorf("%s password does not match Zot htpasswd", identity.Role)
		}
	}
	return managedState, nil
}

func bcryptHashPassword(password []byte) ([]byte, error) {
	return bcrypt.GenerateFromPassword(password, bcryptCost)
}

func loadHTPasswd(path string, ownerUID, ownerGID int) (map[string][]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect Zot htpasswd: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > maxHTPasswdBytes {
		return nil, false, fmt.Errorf("zot htpasswd must be a regular mode-0600 file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != ownerUID || int(stat.Gid) != ownerGID {
		return nil, false, fmt.Errorf("zot htpasswd owner does not match the requested credential owner")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("open Zot htpasswd: %w", err)
	}
	defer file.Close()
	limited := io.LimitReader(file, maxHTPasswdBytes+1)
	scanner := bufio.NewScanner(limited)
	users := make(map[string][]byte)
	for scanner.Scan() {
		line := scanner.Text()
		username, hash, ok := strings.Cut(line, ":")
		if !ok || !usernamePattern.MatchString(username) || !bcryptPattern.MatchString(hash) {
			return nil, false, fmt.Errorf("zot htpasswd contains an invalid entry")
		}
		if _, duplicate := users[username]; duplicate {
			return nil, false, fmt.Errorf("zot htpasswd contains duplicate username %q", username)
		}
		users[username] = []byte(hash)
	}
	if err := scanner.Err(); err != nil {
		return nil, false, fmt.Errorf("read Zot htpasswd: %w", err)
	}
	if len(users) == 0 {
		return nil, false, fmt.Errorf("zot htpasswd is empty")
	}
	return users, true, nil
}

func readCredentialLine(path string, ownerUID, ownerGID int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 512 {
		return nil, fmt.Errorf("credential must be a regular bounded mode-0600 file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != ownerUID || int(stat.Gid) != ownerGID {
		return nil, fmt.Errorf("credential owner mismatch")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer clear(data)
	line := bytes.TrimSuffix(data, []byte{'\n'})
	if len(line) == 0 || bytes.ContainsRune(line, '\n') || bytes.ContainsRune(line, '\r') {
		return nil, fmt.Errorf("credential must contain exactly one non-empty line")
	}
	return append([]byte(nil), line...), nil
}

func atomicWrite(path string, data []byte, ownerUID, ownerGID int) (err error) {
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular file")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".zot-credential-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		temporary.Close()
		os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if err := temporary.Chown(ownerUID, ownerGID); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}

func clearCredentials(credentials []credential) {
	for index := range credentials {
		clear(credentials[index].password)
		clear(credentials[index].hash)
	}
}

func sortedKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
