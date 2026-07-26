// Package config defines the on-disk configuration for both daemons.
//
// The format is JSON. On OpenWrt the init script renders UCI into
// /var/etc/openfrp.json and hands that path to the daemon, so this package
// never reads UCI itself — it only has to understand one format.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Log configures process logging. It is embedded in both daemon configs.
type Log struct {
	// Level is debug, info, warn or error. Empty means info.
	Level string `json:"level,omitempty"`
	// Format is text or json. Empty means text.
	Format string `json:"format,omitempty"`
}

// loadJSON reads path and decodes it into dst, rejecting unknown fields so a
// typo in a config key fails loudly instead of being silently ignored.
func loadJSON(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return nil
}
