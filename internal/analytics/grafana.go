package analytics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// grafanaURL is where the local Grafana instance listens.
const grafanaURL = "http://localhost:3000"

// Grafana is a minimal client for the HTTP API calls the bash version made
// with curl.
type Grafana struct {
	BaseURL  string
	User     string
	Password string
	Client   *http.Client
}

// NewGrafana builds a client with basic-auth credentials.
func NewGrafana(user, password string) *Grafana {
	return &Grafana{BaseURL: grafanaURL, User: user, Password: password, Client: &http.Client{Timeout: 30 * time.Second}}
}

func (g *Grafana) do(method, path string, body []byte) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, g.BaseURL+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(g.User, g.Password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

// Ready reports whether Grafana answers on its base URL.
func (g *Grafana) Ready() bool {
	resp, err := g.Client.Get(g.BaseURL)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode < 500
}

// WaitReady polls until Grafana responds or the timeout elapses.
func (g *Grafana) WaitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if g.Ready() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("Grafana did not start in time")
		}
		time.Sleep(time.Second)
	}
}

// DatasourceExists checks /api/datasources/name/<name>.
func (g *Grafana) DatasourceExists(name string) (bool, error) {
	code, _, err := g.do(http.MethodGet, "/api/datasources/name/"+url.PathEscape(name), nil)
	if err != nil {
		return false, err
	}
	return code == http.StatusOK, nil
}

// CreateDatasource posts a datasource definition.
func (g *Grafana) CreateDatasource(def map[string]any) error {
	body, err := json.Marshal(def)
	if err != nil {
		return err
	}
	code, data, err := g.do(http.MethodPost, "/api/datasources", body)
	if err != nil {
		return err
	}
	if code < 200 || code > 299 {
		return fmt.Errorf("create datasource: HTTP %d: %s", code, bytes.TrimSpace(data))
	}
	return nil
}

// DashboardExists searches dashboards by title and reports a non-empty result.
func (g *Grafana) DashboardExists(title string) (bool, error) {
	code, data, err := g.do(http.MethodGet, "/api/search?query="+url.QueryEscape(title), nil)
	if err != nil {
		return false, err
	}
	if code != http.StatusOK {
		return false, fmt.Errorf("search dashboards: HTTP %d", code)
	}
	var results []json.RawMessage
	if err := json.Unmarshal(data, &results); err != nil {
		return false, err
	}
	return len(results) > 0, nil
}

// DBPayload wraps a dashboard for POST /api/dashboards/db.
func DBPayload(dashboard []byte) ([]byte, error) {
	if !json.Valid(dashboard) {
		return nil, errors.New("dashboard is not valid JSON")
	}
	return json.Marshal(map[string]any{
		"dashboard": json.RawMessage(dashboard),
		"folderId":  0,
		"overwrite": true,
	})
}

// ImportPayload wraps a dashboard that declares the Infinity datasource input
// for POST /api/dashboards/import.
func ImportPayload(dashboard []byte) ([]byte, error) {
	if !json.Valid(dashboard) {
		return nil, errors.New("dashboard is not valid JSON")
	}
	return json.Marshal(map[string]any{
		"dashboard": json.RawMessage(dashboard),
		"folderId":  0,
		"overwrite": true,
		"inputs": []map[string]string{{
			"name":     "DS_YESOREYERAM-INFINITY-DATASOURCE",
			"type":     "datasource",
			"pluginId": infinityPlugin,
			"value":    infinityPlugin,
		}},
	})
}

// DashboardTitle extracts the "title" field of a dashboard definition.
func DashboardTitle(dashboard []byte) string {
	var d struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(dashboard, &d)
	return d.Title
}

// PostDashboard sends a prepared payload to /api/dashboards/db.
func (g *Grafana) PostDashboard(payload []byte) error {
	return g.post("/api/dashboards/db", payload)
}

// ImportDashboard sends a prepared payload to /api/dashboards/import.
func (g *Grafana) ImportDashboard(payload []byte) error {
	return g.post("/api/dashboards/import", payload)
}

func (g *Grafana) post(path string, payload []byte) error {
	code, data, err := g.do(http.MethodPost, path, payload)
	if err != nil {
		return err
	}
	if code < 200 || code > 299 {
		return fmt.Errorf("%s: HTTP %d: %s", path, code, bytes.TrimSpace(data))
	}
	return nil
}
