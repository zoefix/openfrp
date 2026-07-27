package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zoefix/openfrp/internal/stats"
)

// DefaultStatsPath is where the daemon publishes its traffic snapshot.
//
// A file on tmpfs rather than a socket or an API: the reader is a ucode script
// invoked per rpcd call, which wants to open something and parse it, not
// negotiate a connection. It is RAM only, so polling it costs no flash wear.
const DefaultStatsPath = "/var/run/openfrp/stats.json"

// statsWriteInterval is how often the snapshot is republished.
//
// The status page polls every five seconds and derives rates from the
// difference between two readings, so publishing faster than it reads buys
// nothing. Publishing much slower would make a short transfer invisible.
const statsWriteInterval = 2 * time.Second

// Traffic returns the live counters, for callers that would rather read them
// directly than through the published file.
func (c *Client) Traffic() *stats.Registry { return c.traffic }

// SetStatsPath changes where the snapshot is published. Empty disables it.
func (c *Client) SetStatsPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statsPath = path
}

// TrafficSnapshot is the published document.
type TrafficSnapshot struct {
	// UpdatedAt lets a reader tell a live daemon from a stale file left behind
	// by one that died, and lets the UI compute rates over the real elapsed
	// time rather than assuming its polling interval was honoured.
	UpdatedAt int64 `json:"updated_at"`
	Uptime    int64 `json:"uptime_seconds"`

	// Tunnels is keyed by tunnel name.
	Tunnels map[string]TrafficCounters `json:"tunnels"`
	Total   TrafficCounters            `json:"total"`
}

// TrafficCounters is one tunnel's totals since the daemon started.
//
// Cumulative, never rates. A rate computed here would be an average over
// whatever interval this happened to run on; computed by the reader from two
// snapshots it is an average over exactly the period the reader cares about.
type TrafficCounters struct {
	// BytesIn is traffic arriving from the internet, BytesOut what the local
	// service sent back. Named from the tunnel's point of view so that "in"
	// means the same thing on both ends of the connection.
	BytesIn     int64 `json:"bytes_in"`
	BytesOut    int64 `json:"bytes_out"`
	Connections int64 `json:"connections"`
	Active      int64 `json:"active"`
	Spliced     int64 `json:"spliced"`
	Buffered    int64 `json:"buffered"`
}

// publishTraffic writes the snapshot until ctx is cancelled.
func (c *Client) publishTraffic(ctx context.Context) {
	path := c.currentStatsPath()
	if path == "" {
		return
	}

	ticker := time.NewTicker(statsWriteInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// One last write so the file reflects the final totals rather than
			// whatever was current two seconds before shutdown.
			c.writeTraffic(path)
			return
		case <-ticker.C:
			c.writeTraffic(path)
		}
	}
}

// writeTraffic publishes the current counters.
//
// Written to a temporary file and renamed, because the reader is a separate
// process polling it: without the rename a reader can catch a half-written
// document and report a parse error where it should report traffic.
func (c *Client) writeTraffic(path string) {
	snapshot := TrafficSnapshot{
		UpdatedAt: time.Now().Unix(),
		Uptime:    int64(c.traffic.Uptime().Seconds()),
		Tunnels:   map[string]TrafficCounters{},
		Total:     countersOf(c.traffic.Total()),
	}

	for _, tunnel := range c.traffic.All() {
		snapshot.Tunnels[tunnel.Name] = countersOf(tunnel)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			c.logger.Debug("create stats directory", "error", err)
			return
		}
	}

	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o644); err != nil {
		c.logger.Debug("write traffic snapshot", "error", err)
		return
	}
	if err := os.Rename(temporary, path); err != nil {
		c.logger.Debug("publish traffic snapshot", "error", err)
		os.Remove(temporary)
	}
}

func countersOf(snapshot stats.Snapshot) TrafficCounters {
	// Active is decremented on close and can dip below zero during a shutdown
	// race, which would render as a negative connection count.
	active := snapshot.Active
	if active < 0 {
		active = 0
	}

	return TrafficCounters{
		BytesIn:     snapshot.BytesIn,
		BytesOut:    snapshot.BytesOut,
		Connections: snapshot.Connections,
		Active:      active,
		Spliced:     snapshot.Spliced,
		Buffered:    snapshot.Buffered,
	}
}

func (c *Client) currentStatsPath() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statsPath
}

// removeTraffic deletes the published snapshot, so a stopped daemon does not
// leave numbers behind that look current.
func (c *Client) removeTraffic() error {
	path := c.currentStatsPath()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("client: remove %s: %w", path, err)
	}
	return nil
}
