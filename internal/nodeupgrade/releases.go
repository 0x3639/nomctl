// Package nodeupgrade installs signed go-zenon releases without building source.
package nodeupgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	Repository         = "zenon-network/go-zenon-releases"
	SigningFingerprint = "310A0EAEA8449754CF17E8BBF6B82D1155879DAB"
	keyURL             = "https://github.com/0x3639.gpg"
	checksumName       = "SHA256CHECKSUMS.txt"
	maxArchiveSize     = 200 << 20
)

type Asset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Release struct {
	Tag         string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Assets      []Asset   `json:"assets"`
}

// Client talks only to the fixed release repository. The token is never logged.
type Client struct {
	http  *http.Client
	token string
}

func NewClient(token string) *Client {
	return &Client{token: token, http: &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many download redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing non-HTTPS download redirect")
			}
			// Strip even for github.com subdomains: signed asset URLs need no token.
			req.Header.Del("Authorization")
			return nil
		},
	}}
}

// List returns all non-draft releases in publication order, newest first.
func (c *Client) List(ctx context.Context) ([]Release, error) {
	slog.Info("Listing available releases from " + Repository + "...")
	var releases []Release
	for page := 1; page <= 100; page++ {
		url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", Repository, page)
		res, err := c.get(ctx, url, "application/vnd.github+json")
		if err != nil {
			return nil, err
		}
		var batch []Release
		err = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&batch)
		_ = res.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read releases: %w", err)
		}
		for _, r := range batch {
			if !r.Draft && r.Tag != "" {
				releases = append(releases, r)
			}
		}
		if len(batch) < 100 {
			sort.SliceStable(releases, func(i, j int) bool { return releases[i].PublishedAt.After(releases[j].PublishedAt) })
			if len(releases) == 0 {
				return nil, fmt.Errorf("no published releases found in %s", Repository)
			}
			return releases, nil
		}
	}
	return nil, fmt.Errorf("too many releases in %s", Repository)
}

// Select defaults to the most recently published release when tag is empty.
func Select(releases []Release, tag string) (Release, error) {
	if len(releases) == 0 {
		return Release{}, fmt.Errorf("no published releases available")
	}
	if tag == "" {
		return releases[0], nil
	}
	for _, r := range releases {
		if r.Tag == tag {
			return r, nil
		}
	}
	return Release{}, fmt.Errorf("published release %q not found in %s", tag, Repository)
}

func (r Release) asset(name string) (Asset, error) {
	var found Asset
	for _, a := range r.Assets {
		if a.Name != name {
			continue
		}
		if found.ID != 0 || a.ID <= 0 {
			return Asset{}, fmt.Errorf("invalid or duplicate asset %s in %s", name, r.Tag)
		}
		found = a
	}
	if found.ID == 0 {
		return Asset{}, fmt.Errorf("release %s is missing %s", r.Tag, name)
	}
	return found, nil
}

func (c *Client) get(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "nomctl")
	if req.URL.Host == "api.github.com" {
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		if res.StatusCode == 401 || res.StatusCode == 403 || res.StatusCode == 404 {
			return nil, fmt.Errorf("GitHub HTTP %d: check NOMCTL_GITHUB_TOKEN has Contents read access to %s (required while private), and check API rate limits", res.StatusCode, Repository)
		}
		return nil, fmt.Errorf("download failed: HTTP %d", res.StatusCode)
	}
	return res, nil
}

func (c *Client) download(ctx context.Context, url, path string, limit int64) error {
	res, err := c.get(ctx, url, "application/octet-stream")
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(res.Body, limit+1))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("%s exceeds download size limit", filepath.Base(path))
	}
	return closeErr
}
