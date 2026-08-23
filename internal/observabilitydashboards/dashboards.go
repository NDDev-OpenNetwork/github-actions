package observabilitydashboards

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const SchemaVersion = 1

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)

type Bundle struct {
	SchemaVersion int         `json:"schema_version" yaml:"schema_version"`
	Backend       string      `json:"backend" yaml:"backend"`
	Organization  string      `json:"organization" yaml:"organization"`
	Dashboards    []Dashboard `json:"dashboards" yaml:"dashboards"`
}

type Dashboard struct {
	ID             string  `json:"id" yaml:"id"`
	Title          string  `json:"title" yaml:"title"`
	RefreshSeconds int     `json:"refresh_seconds" yaml:"refresh_seconds"`
	DefaultRange   string  `json:"default_range" yaml:"default_range"`
	Owner          string  `json:"owner" yaml:"owner"`
	Runbook        string  `json:"runbook" yaml:"runbook"`
	Panels         []Panel `json:"panels" yaml:"panels"`
}

type Panel struct {
	ID          string `json:"id" yaml:"id"`
	Title       string `json:"title" yaml:"title"`
	Kind        string `json:"kind" yaml:"kind"`
	Query       string `json:"query" yaml:"query"`
	Unit        string `json:"unit" yaml:"unit"`
	Description string `json:"description" yaml:"description"`
}

func Load(path string) (Bundle, error) {
	file, err := os.Open(path)
	if err != nil {
		return Bundle{}, fmt.Errorf("open observability dashboards: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(io.LimitReader(file, 256*1024+1))
	decoder.KnownFields(true)
	var bundle Bundle
	if err := decoder.Decode(&bundle); err != nil {
		return Bundle{}, fmt.Errorf("decode observability dashboards: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Bundle{}, fmt.Errorf("observability dashboards contain multiple YAML documents")
	}
	if err := bundle.Validate(); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func (b Bundle) Validate() error {
	if b.SchemaVersion != SchemaVersion || b.Backend != "openobserve" || b.Organization != "default" {
		return fmt.Errorf("dashboard bundle identity is invalid")
	}
	if len(b.Dashboards) < 5 || len(b.Dashboards) > 16 {
		return fmt.Errorf("dashboard count is invalid")
	}
	ids := make([]string, 0, len(b.Dashboards))
	seen := map[string]struct{}{}
	for _, dashboard := range b.Dashboards {
		if err := dashboard.Validate(); err != nil {
			return fmt.Errorf("dashboard %q: %w", dashboard.ID, err)
		}
		if _, ok := seen[dashboard.ID]; ok {
			return fmt.Errorf("dashboard %q is duplicated", dashboard.ID)
		}
		seen[dashboard.ID] = struct{}{}
		ids = append(ids, dashboard.ID)
	}
	if !sort.StringsAreSorted(ids) {
		return fmt.Errorf("dashboards must be sorted by id")
	}
	return nil
}

func (d Dashboard) Validate() error {
	if !idPattern.MatchString(d.ID) || d.Title == "" || d.Owner == "" || d.RefreshSeconds < 15 || d.RefreshSeconds > 300 {
		return fmt.Errorf("identity, title, owner or refresh is invalid")
	}
	if _, ok := map[string]struct{}{"15m": {}, "1h": {}, "6h": {}, "24h": {}}[d.DefaultRange]; !ok {
		return fmt.Errorf("default range is invalid")
	}
	if !strings.HasPrefix(d.Runbook, "https://github.com/NDDev-OpenNetwork/github-actions/blob/") {
		return fmt.Errorf("runbook must be public and source-bound")
	}
	if len(d.Panels) < 2 || len(d.Panels) > 16 {
		return fmt.Errorf("panel count is invalid")
	}
	ids := make([]string, 0, len(d.Panels))
	seen := map[string]struct{}{}
	for _, panel := range d.Panels {
		if err := panel.Validate(); err != nil {
			return fmt.Errorf("panel %q: %w", panel.ID, err)
		}
		if _, ok := seen[panel.ID]; ok {
			return fmt.Errorf("panel %q is duplicated", panel.ID)
		}
		seen[panel.ID] = struct{}{}
		ids = append(ids, panel.ID)
	}
	if !sort.StringsAreSorted(ids) {
		return fmt.Errorf("panels must be sorted by id")
	}
	return nil
}

func (p Panel) Validate() error {
	if !idPattern.MatchString(p.ID) || p.Title == "" || p.Description == "" || len(p.Query) < 3 || len(p.Query) > 4096 || strings.ContainsAny(p.Query, "\r\x00") {
		return fmt.Errorf("identity, title, description or query is invalid")
	}
	if _, ok := map[string]struct{}{"stat": {}, "table": {}, "timeseries": {}}[p.Kind]; !ok {
		return fmt.Errorf("kind is invalid")
	}
	if _, ok := map[string]struct{}{"bytes": {}, "count": {}, "percent": {}, "seconds": {}, "state": {}}[p.Unit]; !ok {
		return fmt.Errorf("unit is invalid")
	}
	if !strings.Contains(p.Query, "gha_fleet_") && !strings.Contains(p.Query, "otelcol_exporter_") {
		return fmt.Errorf("query does not use an owned fleet or Collector metric")
	}
	return nil
}

func Render(bundle Bundle) ([]byte, error) {
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(bundle, "", "  ")
}
