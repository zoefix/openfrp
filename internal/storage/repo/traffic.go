package repo

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DayFormat is how a calendar day is stored and asked for.
const DayFormat = "2006-01-02"

// DailyTraffic is one tunnel's total for one day.
type DailyTraffic struct {
	Day         string
	Tunnel      string
	BytesIn     int64
	BytesOut    int64
	Connections int64
}

type Traffic struct {
	db *sql.DB
}

func NewTraffic(db *sql.DB) *Traffic { return &Traffic{db: db} }

// Add folds a period's counters into the running total for a day.
//
// Deltas rather than absolutes, because the caller reports what has happened
// since it last spoke. Absolutes would be wrong across a restart: the
// in-memory counters begin again at zero, and a day that had carried a
// gigabyte would be recorded as having carried whatever arrived after the
// restart.
func (r *Traffic) Add(ctx context.Context, day, tunnel string, in, out, conns int64) error {
	if in == 0 && out == 0 && conns == 0 {
		return nil
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO traffic_daily (day, tunnel, bytes_in, bytes_out, connections, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (day, tunnel) DO UPDATE SET
			bytes_in    = bytes_in    + excluded.bytes_in,
			bytes_out   = bytes_out   + excluded.bytes_out,
			connections = connections + excluded.connections,
			updated_at  = excluded.updated_at`,
		day, tunnel, in, out, conns, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("repo: record traffic for %s on %s: %w", tunnel, day, err)
	}
	return nil
}

// Recent returns the last n days, newest first, every tunnel.
func (r *Traffic) Recent(ctx context.Context, days int) ([]DailyTraffic, error) {
	if days < 1 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days).Format(DayFormat)

	rows, err := r.db.QueryContext(ctx, `
		SELECT day, tunnel, bytes_in, bytes_out, connections
		FROM traffic_daily
		WHERE day >= ?
		ORDER BY day DESC, tunnel ASC`, since)
	if err != nil {
		return nil, fmt.Errorf("repo: list traffic: %w", err)
	}
	defer rows.Close()

	var out []DailyTraffic
	for rows.Next() {
		var t DailyTraffic
		if err := rows.Scan(&t.Day, &t.Tunnel, &t.BytesIn, &t.BytesOut, &t.Connections); err != nil {
			return nil, fmt.Errorf("repo: scan traffic: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Totals sums a period into one row per tunnel.
func (r *Traffic) Totals(ctx context.Context, days int) ([]DailyTraffic, error) {
	if days < 1 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days).Format(DayFormat)

	rows, err := r.db.QueryContext(ctx, `
		SELECT tunnel, SUM(bytes_in), SUM(bytes_out), SUM(connections)
		FROM traffic_daily
		WHERE day >= ?
		GROUP BY tunnel
		ORDER BY SUM(bytes_in) + SUM(bytes_out) DESC`, since)
	if err != nil {
		return nil, fmt.Errorf("repo: total traffic: %w", err)
	}
	defer rows.Close()

	var out []DailyTraffic
	for rows.Next() {
		var t DailyTraffic
		if err := rows.Scan(&t.Tunnel, &t.BytesIn, &t.BytesOut, &t.Connections); err != nil {
			return nil, fmt.Errorf("repo: scan totals: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Prune drops days older than the retention window, so a router that has been
// up for years does not accumulate rows nobody will read.
func (r *Traffic) Prune(ctx context.Context, keepDays int) error {
	if keepDays < 1 {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format(DayFormat)

	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM traffic_daily WHERE day < ?`, cutoff); err != nil {
		return fmt.Errorf("repo: prune traffic: %w", err)
	}
	return nil
}
