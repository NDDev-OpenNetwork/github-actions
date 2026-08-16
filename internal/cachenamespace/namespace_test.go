package cachenamespace

import (
	"strings"
	"testing"
)

// The declared template and the built key must have the same shape. They did
// not: the template named eight segments and the implementation wrote nine,
// because the literal `trust` segment was in the code and not in the template,
// and the config validator only checked that each token appeared somewhere.
func TestTemplateDescribesTheKeyThatIsBuilt(t *testing.T) {
	t.Parallel()
	key, err := Object{
		Class: Untrusted, Platform: "linux", Arch: "amd64",
		Toolchain: "curl-sigv4-v1", LockDigest: "abc", RefClass: "canary", Name: "object.txt",
	}.Key()
	if err != nil {
		t.Fatal(err)
	}
	template := strings.Split(Template(), "/")
	// The object name is below the templated namespace, so it is dropped before
	// the shapes are compared.
	built := strings.Split(strings.TrimSuffix(key, "/object.txt"), "/")
	if len(template) != len(built) {
		t.Fatalf("template has %d segments (%s), a built key has %d (%s)",
			len(template), Template(), len(built), key)
	}
	for index, segment := range template {
		if strings.HasPrefix(segment, "{") {
			continue
		}
		if built[index] != segment {
			t.Errorf("segment %d: template says literal %q, key has %q", index, segment, built[index])
		}
	}
}

func TestKeyRefusesComponentsThatWouldEscapeTheNamespace(t *testing.T) {
	t.Parallel()
	base := Object{
		Class: Trusted, Platform: "linux", Arch: "amd64",
		Toolchain: "t", LockDigest: "d", RefClass: "main", Name: "o",
	}
	for _, testCase := range []struct {
		name    string
		mutate  func(*Object)
		message string
	}{
		{"unknown class", func(o *Object) { o.Class = "public" }, "unknown cache trust class"},
		{"empty component", func(o *Object) { o.Toolchain = "" }, "is empty"},
		// A slash in a component invents a path segment the S3 policy never
		// granted, which is how a trusted credential could be steered to write
		// somewhere the policy believed was a different prefix.
		{"slash in a component", func(o *Object) { o.RefClass = "main/../promoted" }, "must be one path segment"},
		{"traversal component", func(o *Object) { o.Platform = ".." }, "must be one path segment"},
		{"absolute object", func(o *Object) { o.Name = "/etc/passwd" }, "relative in-namespace path"},
		{"traversal object", func(o *Object) { o.Name = "a/../../b" }, "relative in-namespace path"},
	} {
		mutated := base
		testCase.mutate(&mutated)
		_, err := mutated.Key()
		if err == nil {
			t.Errorf("%s: accepted", testCase.name)
			continue
		}
		if !strings.Contains(err.Error(), testCase.message) {
			t.Errorf("%s: error %q does not mention %q", testCase.name, err, testCase.message)
		}
	}
}

func TestIdentifierRefusesAPrefixOutsideTheNamespace(t *testing.T) {
	t.Parallel()
	if _, err := Identifier("some-other-account/cache/trust/trusted"); err == nil {
		t.Fatal("an identifier was derived for a prefix outside the fleet namespace")
	}
	got, err := Identifier(MustPrefixRoot(Promoted))
	if err != nil || got != "promoted" {
		t.Fatalf("Identifier(promoted root) = %q, %v", got, err)
	}
}

// Isolation is proven by writing to the counterpart and being refused, so the
// counterpart must never be the class itself.
func TestCounterpartIsNeverTheClassItself(t *testing.T) {
	t.Parallel()
	for _, class := range TrustClasses() {
		counterpart, err := Counterpart(class)
		if err != nil {
			t.Fatalf("no counterpart for %q: %v", class, err)
		}
		if counterpart == class {
			t.Errorf("%q is its own counterpart, so the isolation proof would write where it is allowed", class)
		}
	}
	if _, err := Counterpart("public"); err == nil {
		t.Fatal("an unknown class was given a counterpart")
	}
}
