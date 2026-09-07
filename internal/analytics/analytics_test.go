package analytics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/0x3639/nomctl/dashboards"
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
	if _, err := ImportPayload(data, ""); err == nil {
		t.Error("empty datasource uid must be rejected")
	}
	payload, err := ImportPayload(data, "abc123")
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
	if !p.Overwrite || p.FolderID != 0 || len(p.Inputs) != 1 || p.Inputs[0]["pluginId"] != infinityPlugin || p.Inputs[0]["value"] != "abc123" || p.Dashboard["title"] != "znnd" {
		t.Errorf("payload unexpected: %+v", p)
	}
	if _, err := DBPayload([]byte("{not json")); err == nil {
		t.Error("invalid JSON must be rejected")
	}
}

func TestGrafanaClient(t *testing.T) {
	var created []string
	var passwordChanged string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			// The login page needs no auth; "down" simulates a 503.
			if r.URL.RawQuery == "down" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		u, p, ok := r.BasicAuth()
		if r.URL.Path == "/api/user/password" && r.Method == http.MethodPut {
			// Only the initial password may change itself, as in Grafana.
			if !ok || u != "admin" || p != "admin" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["oldPassword"] != "admin" || body["newPassword"] == "" || body["newPassword"] != body["confirmNew"] {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			passwordChanged = body["newPassword"]
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/api/user" {
			if ok && u == "admin" && (p == "secret" || (passwordChanged != "" && p == passwordChanged)) {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusUnauthorized)
			}
			return
		}
		if !ok || u != "admin" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/datasources/name/Prometheus":
			_, _ = w.Write([]byte(`{"uid":"prom-uid","name":"Prometheus"}`))
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
		case r.URL.Path == "/api/plugins/"+infinityPlugin+"/settings":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/plugins/other/settings":
			w.WriteHeader(http.StatusNotFound)
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
	down := NewGrafana("admin", "secret")
	down.BaseURL = srv.URL + "/?down"
	if down.Ready() {
		t.Error("5xx must not count as ready")
	}
	wrongCreds := NewGrafana("admin", "bad")
	wrongCreds.BaseURL = srv.URL
	if err := wrongCreds.CreateDatasource(map[string]any{"name": "x"}); err == nil {
		t.Error("401 must be an error")
	}
	if ok, err := g.DatasourceExists("Prometheus"); err != nil || !ok {
		t.Errorf("Prometheus datasource should exist: %v %v", ok, err)
	}
	if ok, err := g.DatasourceExists("missing"); err != nil || ok {
		t.Errorf("missing datasource: %v %v", ok, err)
	}
	if uid, err := g.DatasourceUID("Prometheus"); err != nil || uid != "prom-uid" {
		t.Errorf("DatasourceUID = %q, %v", uid, err)
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
	if ok, err := g.PluginLoaded(infinityPlugin); err != nil || !ok {
		t.Errorf("plugin should be loaded: %v %v", ok, err)
	}
	if ok, err := g.PluginLoaded("other"); err != nil || ok {
		t.Errorf("plugin should not be loaded: %v %v", ok, err)
	}
	if err := g.ImportDashboard([]byte(`{}`)); err != nil {
		t.Error(err)
	}
	if err := g.PostDashboard([]byte(`{}`)); err == nil {
		t.Error("teapot status should be an error")
	}

	// Password application: configured password rejected, default accepted,
	// change succeeds, configured password now works.
	if ok, err := g.Authenticated(); err != nil || !ok {
		t.Errorf("configured creds should authenticate: %v %v", ok, err)
	}
	initial := NewGrafana("admin", "admin")
	initial.BaseURL = srv.URL
	if ok, _ := initial.Authenticated(); ok {
		t.Error("default password must not authenticate before it is set")
	}
	if err := initial.ChangePassword("admin", "newpass"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if passwordChanged != "newpass" {
		t.Errorf("password not applied: %q", passwordChanged)
	}
	changed := NewGrafana("admin", "newpass")
	changed.BaseURL = srv.URL
	if ok, _ := changed.Authenticated(); !ok {
		t.Error("new password should authenticate")
	}
	if err := changed.ChangePassword("wrong", "x"); err == nil {
		t.Error("wrong old password must fail")
	}
}

func TestClientURL(t *testing.T) {
	cases := map[string]string{"127.0.0.1": "http://127.0.0.1:3000", "0.0.0.0": "http://127.0.0.1:3000", "": "http://127.0.0.1:3000",
		"::": "http://[::1]:3000", "10.0.0.5": "http://10.0.0.5:3000", "fd00::5": "http://[fd00::5]:3000"}
	for in, want := range cases {
		if got := ClientURL(in); got != want {
			t.Errorf("ClientURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGrafanaDropIn(t *testing.T) {
	if got := GrafanaDropIn("127.0.0.1"); got != "[Service]\nEnvironment=GF_SERVER_HTTP_ADDR=127.0.0.1\n" {
		t.Errorf("drop-in = %q", got)
	}
}

func TestAppendManagedRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "prometheus.yml")
	if err := os.WriteFile(target, []byte("global:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AppendManaged(target, "scrape:\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "global:\nscrape:\n" {
		t.Errorf("content = %q", got)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", info.Mode())
	}
	// A symlink planted where the config should be must not redirect the write.
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.yml")
	if err := os.Symlink(sentinel, link); err != nil {
		t.Fatal(err)
	}
	if err := AppendManaged(link, "evil\n"); err == nil {
		t.Fatal("symlink accepted")
	}
	if got, _ := os.ReadFile(sentinel); string(got) != "keep" {
		t.Errorf("sentinel modified: %q", got)
	}
	if err := AppendManaged(filepath.Join(dir, "missing.yml"), "x"); err == nil {
		t.Error("missing file accepted")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 3 { // yml, sentinel, link: nothing temporary
		t.Errorf("temp file left behind: %v", entries)
	}
}
