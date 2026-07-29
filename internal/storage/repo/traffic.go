package repo

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const DayFormat = "2006-01-02"

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
