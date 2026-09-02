// Package cachenamespace is the one place a compiler-cache object key is built.
//
// A prefix root -- organization/repository/trust/<class> -- was written as
// a literal in twenty-four places across five files: the identity manifest, the
// RustFS validator, the reconciler that also derives lifecycle-rule identifiers
// from it, the provider's delivery check, two jq expressions rendered into the
// guest, and the canary workflow. Changing the root meant finding all of them,
// and #236 is the consequence: every tenant shares one read-write namespace and
// widening it is a twenty-four-site edit rather than a decision.
//
// Repository identity is supplied by deployment configuration; this package
// owns only its public syntax and trust-boundary transformations.
package cachenamespace

import (
	"fmt"
	"strings"
)

// TrustClass is the isolation boundary an object belongs to. It is the only
// axis on which a worker's credential is scoped, so it is the only part of the
// root that varies.
type TrustClass string

const (
	// Trusted holds output from a job on a reviewed ref of a reviewed
	// repository. A fork or an unreviewed ref must never reach it.
	Trusted TrustClass = "trusted"
	// Untrusted holds output from a job whose inputs were not reviewed. It is
	// disposable and expires fastest.
	Untrusted TrustClass = "untrusted"
	// Promoted holds artifacts a promoter moved out of trusted after review.
	// Release jobs read it and nothing writes it from a worker.
	Promoted TrustClass = "promoted"
)

// PrefixRootFor builds a trust root for an explicitly configured repository.
// Repository identity is deployment data: public binaries must not compile a
// tenant into their authorization decisions.
func PrefixRootFor(repository string, class TrustClass) (string, error) {
	if !class.Valid() {
		return "", fmt.Errorf("unknown cache trust class %q", string(class))
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || !validSegment(parts[0]) || !validSegment(parts[1]) {
		return "", fmt.Errorf("cache repository %q must be organization/repository", repository)
	}
	return strings.Join([]string{parts[0], parts[1], trustSegment, string(class)}, "/"), nil
}

// BuildcacheRepositoryFor builds the registry repository that holds a
// project's BuildKit layer cache for one trust class. The layer cache carries
// the same poisoning argument as the object cache: an untrusted build must
// never write cache a trusted build reads, so the class is part of the name.
func BuildcacheRepositoryFor(repository string, class TrustClass) (string, error) {
	if !class.Valid() {
		return "", fmt.Errorf("unknown cache trust class %q", string(class))
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || !validSegment(parts[0]) || !validSegment(parts[1]) {
		return "", fmt.Errorf("cache repository %q must be organization/repository", repository)
	}
	// An OCI repository name is lowercase by specification and BuildKit
	// refuses anything else ("repository name must be lowercase"); GitHub
	// owners are not (NDDev-it-com), so the namespace lowercases the
	// repository while the object-store roots keep GitHub's spelling.
	return strings.Join([]string{"buildcache", strings.ToLower(parts[0]), strings.ToLower(parts[1]), string(class)}, "/"), nil
}

// ActionsCachePrefixFor is where a drop-in replacement for actions/cache
// (runs-on/cache) writes a repository's objects: cache/<owner>/<repo>/...,
// the layout that action hard-codes. It is granted to the trusted writer
// only -- an untrusted build keeps GitHub's own cache, so nothing it writes
// can be restored by a trusted one -- and it lives beside the trust roots,
// not under them, because the action cannot be told a prefix.
func ActionsCachePrefixFor(repository string) (string, error) {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || !validSegment(parts[0]) || !validSegment(parts[1]) {
		return "", fmt.Errorf("cache repository %q must be organization/repository", repository)
	}
	return strings.Join([]string{actionsCacheSegment, parts[0], parts[1]}, "/"), nil
}

// ActionsCacheIdentifier names the lifecycle rule for the actions cache
// prefix, the way Identifier names a trust class's.
const ActionsCacheIdentifier = "actions"

// ParsePrefixRoot returns the repository and trust class encoded by a complete
// credential root. It rejects additional path segments and ambiguous values.
func ParsePrefixRoot(prefix string) (string, TrustClass, error) {
	parts := strings.Split(prefix, "/")
	if len(parts) != 4 || !validSegment(parts[0]) || !validSegment(parts[1]) || parts[2] != trustSegment {
		return "", "", fmt.Errorf("cache prefix %q must be organization/repository/trust/class", prefix)
	}
	class := TrustClass(parts[3])
	if !class.Valid() {
		return "", "", fmt.Errorf("cache prefix %q has unknown trust class", prefix)
	}
	return parts[0] + "/" + parts[1], class, nil
}

func validSegment(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.ContainsAny(value, "/\\")
}

// TrustClasses is every class, in the order a reader would expect them.
func TrustClasses() []TrustClass { return []TrustClass{Trusted, Untrusted, Promoted} }

// Valid reports whether a class is one this package builds keys for. An unknown
// class must fail closed rather than produce a namespace nobody granted.
func (c TrustClass) Valid() bool {
	switch c {
	case Trusted, Untrusted, Promoted:
		return true
	}
	return false
}

const (
	// Organization and Repository form the public example configuration only.
	// Production authorization must use PrefixRootFor with its estate identity.
	Organization = "example-org"
	Repository   = "example-actions"

	// trustSegment separates the trust-scoped cache from anything else that
	// might live under the repository prefix. It is a literal path segment and
	// not a template token, which is what the declared namespace template used
	// to omit -- the template described seven segments and the implementation
	// wrote eight.
	trustSegment = "trust"
	// actionsCacheSegment is the top-level segment runs-on/cache writes under;
	// see ActionsCachePrefixFor.
	actionsCacheSegment = "cache"
)

// PrefixRoot is the credential-scoped root for a trust class. Every S3 policy,
// every lifecycle rule and every worker delivery is scoped to one of these.
func PrefixRoot(class TrustClass) (string, error) {
	return PrefixRootFor(Organization+"/"+Repository, class)
}

// MustPrefixRoot is PrefixRoot for the three constants above, where an unknown
// class is a programming error rather than input.
func MustPrefixRoot(class TrustClass) string {
	root, err := PrefixRoot(class)
	if err != nil {
		panic(err)
	}
	return root
}

// PrefixRoots is every class's root, keyed by class.
func PrefixRoots() map[TrustClass]string {
	roots := make(map[TrustClass]string, len(TrustClasses()))
	for _, class := range TrustClasses() {
		roots[class] = MustPrefixRoot(class)
	}
	return roots
}

// Object names the parts of a key below the root. Every one is required: a key
// missing its lock digest would collide across dependency sets, and a key
// missing its ref class would let a branch read a tag's entry.
type Object struct {
	Class      TrustClass
	Platform   string
	Arch       string
	Toolchain  string
	LockDigest string
	RefClass   string
	// Name is the object itself. It may contain slashes; everything above it
	// may not, because each is one path segment.
	Name string
}

// Key builds the full object key. It refuses rather than guesses: a component
// carrying a slash would silently create a segment the policy did not grant.
func (o Object) Key() (string, error) {
	root, err := PrefixRoot(o.Class)
	if err != nil {
		return "", err
	}
	segments := []struct {
		name  string
		value string
	}{
		{"platform", o.Platform},
		{"architecture", o.Arch},
		{"toolchain", o.Toolchain},
		{"lock_digest", o.LockDigest},
		{"ref_class", o.RefClass},
	}
	parts := []string{root}
	for _, segment := range segments {
		if segment.value == "" {
			return "", fmt.Errorf("cache key component %s is empty", segment.name)
		}
		if strings.ContainsAny(segment.value, "/\\") || segment.value == "." || segment.value == ".." {
			return "", fmt.Errorf("cache key component %s (%q) must be one path segment", segment.name, segment.value)
		}
		parts = append(parts, segment.value)
	}
	if o.Name == "" || strings.HasPrefix(o.Name, "/") || strings.Contains(o.Name, "..") {
		return "", fmt.Errorf("cache object name %q must be a relative in-namespace path", o.Name)
	}
	return strings.Join(parts, "/") + "/" + o.Name, nil
}

// Template is the namespace shape, as the host configuration declares it. The
// literal trust segment is written out because it is literal: a token would
// suggest something substitutes for it.
func Template() string {
	return strings.Join([]string{
		"{organization}", "{repository}", trustSegment, "{trust}",
		"{platform}", "{architecture}", "{toolchain}", "{lock_digest}", "{ref_class}",
	}, "/")
}

// Identifier is the lifecycle-rule id for a prefix: the part below the trust
// root, with separators flattened. The reconciler derived this by trimming the
// literal root, which meant the root was written down there too.
func Identifier(prefix string) (string, error) {
	_, class, err := ParsePrefixRoot(prefix)
	if err != nil {
		return "", err
	}
	return string(class), nil
}

// Counterpart is the class a credential for the given class must be denied. The
// reconciler proves isolation by attempting a write there and requiring refusal,
// and picking the counterpart by hand is how that proof could quietly become a
// test of a class the credential was never granted anyway.
func Counterpart(class TrustClass) (TrustClass, error) {
	switch class {
	case Untrusted:
		return Trusted, nil
	case Trusted, Promoted:
		return Untrusted, nil
	}
	return "", fmt.Errorf("unknown cache trust class %q", string(class))
}
