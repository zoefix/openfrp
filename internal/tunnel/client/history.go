package client

import (
	"context"
	"log/slog"
	"time"

	"github.com/zoefix/openfrp/internal/stats"
	"github.com/zoefix/openfrp/internal/storage/repo"
)

const (
	historyInterval = 5 * time.Minute

	historyRetentionDays = 400
)

type TrafficHistory struct {
	traffic *stats.Registry
	store   *repo.Traffic
	logger  *slog.Logger

	last map[string]stats.Snapshot
}

func NewTrafficHistory(traffic *stats.Registry, store *repo.Traffic, logger *slog.Logger) *TrafficHistory {
	return &TrafficHistory{
		traffic: traffic,
		store:   store,
		logger:  logger,
		last:    map[string]stats.Snapshot{},
	}
}

func (h *TrafficHistory) Run(ctx context.Context) {
	ticker := time.NewTicker(historyInterval)
	defer ticker.Stop()

	prune := time.NewTicker(24 * time.Hour)
	defer prune.Stop()

	for {
		select {
		case <-ctx.Done():

			flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			h.record(flush)
			cancel()
			return

		case <-ticker.C:
			h.record(ctx)

		case <-prune.C:
			if err := h.store.Prune(ctx, historyRetentionDays); err != nil {
				h.logger.Debug("prune traffic history", "error", err)
			}
		}
	}
}

func (h *TrafficHistory) record(ctx context.Context) {
	day := time.Now().Format(repo.DayFormat)

	for _, current := range h.traffic.All() {
		previous := h.last[current.Name]

		in := current.BytesIn - previous.BytesIn
		out := current.BytesOut - previous.BytesOut
		conns := current.Connections - previous.Connections

		if in < 0 || out < 0 || conns < 0 {
			in, out, conns = current.BytesIn, current.BytesOut, current.Connections
		}

		if err := h.store.Add(ctx, day, current.Name, in, out, conns); err != nil {
			h.logger.Debug("record traffic history", "tunnel", current.Name, "error", err)
			continue
		}
		h.last[current.Name] = current
	}
}
