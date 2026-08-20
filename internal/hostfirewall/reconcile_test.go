package hostfirewall

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/incusplan"
)

func TestApplyAddsMissingRuleAndConverges(t *testing.T) {
	t.Parallel()

	plan := testPlan()
	runner := &fakeRunner{responses: []response{
		{output: testStatus},
		{output: "Added user rules (see 'ufw status' for running firewall):\n"},
		{},
		{output: "Added user rules (see 'ufw status' for running firewall):\n" + renderCommand(plan.Rules[0].Args) + "\n"},
	}}
	result, err := (Reconciler{Runner: runner}).Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Name != "dhcp" {
		t.Fatalf("unexpected changes: %#v", result.Changes)
	}
	if got := strings.Join(runner.calls[2], " "); got != strings.Join(plan.Rules[0].Args, " ") {
		t.Fatalf("unexpected mutation command: %s", got)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	t.Parallel()

	plan := testPlan()
	added := "Added user rules (see 'ufw status' for running firewall):\n" + renderCommand(plan.Rules[0].Args) + "\n"
	runner := &fakeRunner{responses: []response{{output: testStatus}, {output: added}, {output: added}}}
	result, err := (Reconciler{Runner: runner}).Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(result.Changes) != 0 || len(runner.calls) != 3 {
		t.Fatalf("expected an idempotent read-only convergence, result=%#v calls=%#v", result, runner.calls)
	}
}

func TestRenderCommandMatchesUFWHostCIDRNormalization(t *testing.T) {
	t.Parallel()
	command := renderCommand([]string{
		"route", "allow", "from", "198.51.100.0/24", "to", "203.0.113.20/32",
		"port", "22", "proto", "tcp", "comment", "gha-fleet-release-egress-v1",
	})
	if command != "ufw route allow from 198.51.100.0/24 to 203.0.113.20 port 22 proto tcp comment 'gha-fleet-release-egress-v1'" {
		t.Fatalf("unexpected normalized command: %s", command)
	}
}

func TestApplyRejectsUnsafeUFWDefaults(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{responses: []response{{output: "Status: active\nDefault: allow (incoming), allow (outgoing), allow (routed)\n"}}}
	_, err := (Reconciler{Runner: runner}).Apply(context.Background(), testPlan())
	if err == nil || !strings.Contains(err.Error(), "defaults do not match") {
		t.Fatalf("expected default-policy rejection, got %v", err)
	}
}

func TestApplyRejectsStaleManagedRule(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{responses: []response{
		{output: testStatus},
		{output: "ufw allow in on gha0 to 198.51.100.1 port 1 proto tcp comment 'gha-fleet-stale-v1'\n"},
	}}
	_, err := (Reconciler{Runner: runner}).Apply(context.Background(), testPlan())
	if err == nil || !strings.Contains(err.Error(), "drift must be reviewed") {
		t.Fatalf("expected managed-drift rejection, got %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("reconciler mutated after detecting drift: %#v", runner.calls)
	}
}

const testStatus = "Status: active\nDefault: deny (incoming), allow (outgoing), deny (routed)\n"

func testPlan() incusplan.HostFirewall {
	return incusplan.HostFirewall{
		Backend:         "ufw",
		RequiredStatus:  "active",
		RequiredDefault: "deny (incoming), allow (outgoing), deny (routed)",
		Rules: []incusplan.HostFirewallRule{{
			Name: "dhcp",
			Args: []string{"allow", "in", "on", "gha0", "to", "198.51.100.1", "port", "67", "proto", "udp", "comment", "gha-fleet-dhcp-v1"},
		}},
	}
}

type response struct {
	output string
	err    error
}

type fakeRunner struct {
	responses []response
	calls     [][]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(f.responses) == 0 {
		return "", fmt.Errorf("unexpected call: %v", args)
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response.output, response.err
}
