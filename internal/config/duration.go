package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration that round-trips through JSON as a human string
// such as "20s" or "1m30s". Plain numbers are also accepted and read as
// seconds, because the UCI-to-JSON renderer emits bare integers for options a
// user types as a number in the LuCI form.
type Duration time.Duration

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// String implements fmt.Stringer.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON encodes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts either a duration string or a number of seconds.
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
