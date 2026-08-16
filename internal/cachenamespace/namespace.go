// Package cachenamespace is the one place a compiler-cache object key is built.
//
// The prefix root -- NDDev-OpenNetwork/github-actions/trust/<class> -- was written as
// a literal in twenty-four places across five files: the identity manifest, the
// RustFS validator, the reconciler that also derives lifecycle-rule identifiers
// from it, the provider's delivery check, two jq expressions rendered into the
// guest, and the canary workflow. Changing the root meant finding all of them,
// and #236 is the consequence: every tenant shares one read-write namespace and
// widening it is a twenty-four-site edit rather than a decision.
//
// Making the root one function is the prerequisite for making it per-tenant. It
// is not that change: the values it produces today are byte-identical to the
// literals it replaces, so no cache object moves and no IAM policy changes.
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
	// Organization and Repository are the account the fleet's cache lives under.
	// They are constants because there is exactly one today; #236 is the work of
	// making them a per-tenant parameter, and this package is where that change
	// happens once rather than in twenty-four places.
	Organization = "NDDev-OpenNetwork"
	Repository   = "github-actions"

	// trustSegment separates the trust-scoped cache from anything else that
	// might live under the repository prefix. It is a literal path segment and
	// not a template token, which is what the declared namespace template used
	// to omit -- the template described seven segments and the implementation
	// wrote eight.
	trustSegment = "trust"
)

// PrefixRoot is the credential-scoped root for a trust class. Every S3 policy,
// every lifecycle rule and every worker delivery is scoped to one of these.
func PrefixRoot(class TrustClass) (string, error) {
	if !class.Valid() {
		return "", fmt.Errorf("unknown cache trust class %q", string(class))
	}
	return strings.Join([]string{Organization, Repository, trustSegment, string(class)}, "/"), nil
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
	root := strings.Join([]string{Organization, Repository, trustSegment}, "/") + "/"
	below, found := strings.CutPrefix(prefix, root)
	if !found {
		return "", fmt.Errorf("prefix %q is not inside the fleet cache namespace", prefix)
	}
	below = strings.Trim(below, "/")
	if below == "" {
		return "", fmt.Errorf("prefix %q names no trust class", prefix)
	}
	return strings.ReplaceAll(below, "/", "-"), nil
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
