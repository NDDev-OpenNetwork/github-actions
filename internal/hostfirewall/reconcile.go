package hostfirewall

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/NDDev-OpenNetwork/github-actions/internal/incusplan"
)

const managedCommentPrefix = "gha-fleet-"

type Change struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"`
}

type Result struct {
	Applied bool     `json:"applied"`
	Changes []Change `json:"changes"`
}

type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

type Reconciler struct {
	Runner Runner
}

// Apply adds only explicitly declared gha-fleet UFW rules. Existing host rules
// are preserved. Managed rules that are no longer in the plan fail closed so a
// firewall policy change can never silently broaden access or delete operator
// state.
func (r Reconciler) Apply(ctx context.Context, plan incusplan.HostFirewall) (Result, error) {
	if r.Runner == nil {
		return Result{}, fmt.Errorf("host firewall runner is required")
	}
	if plan.Backend != "ufw" {
		return Result{}, fmt.Errorf("unsupported host firewall backend %q", plan.Backend)
	}

	status, err := r.Runner.Run(ctx, "status", "verbose")
	if err != nil {
		return Result{}, fmt.Errorf("inspect UFW status: %w", err)
	}
	if !hasLine(status, "Status: "+plan.RequiredStatus) {
		return Result{}, fmt.Errorf("UFW status is not %q", plan.RequiredStatus)
	}
	if !hasLine(status, "Default: "+plan.RequiredDefault) {
		return Result{}, fmt.Errorf("UFW defaults do not match %q", plan.RequiredDefault)
	}

	desired, err := desiredCommands(plan.Rules)
	if err != nil {
		return Result{}, err
	}
	currentOutput, err := r.Runner.Run(ctx, "show", "added")
	if err != nil {
		return Result{}, fmt.Errorf("inspect persistent UFW rules: %w", err)
	}
	current := commandSet(currentOutput)
	if stale := staleManaged(current, desired); len(stale) > 0 {
		return Result{}, fmt.Errorf("unmanaged gha-fleet UFW drift must be reviewed before mutation: %s", strings.Join(stale, "; "))
	}

	result := Result{Applied: true, Changes: []Change{}}
	for _, rule := range plan.Rules {
		command := renderCommand(rule.Args)
		if _, exists := current[command]; exists {
			continue
		}
		if _, err := r.Runner.Run(ctx, rule.Args...); err != nil {
			return Result{}, fmt.Errorf("apply UFW rule %q: %w", rule.Name, err)
		}
		result.Changes = append(result.Changes, Change{Kind: "host-firewall-rule", Name: rule.Name, Action: "create"})
	}

	verifiedOutput, err := r.Runner.Run(ctx, "show", "added")
	if err != nil {
		return Result{}, fmt.Errorf("verify persistent UFW rules: %w", err)
	}
	verified := commandSet(verifiedOutput)
	for command := range desired {
		if _, exists := verified[command]; !exists {
			return Result{}, fmt.Errorf("UFW did not persist desired rule %q", command)
		}
	}
	if stale := staleManaged(verified, desired); len(stale) > 0 {
		return Result{}, fmt.Errorf("unexpected gha-fleet UFW rules after reconciliation: %s", strings.Join(stale, "; "))
	}
	return result, nil
}

func desiredCommands(rules []incusplan.HostFirewallRule) (map[string]struct{}, error) {
	desired := make(map[string]struct{}, len(rules))
	names := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if rule.Name == "" {
			return nil, fmt.Errorf("host firewall rule name is required")
		}
		if _, exists := names[rule.Name]; exists {
			return nil, fmt.Errorf("host firewall rule name %q is duplicated", rule.Name)
		}
		names[rule.Name] = struct{}{}
		if len(rule.Args) < 3 || rule.Args[len(rule.Args)-2] != "comment" || !strings.HasPrefix(rule.Args[len(rule.Args)-1], managedCommentPrefix) {
			return nil, fmt.Errorf("host firewall rule %q requires a final %q managed comment", rule.Name, managedCommentPrefix)
		}
		command := renderCommand(rule.Args)
		if _, exists := desired[command]; exists {
			return nil, fmt.Errorf("host firewall command %q is duplicated", command)
		}
		desired[command] = struct{}{}
	}
	return desired, nil
}

func renderCommand(args []string) string {
	parts := slices.Clone(args)
	for index, part := range parts {
		ip, network, err := net.ParseCIDR(part)
		if err != nil {
			continue
		}
		ones, bits := network.Mask.Size()
		if ones == bits {
			parts[index] = ip.String()
		}
	}
	parts[len(parts)-1] = "'" + parts[len(parts)-1] + "'"
	return "ufw " + strings.Join(parts, " ")
}

func commandSet(output string) map[string]struct{} {
	commands := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ufw ") {
			commands[line] = struct{}{}
		}
	}
	return commands
}

func staleManaged(current, desired map[string]struct{}) []string {
	stale := make([]string, 0)
	for command := range current {
		if !strings.Contains(command, "comment '"+managedCommentPrefix) {
			continue
		}
		if _, exists := desired[command]; !exists {
			stale = append(stale, command)
		}
	}
	slices.Sort(stale)
	return stale
}

func hasLine(output, wanted string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == wanted {
			return true
		}
	}
	return false
}
