package joblifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Exporter delivers lifecycle records to an OpenObserve logs stream.
type Exporter struct {
	Endpoint   *url.URL
	Username   string
	Password   string
	HTTPClient *http.Client
}

// NewExporter validates the OpenObserve origin the way the alert reconciler
// does: a bare HTTP(S) origin, credentials complete.
func NewExporter(endpoint, username, password string) (*Exporter, error) {
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("OpenObserve endpoint must be an HTTP(S) origin")
	}
	if username == "" || password == "" {
		return nil, fmt.Errorf("OpenObserve credentials are incomplete")
	}
	return &Exporter{Endpoint: parsed, Username: username, Password: password,
		HTTPClient: &http.Client{Timeout: 30 * time.Second}}, nil
}

// Export posts the records as one bulk ingestion. The caller persists its
// watermarks only after Export returns nil, so a failed delivery retries the
// same records rather than losing them.
func (e *Exporter) Export(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	wire := make([]map[string]any, 0, len(records))
	for _, record := range records {
		var flat map[string]any
		encoded, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("encode lifecycle record: %w", err)
		}
		if err := json.Unmarshal(encoded, &flat); err != nil {
			return fmt.Errorf("flatten lifecycle record: %w", err)
		}
		// OpenObserve takes the event time as integer microseconds.
		flat["_timestamp"] = record.Timestamp.UnixMicro()
		wire = append(wire, flat)
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return fmt.Errorf("encode lifecycle batch: %w", err)
	}
	endpoint := *e.Endpoint
	endpoint.Path = "/api/default/" + StreamName + "/_json"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build lifecycle export request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(e.Username, e.Password)
	response, err := e.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("deliver lifecycle batch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("OpenObserve ingestion returned %d", response.StatusCode)
	}
	return nil
}
