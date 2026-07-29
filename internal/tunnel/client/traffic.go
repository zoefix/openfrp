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

const DefaultStatsPath = "/var/run/openfrp/stats.json"

const statsWriteInterval = 2 * time.Second

func (c *Client) Traffic() *stats.Registry { return c.traffic }

func (c *Client) SetStatsPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statsPath = path
}

type TrafficSnapshot struct {
	UpdatedAt int64 `json:"updated_at"`
	Uptime    int64 `json:"uptime_seconds"`

	ClientVersion string `json:"client_version,omitempty"`

	Servers map[string]ServerSnapshot `json:"servers,omitempty"`

	Tunnels map[string]TrafficCounters `json:"tunnels"`
	Total   TrafficCounters            `json:"total"`
}

type ServerSnapshot struct {
	Connected bool `json:"connected"`

	Version string `json:"version,omitempty"`
}

type TrafficCounters struct {
	BytesIn     int64 `json:"bytes_in"`
	BytesOut    int64 `json:"bytes_out"`
	Connections int64 `json:"connections"`
	Active      int64 `json:"active"`
	Spliced     int64 `json:"spliced"`
	Buffered    int64 `json:"buffered"`
}

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

			c.writeTraffic(path)
			return
		case <-ticker.C:
			c.writeTraffic(path)
		}
	}
}

func (c *Client) writeTraffic(path string) {
	snapshot := TrafficSnapshot{
		UpdatedAt:     time.Now().Unix(),
		Uptime:        int64(c.traffic.Uptime().Seconds()),
		ClientVersion: c.version,
		Tunnels:       map[string]TrafficCounters{},
		Total:         countersOf(c.traffic.Total()),
	}

	if c.serverStates != nil {
		snapshot.Servers = c.serverStates()
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
