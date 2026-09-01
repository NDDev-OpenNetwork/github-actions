package observabilitydashboards

import (
	"fmt"
	"regexp"
)

const managedDescriptionPrefix = "managed-by:gds;dashboard-contract:v1;"

var metricPattern = regexp.MustCompile(`(?:gha_fleet_|gha_diagnostic_storage_|otelcol_exporter_|system_cpu_time|system_memory_usage|system_paging_)[a-zA-Z0-9_:]*`)

type OpenObserveDashboard struct {
	Version                 int              `json:"version"`
	DashboardID             string           `json:"dashboardId,omitempty"`
	Title                   string           `json:"title"`
	Description             string           `json:"description"`
	Role                    string           `json:"role"`
	Owner                   string           `json:"owner,omitempty"`
	Tabs                    []OpenObserveTab `json:"tabs"`
	Variables               Variables        `json:"variables"`
	DefaultDatetimeDuration DateTimeOptions  `json:"defaultDatetimeDuration"`
}

type Variables struct {
	List               []any `json:"list"`
	ShowDynamicFilters bool  `json:"showDynamicFilters"`
}

type DateTimeOptions struct {
	Type               string `json:"type"`
	RelativeTimePeriod string `json:"relativeTimePeriod"`
}

type OpenObserveTab struct {
	TabID  string             `json:"tabId"`
	Name   string             `json:"name"`
	Panels []OpenObservePanel `json:"panels"`
}

type OpenObservePanel struct {
	ID          string             `json:"id"`
	Type        string             `json:"type"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Config      PanelConfig        `json:"config"`
	QueryType   string             `json:"queryType"`
	Queries     []OpenObserveQuery `json:"queries"`
	Layout      PanelLayout        `json:"layout"`
}

type PanelConfig struct {
	ShowLegends     bool   `json:"show_legends"`
	LegendsPosition string `json:"legends_position,omitempty"`
	Unit            string `json:"unit,omitempty"`
}

type OpenObserveQuery struct {
	Query            string      `json:"query"`
	VRLFunctionQuery string      `json:"vrlFunctionQuery"`
	CustomQuery      bool        `json:"customQuery"`
	Fields           PanelFields `json:"fields"`
	Config           QueryConfig `json:"config"`
}

type PanelFields struct {
	Stream     string      `json:"stream"`
	StreamType string      `json:"stream_type"`
	X          []any       `json:"x"`
	Y          []any       `json:"y"`
	Z          []any       `json:"z"`
	Breakdown  []any       `json:"breakdown"`
	Filter     PanelFilter `json:"filter"`
}

type PanelFilter struct {
	FilterType      string `json:"filterType"`
	LogicalOperator string `json:"logicalOperator"`
	Conditions      []any  `json:"conditions"`
}

type QueryConfig struct {
	PromQLLegend string `json:"promql_legend"`
	LayerType    string `json:"layer_type"`
}

type PanelLayout struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
	I int `json:"i"`
}

func RenderOpenObserve(bundle Bundle) ([]OpenObserveDashboard, error) {
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	result := make([]OpenObserveDashboard, 0, len(bundle.Dashboards))
	for _, dashboard := range bundle.Dashboards {
		panels := make([]OpenObservePanel, 0, len(dashboard.Panels))
		for index, panel := range dashboard.Panels {
			queryType := "promql"
			streamType := "metrics"
			stream := ""
			if panel.QueryLanguage == "sql" {
				// A SQL panel charts a logs stream; the statement is the whole
				// query and the stream identity is declared, not derived.
				queryType = "sql"
				streamType = "logs"
				stream = panel.StreamName
			} else {
				stream = metricPattern.FindString(panel.Query)
				if stream == "" {
					return nil, fmt.Errorf("dashboard %q panel %q has no metric identity", dashboard.ID, panel.ID)
				}
			}
			panelType := "line"
			if panel.Kind == "stat" {
				panelType = "metric"
			} else if panel.Kind == "table" {
				panelType = "table"
			}
			panels = append(panels, OpenObservePanel{
				ID: panel.ID, Type: panelType, Title: panel.Title, Description: panel.Description,
				Config:    PanelConfig{ShowLegends: panel.Kind == "timeseries", Unit: openObserveUnit(panel.Unit)},
				QueryType: queryType,
				Queries: []OpenObserveQuery{{
					Query: panel.Query, CustomQuery: true,
					Fields: PanelFields{Stream: stream, StreamType: streamType, X: []any{}, Y: []any{}, Z: []any{}, Breakdown: []any{},
						Filter: PanelFilter{FilterType: "group", LogicalOperator: "AND", Conditions: []any{}}},
					Config: QueryConfig{LayerType: "scatter"},
				}},
				Layout: PanelLayout{X: (index % 2) * 96, Y: (index / 2) * 9, W: 96, H: 9, I: index + 1},
			})
		}
		result = append(result, OpenObserveDashboard{
			Version: 8, Title: dashboard.Title,
			Description: fmt.Sprintf("%sid:%s;owner:%s;refresh:%d;runbook:%s", managedDescriptionPrefix, dashboard.ID, dashboard.Owner, dashboard.RefreshSeconds, dashboard.Runbook),
			Role:        "", Tabs: []OpenObserveTab{{TabID: dashboard.ID + "_overview", Name: "Overview", Panels: panels}},
			Variables: Variables{List: []any{}}, DefaultDatetimeDuration: DateTimeOptions{Type: "relative", RelativeTimePeriod: dashboard.DefaultRange},
		})
	}
	return result, nil
}

func openObserveUnit(unit string) string {
	switch unit {
	case "bytes":
		return "bytes"
	case "bytes_per_second":
		return "bytes"
	case "percent":
		return "percent-1"
	case "seconds":
		return "s"
	default:
		return ""
	}
}
