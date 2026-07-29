package config

import (
	"encoding/json"
	"fmt"
	"time"
)

type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	switch v := raw.(type) {
	case string:
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", v, err)
		}
		*d = Duration(parsed)
	case float64:
		*d = Duration(time.Duration(v) * time.Second)
	default:
		return fmt.Errorf("invalid duration %v: want a string like \"20s\" or a number of seconds", raw)
	}
	return nil
}
