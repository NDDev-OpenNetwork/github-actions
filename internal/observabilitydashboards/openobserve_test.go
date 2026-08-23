package observabilitydashboards

import "testing"

func TestRenderOpenObserveV8IsDeterministicAndManaged(t *testing.T) {
	bundle, err := Load(configPath(t))
	if err != nil {
		t.Fatal(err)
	}
	dashboards, err := RenderOpenObserve(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboards) != 6 {
		t.Fatalf("dashboards=%d", len(dashboards))
	}
	for _, dashboard := range dashboards {
		if dashboard.Version != 8 || len(dashboard.Tabs) != 1 || dashboard.Tabs[0].TabID == "" || len(dashboard.Tabs[0].Panels) != 4 {
			t.Fatalf("invalid dashboard %#v", dashboard)
		}
		if dashboard.Description[:len(managedDescriptionPrefix)] != managedDescriptionPrefix {
			t.Fatalf("dashboard %q is not managed", dashboard.Title)
		}
		for index, panel := range dashboard.Tabs[0].Panels {
			if panel.QueryType != "promql" || len(panel.Queries) != 1 || panel.Queries[0].Fields.StreamType != "metrics" || panel.Queries[0].Fields.Stream == "" {
				t.Fatalf("invalid panel %#v", panel)
			}
			if panel.Layout.I != index+1 || panel.Layout.W != 96 || panel.Layout.H != 9 {
				t.Fatalf("invalid layout %#v", panel.Layout)
			}
		}
	}
}
