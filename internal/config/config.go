package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Log struct {
	Level string `json:"level,omitempty"`

	Format string `json:"format,omitempty"`
}

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
