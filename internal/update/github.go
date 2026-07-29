package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

const (
	DefaultRepo = "zoefix/openfrp"

	apiTimeout = 20 * time.Second

	maxAPIResponse = 4 << 20
)

type Release struct {
	Tag        string    `json:"tag_name"`
	Name       string    `json:"name"`
	Notes      string    `json:"body"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	HTMLURL    string    `json:"html_url"`
	Assets     []Asset   `json:"assets"`
}

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// AssetFor finds the bundle built for this machine.
//
// Matching on the exact name the release workflow writes rather than on a
// substring: "arm" appears inside "arm64", and installing the wrong
// architecture leaves a router with a binary it cannot execute.
func (r Release) AssetFor(goos, goarch string) (Asset, bool) {
	want := BundleName(r.Tag, goos, goarch)
	for _, asset := range r.Assets {
		if asset.Name == want {
			return asset, true
		}
	}
	return Asset{}, false
}

func (r Release) Checksums() (Asset, bool) {
	for _, asset := range r.Assets {
		if asset.Name == ChecksumsName {
			return asset, true
		}
	}
	return Asset{}, false
}

const ChecksumsName = "checksums.txt"

func BundleName(tag, goos, goarch string) string {
	return fmt.Sprintf("openfrp-%s-%s-%s.tar.gz", strings.TrimPrefix(tag, "v"), goos, goarch)
}

type Client struct {
	Repo string

	BaseURL string

	HTTP *http.Client

	GOOS   string
	GOARCH string
}

const DefaultBaseURL = "https://api.github.com"

func NewClient() *Client {
	return &Client{
		Repo:    DefaultRepo,
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: apiTimeout},
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
	}
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimSuffix(c.BaseURL, "/")
}

func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "openfrp-updater")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: reach %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNoRelease
	}
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return nil, fmt.Errorf("update: GitHub is rate limiting this address; " +
			"try again later")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: %s returned %s", url, resp.Status)
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
}

var ErrNoRelease = fmt.Errorf("update: the repository has published no releases yet")

// Latest returns the newest published release, skipping drafts and
// prereleases.
//
// The releases list rather than /releases/latest, because that endpoint 404s
// on a repository whose only releases are prereleases, which reads as "no
// repository" rather than "nothing to offer you".
func (c *Client) Latest(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=20", c.base(), c.Repo)

	body, err := c.get(ctx, url)
	if err != nil {
		return Release{}, err
	}

	var releases []Release
	if err := json.Unmarshal(body, &releases); err != nil {
		return Release{}, fmt.Errorf("update: read the release list: %w", err)
	}

	var best Release
	var bestVersion Version
	found := false

	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		version, err := ParseVersion(release.Tag)
		if err != nil {
			continue
		}
		if !found || version.NewerThan(bestVersion) {
			best, bestVersion, found = release, version, true
		}
	}

	if !found {
		return Release{}, ErrNoRelease
	}
	return best, nil
}
