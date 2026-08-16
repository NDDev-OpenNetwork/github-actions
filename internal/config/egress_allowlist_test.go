package config

import (
	"strings"
	"testing"
)

// The per-pool allowlist and the egress ACL agree by construction rather than
// by ordering: the ACL rejects a fixed set of ranges, and a pool may not name a
// destination inside any of them. That is what makes it irrelevant which of two
// attached ACLs an implementation evaluates first -- a declared destination and
// a rejected range can never describe the same address.
func TestReleaseAllowlistCannotNameAnythingTheACLRejects(t *testing.T) {
	t.Parallel()

	for _, blocked := range ReservedEgressDestinations() {
		address := strings.Split(blocked, "/")[0] + "/32"
		issues := allowlistIssues(t, EgressDestination{
			Destination: address,
			Protocol:    "tcp",
			Ports:       "22",
			Purpose:     "reach something inside a denied range",
		})
		if !containsMessage(issues, "falls inside a range every pool is denied") {
			t.Fatalf("destination %s inside %s was accepted: %v", address, blocked, issues)
		}
	}
}

// Every field of a destination is reviewed, so every field is required to be
// the exact thing that was reviewed: one address, not a subnet that could grow;
// named ports, not a range; and a stated purpose, because a rule whose reason
// is not written down cannot be re-reviewed later.
func TestReleaseAllowlistRefusesAnythingWiderThanWhatWasReviewed(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		destination EgressDestination
		want        string
	}{
		"a subnet rather than one address": {
			EgressDestination{Destination: "203.0.113.0/24", Protocol: "tcp", Ports: "22", Purpose: "jump host"},
			"must be a single IPv4 /32",
		},
		"a port range rather than named ports": {
			EgressDestination{Destination: "203.0.113.7/32", Protocol: "tcp", Ports: "22-40", Purpose: "jump host"},
			"comma-separated exact ports",
		},
		"a protocol the ACL does not scope": {
			EgressDestination{Destination: "203.0.113.7/32", Protocol: "udp", Ports: "22", Purpose: "jump host"},
			"must be tcp",
		},
		"no stated reason": {
			EgressDestination{Destination: "203.0.113.7/32", Protocol: "tcp", Ports: "22", Purpose: "  "},
			"must state why this destination is reachable",
		},
		"not the network address of its prefix": {
			EgressDestination{Destination: "203.0.113.7/24", Protocol: "tcp", Ports: "22", Purpose: "jump host"},
			"must be a single IPv4 /32",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			issues := allowlistIssues(t, testCase.destination)
			if !containsMessage(issues, testCase.want) {
				t.Fatalf("wanted %q, got %v", testCase.want, issues)
			}
		})
	}
}

// A destination only means something under the policy that implements it.
// Declaring one on a public-internet pool would read as a boundary that exists
// and is not applied anywhere.
func TestOnlyReleasePoolsMayDeclareDestinations(t *testing.T) {
	t.Parallel()

	var issues []Issue
	add := func(path, message string) { issues = append(issues, Issue{Path: path, Message: message}) }
	validateEgressAllowlist(add, "pools[0]", Pool{Capabilities: Capabilities{
		NetworkPolicy: "public-internet",
		EgressAllowlist: []EgressDestination{{
			Destination: "203.0.113.7/32", Protocol: "tcp", Ports: "22", Purpose: "jump host",
		}},
	}})
	if !containsMessage(issues, "only a release-allowlist pool may declare egress destinations") {
		t.Fatalf("a public-internet pool kept its destinations: %v", issues)
	}
}

// The pool every host declares carries no destinations today, and that is a
// legal state meaning exactly the bounded public egress. Requiring one would
// only invite an invented address to satisfy a validator.
func TestAReleasePoolWithNoDestinationsIsValid(t *testing.T) {
	t.Parallel()

	var issues []Issue
	add := func(path, message string) { issues = append(issues, Issue{Path: path, Message: message}) }
	validateEgressAllowlist(add, "pools[0]", Pool{Capabilities: Capabilities{NetworkPolicy: "release-allowlist"}})
	if len(issues) != 0 {
		t.Fatalf("an empty release allowlist was refused: %v", issues)
	}
}

func allowlistIssues(t *testing.T, destination EgressDestination) []Issue {
	t.Helper()
	var issues []Issue
	add := func(path, message string) { issues = append(issues, Issue{Path: path, Message: message}) }
	validateEgressAllowlist(add, "pools[0]", Pool{Capabilities: Capabilities{
		NetworkPolicy:   "release-allowlist",
		EgressAllowlist: []EgressDestination{destination},
	}})
	return issues
}

func containsMessage(issues []Issue, want string) bool {
	for _, issue := range issues {
		if strings.Contains(issue.Message, want) {
			return true
		}
	}
	return false
}
