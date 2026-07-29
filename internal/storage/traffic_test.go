package storage_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zoefix/openfrp/internal/storage"
	"github.com/zoefix/openfrp/internal/storage/repo"
)

func TestDailyTrafficAccumulates(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Skipf("SQLite unavailable here: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	traffic := repo.NewTraffic(db.DB)

	if err := traffic.Add(ctx, "2026-07-29", "web", 100, 200, 2); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := traffic.Add(ctx, "2026-07-29", "web", 50, 25, 1); err != nil {
		t.Fatalf("add again: %v", err)
	}

	rows, err := traffic.Recent(ctx, 3650)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	var found bool
	for _, r := range rows {
		if r.Day == "2026-07-29" && r.Tunnel == "web" {
			found = true
			if r.BytesIn != 150 || r.BytesOut != 225 || r.Connections != 3 {
				t.Errorf("totals = %d/%d/%d, want 150/225/3 — the second write "+
					"must add to the day rather than replace it",
					r.BytesIn, r.BytesOut, r.Connections)
			}
		}
	}
	if !found {
		t.Error("the recorded day was not returned")
	}

	if err := traffic.Add(ctx, "2026-07-29", "web", 0, 0, 0); err != nil {
		t.Errorf("an empty delta should be a no-op, got %v", err)
	}
}
