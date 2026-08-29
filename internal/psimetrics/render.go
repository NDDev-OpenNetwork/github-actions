// Package psimetrics renders Linux pressure-stall information as Prometheus
// text, in one place, because two observers publish it and they must publish
// exactly the same series.
//
// The services host published these families from the beginning. The four
// compute members -- the only hosts that run jobs -- published a single boolean
// derived from the same numbers, so the values the admission gate actually
// decides on existed nowhere anyone could read. Every question about fleet
// pressure was answered from the one host with no workers on it.
package psimetrics

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
)

// Render writes gha_fleet_host_psi_available, gha_fleet_host_psi_stall_percent
// and gha_fleet_host_psi_stall_seconds_total for one host's pressure sample.
func Render(pressure hostprobe.Pressure) string {
	var output strings.Builder
	fmt.Fprintf(&output,
		"# HELP gha_fleet_host_psi_available Whether Linux pressure stall information is available.\n"+
			"# TYPE gha_fleet_host_psi_available gauge\ngha_fleet_host_psi_available %s\n",
		formatFloat(boolFloat(pressure.Available)))
	fmt.Fprint(&output,
		"# HELP gha_fleet_host_psi_stall_percent Percentage of wall time stalled over each Linux PSI averaging window.\n"+
			"# TYPE gha_fleet_host_psi_stall_percent gauge\n")
	fmt.Fprint(&output,
		"# HELP gha_fleet_host_psi_stall_seconds_total Cumulative Linux PSI stall time since host boot.\n"+
			"# TYPE gha_fleet_host_psi_stall_seconds_total counter\n")
	for _, resource := range []struct {
		name     string
		pressure hostprobe.PressureResource
	}{
		{name: "cpu", pressure: pressure.CPU},
		{name: "memory", pressure: pressure.Memory},
		{name: "io", pressure: pressure.IO},
	} {
		for _, mode := range []struct {
			name   string
			window hostprobe.PressureWindow
		}{
			{name: "some", window: resource.pressure.Some},
			{name: "full", window: resource.pressure.Full},
		} {
			for _, average := range []struct {
				window string
				value  float64
			}{
				{window: "10", value: mode.window.Avg10},
				{window: "60", value: mode.window.Avg60},
				{window: "300", value: mode.window.Avg300},
			} {
				metric(&output, "gha_fleet_host_psi_stall_percent", map[string]string{
					"resource": resource.name, "mode": mode.name, "window_seconds": average.window,
				}, average.value)
			}
			metric(&output, "gha_fleet_host_psi_stall_seconds_total", map[string]string{
				"resource": resource.name, "mode": mode.name,
			}, float64(mode.window.TotalMicros)/1_000_000)
		}
	}
	return output.String()
}

func metric(output *strings.Builder, name string, labels map[string]string, value float64) {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	output.WriteString(name)
	if len(keys) != 0 {
		output.WriteByte('{')
		for index, key := range keys {
			if index != 0 {
				output.WriteByte(',')
			}
			fmt.Fprintf(output, "%s=%q", key, labels[key])
		}
		output.WriteByte('}')
	}
	output.WriteByte(' ')
	output.WriteString(formatFloat(value))
	output.WriteByte('\n')
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
