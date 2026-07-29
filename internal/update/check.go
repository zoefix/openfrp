package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zoefix/openfrp/internal/version"
)

const (
	DefaultCachePath = "/var/run/openfrp/update.json"

	CacheTTL = 6 * time.Hour
)

type Status struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest,omitempty"`
	Available bool      `json:"available"`
	Notes     string    `json:"notes,omitempty"`
	Published time.Time `json:"published,omitempty"`
	URL       string    `json:"url,omitempty"`
	Asset     string    `json:"asset,omitempty"`
	Size      int64     `json:"size,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

func (s Status) Stale() bool { return time.Since(s.CheckedAt) > CacheTTL }

func Check(ctx context.Context, c *Client) Status {
	status := Status{
		Current:   version.Short(),
		CheckedAt: time.Now(),
	}

	running, err := ParseVersion(version.Short())
	if err != nil {
		status.Error = fmt.Sprintf("the running version %q cannot be compared", version.Short())
		return status
	}

	release, err := c.Latest(ctx)
	if err != nil {
		if errors.Is(err, ErrNoRelease) {
			return status
		}
		status.Error = err.Error()
		return status
	}

	latest, err := ParseVersion(release.Tag)
	if err != nil {
		status.Error = err.Error()
		return status
	}

	status.Latest = latest.String()
	status.Notes = release.Notes
	status.Published = release.Published
	status.URL = release.HTMLURL

	if !latest.NewerThan(running) {
		return status
	}

	// An upgrade nobody can install is not an upgrade. Offering it would put a
	// badge on the page that leads to a failure at download time, on whichever
	// architecture the release happened to skip.
	asset, ok := release.AssetFor(c.GOOS, c.GOARCH)
	if !ok {
		status.Error = fmt.Sprintf("%s has no build for %s/%s",
			release.Tag, c.GOOS, c.GOARCH)
		return status
	}

	status.Available = true
	status.Asset = asset.Name
	status.Size = asset.Size
	return status
}

func ReadCache(path string) (Status, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.Unmarshal(raw, &status); err != nil {
		return Status{}, fmt.Errorf("update: read cached check: %w", err)
	}
	return status, nil
}

func WriteCache(path string, status Status) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
