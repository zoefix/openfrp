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

// TrafficHistory folds the live counters into daily totals on disk.
//
// The counters in memory answer "what is happening now" and die with the
// process. This answers "what did this tunnel carry last Tuesday", which is
// the question a quota argument or a bandwidth bill turns on, and it has to
// survive a restart to be worth anything.
//
// Deltas are written rather than absolutes. The counters begin again at zero
// when the daemon restarts, so writing absolutes would record a day that had
// carried a gigabyte as having carried whatever arrived after the restart.
type TrafficHistory struct {
	traffic *stats.Registry
	store   *repo.Traffic
	logger  *slog.Logger

	// last is what was already written, per tunnel, so the next write can
	// send the difference.
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

// Run records until ctx is cancelled, and once more on the way out so the
// last few minutes are not lost to a clean shutdown.
func (h *TrafficHistory) Run(ctx context.Context) {
	ticker := time.NewTicker(historyInterval)
	defer ticker.Stop()

	prune := time.NewTicker(24 * time.Hour)
	defer prune.Stop()

	for {
		select {
		case <-ctx.Done():
			// A cancelled context cannot be used for the write itself.
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

// record writes what has accumulated since the last call.
func (h *TrafficHistory) record(ctx context.Context) {
	day := time.Now().Format(repo.DayFormat)

	for _, current := range h.traffic.All() {
		previous := h.last[current.Name]

		in := current.BytesIn - previous.BytesIn
		out := current.BytesOut - previous.BytesOut
		conns := current.Connections - previous.Connections

		// A restart resets the counters, so a negative delta means the totals
		// began again rather than that traffic was undone. What arrived
		// before this process started belongs to whatever wrote it.
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
