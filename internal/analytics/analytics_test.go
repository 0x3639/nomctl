package analytics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hypercore-one/nomctl/dashboards"
)

func TestEmbeddedDashboard(t *testing.T) {
	data, err := dashboards.FS.ReadFile(dashboards.Node)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatal("embedded dashboard is not valid JSON")
	}
	if title := DashboardTitle(data); title != "znnd" {
		t.Errorf("title = %q", title)
	}
	payload, err := ImportPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Dashboard map[string]any   `json:"dashboard"`
		FolderID  int              `json:"folderId"`
		Overwrite bool             `json:"overwrite"`
		Inputs    []map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	if !p.Overwrite || p.FolderID != 0 || len(p.Inputs) != 1 || p.Inputs[0]["pluginId"] != infinityPlugin || p.Dashboard["title"] != "znnd" {
		t.Errorf("payload unexpected: %+v", p)
	}
	if _, err := DBPayload([]byte("{not json")); err == nil {
		t.Error("invalid JSON must be rejected")
	}
}

func TestNeedsNodeScrapeJob(t *testing.T) {
	if !NeedsNodeScrapeJob("scrape_configs:\n  - job_name: \"prometheus\"\n") {
		t.Error("should need node job")
	}
	if NeedsNodeScrapeJob("  - job_name: \"node\"\n") {
		t.Error("should not need node job")
	}
}

func TestGrafanaClient(t *testing.T) {
	var created []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "admin" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/datasources/name/Prometheus":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/datasources/name/missing":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/api/datasources" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			created = append(created, body["name"].(string))
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/search":
			if r.URL.Query().Get("query") == "znnd" {
				_, _ = w.Write([]byte(`[{"title":"znnd"}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case r.URL.Path == "/api/dashboards/import":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()

	g := NewGrafana("admin", "secret")
	g.BaseURL = srv.URL
	if !g.Ready() {
		t.Error("server should be ready")
	}
	if ok, err := g.DatasourceExists("Prometheus"); err != nil || !ok {
		t.Errorf("Prometheus datasource should exist: %v %v", ok, err)
	}
	if ok, err := g.DatasourceExists("missing"); err != nil || ok {
		t.Errorf("missing datasource: %v %v", ok, err)
	}
	if err := g.CreateDatasource(map[string]any{"name": "x"}); err != nil || len(created) != 1 {
		t.Errorf("create: %v %v", err, created)
	}
	if ok, _ := g.DashboardExists("znnd"); !ok {
		t.Error("znnd dashboard should exist")
	}
	if ok, _ := g.DashboardExists("nope"); ok {
		t.Error("nope dashboard should not exist")
	}
	if err := g.ImportDashboard([]byte(`{}`)); err != nil {
		t.Error(err)
	}
	if err := g.PostDashboard([]byte(`{}`)); err == nil {
		t.Error("teapot status should be an error")
	}
}
