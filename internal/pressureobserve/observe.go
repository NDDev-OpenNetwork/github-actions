package pressureobserve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
	"github.com/NDDev-OpenNetwork/github-actions/internal/pressuregate"
	"github.com/NDDev-OpenNetwork/github-actions/internal/psimetrics"
)

const DefaultMaxStaleness = 90 * time.Second

type Handler struct {
	StatePath    string
	HostRoot     string
	MaxStaleness time.Duration
	Now          func() time.Time
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now().UTC()
	}
	state, err := Load(h.StatePath)
	fresh := err == nil && Fresh(state, now, h.maxStaleness())
	switch r.URL.Path {
	case "/healthz":
		w.Header().Set("Content-Type", "application/json")
		if !fresh {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = fmt.Fprintf(w, "{\"healthy\":%t,\"fresh\":%t}\n", fresh, fresh)
	case "/metrics":
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		metrics := Render(state, now, h.maxStaleness(), err) +
			RenderPressure(hostprobe.ReadPressure(h.hostRoot())) +
			RenderCompliance(CollectCompliance(h.HostRoot, now))
		_, _ = w.Write([]byte(metrics))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h Handler) hostRoot() string {
	if h.HostRoot == "" {
		return "/"
	}
	return h.HostRoot
}

func (h Handler) maxStaleness() time.Duration {
	if h.MaxStaleness <= 0 {
		return DefaultMaxStaleness
	}
	return h.MaxStaleness
}

func Load(path string) (pressuregate.State, error) {
	info, err := os.Stat(path)
	if err != nil {
		return pressuregate.State{}, err
	}
	if !info.Mode().IsRegular() {
		return pressuregate.State{}, fmt.Errorf("pressure state must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return pressuregate.State{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var state pressuregate.State
	if err := decoder.Decode(&state); err != nil {
		return pressuregate.State{}, fmt.Errorf("decode pressure state: %w", err)
	}
	if err := state.Validate(); err != nil {
		return pressuregate.State{}, err
	}
	return state, nil
}

func Fresh(state pressuregate.State, now time.Time, maxStaleness time.Duration) bool {
	return maxStaleness > 0 && !state.ObservedAt.After(now.Add(time.Second)) && now.Sub(state.ObservedAt) <= maxStaleness
}

func Render(state pressuregate.State, now time.Time, maxStaleness time.Duration, loadErr error) string {
	var output strings.Builder
	gauge := func(name, help string, value float64) {
		fmt.Fprintf(&output, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", name, help, name, name, strconv.FormatFloat(value, 'f', -1, 64))
	}
	counter := func(name, help string, value uint64) {
		fmt.Fprintf(&output, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, value)
	}
	fresh := loadErr == nil && Fresh(state, now, maxStaleness)
	age := float64(-1)
	if !state.ObservedAt.IsZero() && !state.ObservedAt.After(now) {
		age = now.Sub(state.ObservedAt).Seconds()
	}
	gauge("gha_fleet_pressure_observer_up", "Whether the local pressure state is valid and fresh.", boolFloat(fresh))
	gauge("gha_fleet_pressure_sample_age_seconds", "Age of the local pressure publication, or -1 when unavailable.", age)
	counter("gha_fleet_host_oom_kills_total", "Kernel OOM kills observed since host boot.", state.OOMKillsTotal)
	gauge("gha_fleet_host_pressure_open", "Whether local pressure admission is open and fresh.", boolFloat(fresh && state.State == pressuregate.StateOpen))
	// The gate records why it is in its current state and that reason was
	// readable only by opening a JSON file on the host. When admission closes
	// and jobs queue, the reason is the first thing anyone asks for.
	reason := state.Reason
	if !fresh || reason == "" {
		reason = "unknown"
	}
	fmt.Fprintf(&output,
		"# HELP gha_fleet_host_pressure_state Current local admission state, labelled with the gate's own reason.\n"+
			"# TYPE gha_fleet_host_pressure_state gauge\ngha_fleet_host_pressure_state{reason=%q} 1\n", reason)
	return output.String()
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// RenderPressure publishes this host's pressure-stall information in exactly
// the families the services observer already publishes, so one query answers
// for every host in the fleet instead of only the one that runs no jobs.
//
// A read error is not silence: psi_available goes to 0, which is what the
// absence of the series could never say.
func RenderPressure(pressure hostprobe.Pressure, err error) string {
	if err != nil {
		return psimetrics.Render(hostprobe.Pressure{})
	}
	return psimetrics.Render(pressure)
}
