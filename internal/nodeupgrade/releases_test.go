package nodeupgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string, code int) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestListPaginationPublicationOrderAndSelection(t *testing.T) {
	client := NewClient("private-test-token")
	calls := 0
	client.http.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Authorization") != "Bearer private-test-token" {
			t.Fatal("private API request has no token")
		}
		if req.URL.Path != "/repos/"+Repository+"/releases" || req.URL.Query().Get("page") != fmt.Sprint(calls) {
			t.Fatal(req.URL)
		}
		batch := []Release{{Tag: "new", PublishedAt: time.Unix(500, 0), Prerelease: true}, {Tag: "draft", Draft: true, PublishedAt: time.Unix(600, 0)}}
		if calls == 1 {
			batch = nil
			for i := 0; i < 100; i++ {
				batch = append(batch, Release{Tag: fmt.Sprint(i), PublishedAt: time.Unix(int64(i), 0)})
			}
		}
		data, _ := json.Marshal(batch)
		return response(string(data), 200), nil
	})
	releases, err := client.List(context.Background())
	if err != nil || len(releases) != 101 || releases[0].Tag != "new" || calls != 2 {
		t.Fatalf("got %d releases, %d calls: %v", len(releases), calls, err)
	}
	for _, tag := range []string{"", "27"} {
		r, err := Select(releases, tag)
		if err != nil || (tag == "" && r.Tag != "new") || (tag != "" && r.Tag != tag) {
			t.Fatalf("selected %+v: %v", r, err)
		}
	}
	if _, err := Select(releases, "draft"); err == nil {
		t.Fatal("selected draft")
	}
}

func TestPublicAccessAndActionableErrors(t *testing.T) {
	for _, code := range []int{200, 401, 403, 404, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			client := NewClient("")
			client.http.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Authorization") != "" {
					t.Fatal("anonymous request carries auth")
				}
				return response("[]", code), nil
			})
			_, err := client.List(context.Background())
			if err == nil {
				t.Fatal("empty/error response succeeded")
			}
			if code == 401 || code == 403 || code == 404 {
				if !strings.Contains(err.Error(), "NOMCTL_GITHUB_TOKEN") {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCredentialsNeverReachRedirectOrSigningKey(t *testing.T) {
	client := NewClient("secret")
	client.http.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "api.github.com" {
			if req.Header.Get("Authorization") != "Bearer secret" {
				t.Fatal("missing API authorization")
			}
			res := response("", 302)
			res.Header.Set("Location", "https://assets.github.com/file")
			return res, nil
		}
		if req.Header.Get("Authorization") != "" {
			t.Fatalf("credential leaked to %s", req.URL.Host)
		}
		return response("content", 200), nil
	})
	for _, url := range []string{"https://api.github.com/repos/" + Repository + "/releases/assets/1", keyURL} {
		res, err := client.get(context.Background(), url, "application/octet-stream")
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
	}
}

func TestMissingDuplicateAssetsAndPlatform(t *testing.T) {
	for _, assets := range [][]Asset{nil, {{ID: 1, Name: "x"}, {ID: 2, Name: "x"}}, {{Name: "x"}}} {
		if _, err := (Release{Assets: assets}).asset("x"); err == nil {
			t.Fatal("invalid assets accepted")
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if name, err := archiveName("linux", arch); err != nil || name != "znnd-linux-"+arch+".tar.gz" {
			t.Fatal(name, err)
		}
	}
	if _, err := archiveName("darwin", "arm64"); err == nil {
		t.Fatal("unsupported OS accepted")
	}
	if _, err := archiveName("linux", "386"); err == nil {
		t.Fatal("unsupported arch accepted")
	}
}

func TestDownloadLimit(t *testing.T) {
	client := NewClient("")
	client.http.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return response("too large", 200), nil })
	if err := client.download(context.Background(), keyURL, t.TempDir()+"/key", 3); err == nil {
		t.Fatal("oversized download accepted")
	}
}
