package observabilitydashboards

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

type ReconcileAction struct {
	Kind        string                `json:"kind"`
	Title       string                `json:"title"`
	DashboardID string                `json:"dashboard_id,omitempty"`
	Hash        string                `json:"hash,omitempty"`
	Desired     *OpenObserveDashboard `json:"desired,omitempty"`
}

type ReconcilePlan struct {
	SchemaVersion int               `json:"schema_version"`
	State         string            `json:"state"`
	Organization  string            `json:"organization"`
	Folder        string            `json:"folder"`
	Actions       []ReconcileAction `json:"actions"`
}

type OpenObserveClient struct {
	endpoint *url.URL
	username string
	password string
	http     *http.Client
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
	return &OpenObserveClient{endpoint: parsed, username: username, password: password, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (c *OpenObserveClient) request(ctx context.Context, method, resource string, body, output any) error {
	endpoint := *c.endpoint
	relative, err := url.Parse(resource)
	if err != nil {
		return err
	}
	endpoint.Path = path.Join(endpoint.Path, relative.Path)
	endpoint.RawQuery = relative.RawQuery
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return err
	}
	request.SetBasicAuth(c.username, c.password)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("call OpenObserve: %w", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil || len(content) > 4*1024*1024 {
		return errors.New("OpenObserve response is unreadable or exceeds the bounded limit")
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

type dashboardList struct {
	Dashboards []dashboardSummary `json:"dashboards"`
}

type dashboardSummary struct {
	Version     int    `json:"version"`
	Hash        string `json:"hash"`
	FolderID    string `json:"folder_id"`
	DashboardID string `json:"dashboard_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type dashboardEnvelope struct {
	Version int                   `json:"version"`
	Hash    string                `json:"hash"`
	V8      *OpenObserveDashboard `json:"v8"`
}

func (c *OpenObserveClient) Plan(ctx context.Context, organization, folder string, desired []OpenObserveDashboard) (ReconcilePlan, error) {
	if organization == "" || folder == "" {
		return ReconcilePlan{}, errors.New("organization and folder are required")
	}
	plan := ReconcilePlan{SchemaVersion: 1, State: "managed", Organization: organization, Folder: folder, Actions: []ReconcileAction{}}
	var existing dashboardList
	resource := fmt.Sprintf("/api/%s/dashboards?folder=%s", url.PathEscape(organization), url.QueryEscape(folder))
	if err := c.request(ctx, http.MethodGet, resource, nil, &existing); err != nil {
		return ReconcilePlan{}, err
	}
	desiredByTitle := make(map[string]OpenObserveDashboard, len(desired))
	for _, dashboard := range desired {
		if _, duplicate := desiredByTitle[dashboard.Title]; duplicate {
			return ReconcilePlan{}, fmt.Errorf("dashboard title %q is duplicated", dashboard.Title)
		}
		desiredByTitle[dashboard.Title] = dashboard
	}
	seen := make(map[string]struct{}, len(existing.Dashboards))
	for _, summary := range existing.Dashboards {
		wanted, managed := desiredByTitle[summary.Title]
		if managed {
			if _, duplicate := seen[summary.Title]; duplicate {
				return ReconcilePlan{}, fmt.Errorf("OpenObserve dashboard %q is duplicated", summary.Title)
			}
			seen[summary.Title] = struct{}{}
			var actual dashboardEnvelope
			get := fmt.Sprintf("/api/%s/dashboards/%s?folder=%s", url.PathEscape(organization), url.PathEscape(summary.DashboardID), url.QueryEscape(folder))
			if err := c.request(ctx, http.MethodGet, get, nil, &actual); err != nil {
				return ReconcilePlan{}, err
			}
			if actual.Version != 8 || actual.V8 == nil {
				copy := wanted
				plan.Actions = append(plan.Actions, ReconcileAction{Kind: "update", Title: wanted.Title, DashboardID: summary.DashboardID, Hash: actual.Hash, Desired: &copy})
				continue
			}
			wanted.DashboardID = summary.DashboardID
			actual.V8.Owner = ""
			if !reflect.DeepEqual(*actual.V8, wanted) {
				copy := wanted
				plan.Actions = append(plan.Actions, ReconcileAction{Kind: "update", Title: wanted.Title, DashboardID: summary.DashboardID, Hash: actual.Hash, Desired: &copy})
			}
			continue
		}
		if strings.HasPrefix(summary.Description, managedDescriptionPrefix) {
			plan.Actions = append(plan.Actions, ReconcileAction{Kind: "delete", Title: summary.Title, DashboardID: summary.DashboardID})
		}
	}
	for _, dashboard := range desired {
		if _, exists := seen[dashboard.Title]; !exists {
			copy := dashboard
			plan.Actions = append(plan.Actions, ReconcileAction{Kind: "create", Title: dashboard.Title, Desired: &copy})
		}
	}
	sort.Slice(plan.Actions, func(i, j int) bool { return plan.Actions[i].Title < plan.Actions[j].Title })
	if len(plan.Actions) != 0 {
		plan.State = "drifted"
	}
	return plan, nil
}

func (c *OpenObserveClient) Apply(ctx context.Context, organization, folder string, desired []OpenObserveDashboard) (ReconcilePlan, error) {
	plan, err := c.Plan(ctx, organization, folder, desired)
	if err != nil {
		return ReconcilePlan{}, err
	}
	for _, action := range plan.Actions {
		var method, resource string
		switch action.Kind {
		case "create":
			method, resource = http.MethodPost, fmt.Sprintf("/api/%s/dashboards?folder=%s", url.PathEscape(organization), url.QueryEscape(folder))
		case "update":
			method, resource = http.MethodPut, fmt.Sprintf("/api/%s/dashboards/%s?folder=%s&hash=%s", url.PathEscape(organization), url.PathEscape(action.DashboardID), url.QueryEscape(folder), url.QueryEscape(action.Hash))
		case "delete":
			method, resource = http.MethodDelete, fmt.Sprintf("/api/%s/dashboards/%s?folder=%s", url.PathEscape(organization), url.PathEscape(action.DashboardID), url.QueryEscape(folder))
		default:
			return ReconcilePlan{}, fmt.Errorf("unsupported dashboard action %q", action.Kind)
		}
		if err := c.request(ctx, method, resource, action.Desired, nil); err != nil {
			return ReconcilePlan{}, err
		}
	}
	verified, err := c.Plan(ctx, organization, folder, desired)
	if err != nil {
		return ReconcilePlan{}, err
	}
	if verified.State != "managed" {
		return verified, errors.New("OpenObserve dashboard read-back did not converge")
	}
	return verified, nil
}
