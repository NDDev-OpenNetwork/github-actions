package fleetobserve

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/pressureobserve"
)

type State struct {
	mutex   sync.RWMutex
	latest  Snapshot
	present bool
}

func (s *State) Set(snapshot Snapshot) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.latest = snapshot
	s.present = true
}

func (s *State) Latest() (Snapshot, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.latest, s.present
}

type Handler struct {
	State        *State
	MaxStaleness time.Duration
	Now          func() time.Time
	HostRoot     string
}

func (h Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request == nil || request.URL == nil || request.URL.RawQuery != "" || request.URL.RawPath != "" {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	snapshot, present := h.latest()
	if !present {
		http.Error(writer, "observer has no sample", http.StatusServiceUnavailable)
		return
	}
	now := h.now()
	age := now.Sub(snapshot.CapturedAt)
	fresh := h.MaxStaleness > 0 && age >= 0 && age <= h.MaxStaleness

	switch request.URL.Path {
	case "/metrics":
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		metrics := RenderPrometheus(snapshot, now, h.MaxStaleness) + pressureobserve.RenderCompliance(pressureobserve.CollectCompliance(h.HostRoot, now))
		_, _ = writer.Write([]byte(metrics))
	case "/snapshot":
		writer.Header().Set("Content-Type", "application/json")
		writeHTTPJSON(writer, http.StatusOK, snapshot)
	case "/healthz":
		status := http.StatusOK
		if !snapshot.Healthy || !fresh {
			status = http.StatusServiceUnavailable
		}
		writeHTTPJSON(writer, status, struct {
			Healthy    bool      `json:"healthy"`
			Fresh      bool      `json:"fresh"`
			CapturedAt time.Time `json:"captured_at"`
		}{Healthy: snapshot.Healthy, Fresh: fresh, CapturedAt: snapshot.CapturedAt})
	default:
		http.Error(writer, http.StatusText(http.StatusNotFound), http.StatusNotFound)
	}
}

func (h Handler) latest() (Snapshot, bool) {
	if h.State == nil {
		return Snapshot{}, false
	}
	return h.State.Latest()
}

func (h Handler) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}

func writeHTTPJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(true)
	_ = encoder.Encode(value)
}
