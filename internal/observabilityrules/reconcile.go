package observabilityrules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"
)

const managedTag = "managed-by:gds"

type ReconcileAction struct {
	Kind    string            `json:"kind"`
	Name    string            `json:"name"`
	AlertID string            `json:"alert_id,omitempty"`
	Desired *OpenObserveAlert `json:"desired,omitempty"`
}

type ReconcilePlan struct {
	SchemaVersion      int               `json:"schema_version"`
	State              string            `json:"state"`
	Organization       string            `json:"organization"`
	Destination        string            `json:"destination"`
	DestinationPresent bool              `json:"destination_present"`
	MissingStreams     []string          `json:"missing_streams"`
	Actions            []ReconcileAction `json:"actions"`
}

type OpenObserveClient struct {
	Endpoint   *url.URL
	Username   string
	Password   string
	HTTPClient *http.Client
}

func NewOpenObserveClient(endpoint, username, password string) (*OpenObserveClient, error) {
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("OpenObserve endpoint must be an HTTP(S) origin")
	}
	if strings.TrimSpace(username) == "" || password == "" {
		return nil, errors.New("OpenObserve credentials are incomplete")
	}
	return &OpenObserveClient{
		Endpoint: parsed, Username: username, Password: password,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *OpenObserveClient) request(ctx context.Context, method, resource string, body any, output any) error {
	endpoint := *c.Endpoint
	relative, err := url.Parse(resource)
	if err != nil {
		return fmt.Errorf("parse OpenObserve resource: %w", err)
	}
	endpoint.Path = path.Join(endpoint.Path, relative.Path)
	endpoint.RawQuery = relative.RawQuery
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode OpenObserve request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return fmt.Errorf("build OpenObserve request: %w", err)
	}
	request.SetBasicAuth(c.Username, c.Password)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("call OpenObserve: %w", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil {
		return fmt.Errorf("read OpenObserve response: %w", err)
	}
	if len(content) > 4*1024*1024 {
		return errors.New("OpenObserve response exceeds the bounded limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("OpenObserve %s %s returned %d: %s", method, resource, response.StatusCode, strings.TrimSpace(string(content)))
	}
	if output != nil && len(content) != 0 {
		if err := json.Unmarshal(content, output); err != nil {
			return fmt.Errorf("decode OpenObserve response: %w", err)
		}
	}
	return nil
}

type destinationSummary struct {
	Name string `json:"name"`
}

type streamList struct {
	List []struct {
		Name string `json:"name"`
	} `json:"list"`
}

type alertList struct {
	List []struct {
		AlertID string   `json:"alert_id"`
		Name    string   `json:"name"`
		Tags    []string `json:"tags"`
	} `json:"list"`
}

func (c *OpenObserveClient) Plan(ctx context.Context, desired OpenObserveBundle) (ReconcilePlan, error) {
	plan := ReconcilePlan{
		SchemaVersion: 1, State: "managed", Organization: desired.Organization,
		Destination: desired.Destination, MissingStreams: []string{}, Actions: []ReconcileAction{},
	}
	var destinations []destinationSummary
	if err := c.request(ctx, http.MethodGet, fmt.Sprintf("/api/%s/alerts/destinations", url.PathEscape(desired.Organization)), nil, &destinations); err != nil {
		return ReconcilePlan{}, err
	}
	for _, destination := range destinations {
		if destination.Name == desired.Destination {
			plan.DestinationPresent = true
		}
	}
	var streams streamList
	if err := c.request(ctx, http.MethodGet, fmt.Sprintf("/api/%s/streams?type=metrics", url.PathEscape(desired.Organization)), nil, &streams); err != nil {
		return ReconcilePlan{}, err
	}
	available := make(map[string]struct{}, len(streams.List))
	for _, stream := range streams.List {
		available[stream.Name] = struct{}{}
	}
	for _, alert := range desired.Alerts {
		if _, exists := available[alert.StreamName]; !exists {
			plan.MissingStreams = append(plan.MissingStreams, alert.StreamName)
		}
	}
	sort.Strings(plan.MissingStreams)
	plan.MissingStreams = compact(plan.MissingStreams)
	if !plan.DestinationPresent || len(plan.MissingStreams) != 0 {
		plan.State = "blocked"
		return plan, nil
	}

	var existing alertList
	if err := c.request(ctx, http.MethodGet, fmt.Sprintf("/api/v2/%s/alerts", url.PathEscape(desired.Organization)), nil, &existing); err != nil {
		return ReconcilePlan{}, err
	}
	desiredByName := make(map[string]OpenObserveAlert, len(desired.Alerts))
	for _, alert := range desired.Alerts {
		desiredByName[alert.Name] = alert
	}
	seen := make(map[string]struct{}, len(existing.List))
	for _, summary := range existing.List {
		wanted, managed := desiredByName[summary.Name]
		if managed {
			if _, duplicate := seen[summary.Name]; duplicate {
				return ReconcilePlan{}, fmt.Errorf("OpenObserve alert %q is duplicated", summary.Name)
			}
			seen[summary.Name] = struct{}{}
			var actual OpenObserveAlert
			if err := c.request(ctx, http.MethodGet, fmt.Sprintf("/api/v2/%s/alerts/%s", url.PathEscape(desired.Organization), url.PathEscape(summary.AlertID)), nil, &actual); err != nil {
				return ReconcilePlan{}, err
			}
			if !reflect.DeepEqual(actual, wanted) {
				copy := wanted
				plan.Actions = append(plan.Actions, ReconcileAction{Kind: "update", Name: wanted.Name, AlertID: summary.AlertID, Desired: &copy})
			}
			continue
		}
		if contains(summary.Tags, managedTag) {
			plan.Actions = append(plan.Actions, ReconcileAction{Kind: "delete", Name: summary.Name, AlertID: summary.AlertID})
		}
	}
	for _, alert := range desired.Alerts {
		if _, exists := seen[alert.Name]; !exists {
			copy := alert
			plan.Actions = append(plan.Actions, ReconcileAction{Kind: "create", Name: alert.Name, Desired: &copy})
		}
	}
	sort.Slice(plan.Actions, func(i, j int) bool {
		if plan.Actions[i].Name == plan.Actions[j].Name {
			return plan.Actions[i].Kind < plan.Actions[j].Kind
		}
		return plan.Actions[i].Name < plan.Actions[j].Name
	})
	if len(plan.Actions) != 0 {
		plan.State = "drifted"
	}
	return plan, nil
}

func (c *OpenObserveClient) Apply(ctx context.Context, desired OpenObserveBundle) (ReconcilePlan, error) {
	plan, err := c.Plan(ctx, desired)
	if err != nil {
		return ReconcilePlan{}, err
	}
	if plan.State == "blocked" {
		return plan, errors.New("OpenObserve reconciliation is blocked by destination or stream prerequisites")
	}
	for _, action := range plan.Actions {
		var method, resource string
		switch action.Kind {
		case "create":
			method = http.MethodPost
			resource = fmt.Sprintf("/api/v2/%s/alerts", url.PathEscape(desired.Organization))
		case "update":
			method = http.MethodPut
			resource = fmt.Sprintf("/api/v2/%s/alerts/%s", url.PathEscape(desired.Organization), url.PathEscape(action.AlertID))
		case "delete":
			method = http.MethodDelete
			resource = fmt.Sprintf("/api/v2/%s/alerts/%s", url.PathEscape(desired.Organization), url.PathEscape(action.AlertID))
		default:
			return ReconcilePlan{}, fmt.Errorf("unsupported OpenObserve action %q", action.Kind)
		}
		if err := c.request(ctx, method, resource, action.Desired, nil); err != nil {
			return ReconcilePlan{}, err
		}
	}
	verified, err := c.Plan(ctx, desired)
	if err != nil {
		return ReconcilePlan{}, err
	}
	if verified.State != "managed" {
		return verified, errors.New("OpenObserve read-back did not converge")
	}
	return verified, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func compact(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
