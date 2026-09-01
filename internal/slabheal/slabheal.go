// Package slabheal decides when a member may take itself out for the one
// mitigation unreclaimable slab has: a reboot.
//
// SUnreclaim regrew from ~100 MiB to ~4 GiB per member in a day, and the
// rolling reboot was manual. The decision is deliberately timid: it heals
// only above the alert's own threshold, only when this member's gate is open
// (a closed gate belongs to its operator), only when every other member is
// open (one member at a time, fleet-wide), only when no job occupies the
// member (a drain never aborts a job, but a heal should not close capacity
// someone is using), and only outside a cooldown (a reboot that did not help
// must page a human, not loop).
package slabheal

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Facts is everything the decision reads, gathered by the caller.
type Facts struct {
	SUnreclaimBytes  uint64
	ThresholdBytes   uint64
	SelfState        string
	OtherStates      map[string]string
	NonWarmOccupants int
	LastHealAt       time.Time
	Cooldown         time.Duration
	Now              time.Time
}

// Decision names the action and the reason it was or was not taken.
type Decision struct {
	Heal   bool
	Reason string
}

// Decide applies the guards in the order a reader would defend them.
func Decide(facts Facts) Decision {
	if facts.ThresholdBytes == 0 {
		return Decision{Reason: "threshold is zero; refusing to heal on an unset budget"}
	}
	if facts.SUnreclaimBytes <= facts.ThresholdBytes {
		return Decision{Reason: fmt.Sprintf("SUnreclaim %d is within the %d budget", facts.SUnreclaimBytes, facts.ThresholdBytes)}
	}
	if facts.SelfState != "open" {
		return Decision{Reason: fmt.Sprintf("own gate is %q; a closed gate belongs to its operator", facts.SelfState)}
	}
	for member, state := range facts.OtherStates {
		if state != "open" {
			return Decision{Reason: fmt.Sprintf("member %s gate is %q; one member heals at a time", member, state)}
		}
	}
	if facts.NonWarmOccupants > 0 {
		return Decision{Reason: fmt.Sprintf("%d non-warm occupants; healing waits for a quiet member", facts.NonWarmOccupants)}
	}
	if !facts.LastHealAt.IsZero() && facts.Now.Sub(facts.LastHealAt) < facts.Cooldown {
		return Decision{Reason: fmt.Sprintf("healed %s ago, inside the %s cooldown; a reboot that did not help needs a human", facts.Now.Sub(facts.LastHealAt).Round(time.Minute), facts.Cooldown)}
	}
	return Decision{Heal: true, Reason: fmt.Sprintf("SUnreclaim %d exceeds the %d budget on a quiet open member", facts.SUnreclaimBytes, facts.ThresholdBytes)}
}

// PreferAttributed picks the measurement the guard should act on. The
// global meminfo counter drifts under container churn (measured 1041 MiB
// against a 44 MiB memcg-attributed truth on 2026-09-01, the gap
// unattributable to any live cache), so when the root cgroup exposes
// slab_unreclaimable that attributed value wins; the counter remains the
// fallback for hosts without cgroup v2 memory accounting.
func PreferAttributed(attributed uint64, attributedOK bool, counter uint64) (uint64, string) {
	if attributedOK {
		return attributed, "memcg-attributed"
	}
	return counter, "meminfo-counter"
}

// ParseAttributedSlabUnreclaimable reads slab_unreclaimable (bytes) from
// cgroup v2 memory.stat content.
func ParseAttributedSlabUnreclaimable(reader io.Reader) (uint64, error) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "slab_unreclaimable" {
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("malformed slab_unreclaimable value %q", fields[1])
			}
			return value, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("memory.stat has no slab_unreclaimable line")
}

// ParseSUnreclaim reads SUnreclaim from /proc/meminfo content.
func ParseSUnreclaim(reader io.Reader) (uint64, error) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "SUnreclaim:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed SUnreclaim line %q", line)
		}
		kib, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("malformed SUnreclaim value %q", fields[1])
		}
		return kib * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("read meminfo: %w", err)
	}
	return 0, fmt.Errorf("meminfo carries no SUnreclaim line")
}

// HealReasonPrefix marks a drain this package owns, so the boot-side restore
// reopens exactly the drains it created and never an operator's.
const HealReasonPrefix = "slab-heal: "
