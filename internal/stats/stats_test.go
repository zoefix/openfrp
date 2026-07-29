package stats

import (
	"sync"
	"testing"
)

func TestRecordTransferAccumulates(t *testing.T) {
	r := NewRegistry()

	r.RecordTransfer("web", 100, 200, true)
	r.RecordTransfer("web", 50, 25, true)
	r.RecordTransfer("web", 10, 5, false)

	got := r.Snapshot("web")
	if got.BytesIn != 160 || got.BytesOut != 230 {
		t.Errorf("bytes = %d/%d, want 160/230", got.BytesIn, got.BytesOut)
	}
	if got.Connections != 3 {
		t.Errorf("connections = %d, want 3", got.Connections)
	}
	if got.Spliced != 2 || got.Buffered != 1 {
		t.Errorf("spliced/buffered = %d/%d, want 2/1", got.Spliced, got.Buffered)
	}
	if got.LastSeen.IsZero() {
		t.Error("LastSeen was not recorded")
	}
}

func TestSplicedFractionIsTheHealthSignal(t *testing.T) {
	r := NewRegistry()

	for range 9 {
		r.RecordTransfer("t", 1, 1, true)
	}
	r.RecordTransfer("t", 1, 1, false)

	if got := r.Snapshot("t").SplicedFraction(); got < 0.89 || got > 0.91 {
		t.Errorf("SplicedFraction = %.2f, want ~0.90", got)
	}

	if got := r.Snapshot("idle").SplicedFraction(); got != 0 {
		t.Errorf("SplicedFraction with no traffic = %v, want 0", got)
	}
}

func TestActiveNeverReportsNegative(t *testing.T) {
	r := NewRegistry()

	r.Close("t")
	r.Close("t")

	if got := r.Snapshot("t").Active; got != 0 {
		t.Errorf("Active = %d, want it clamped to 0", got)
	}

	r.Open("t")
	r.Open("t")
	r.Close("t")
	if got := r.Snapshot("t").Active; got < 0 {
		t.Errorf("Active = %d, must never be negative", got)
	}
}

func TestConcurrentRecordingIsRaceFree(t *testing.T) {
	r := NewRegistry()

	const workers, each = 16, 200
	var wg sync.WaitGroup

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				r.Open("shared")
				r.RecordTransfer("shared", 1, 2, true)
				r.Close("shared")
			}
		}()
	}
	wg.Wait()

	got := r.Snapshot("shared")
	if want := int64(workers * each); got.Connections != want {
		t.Errorf("connections = %d, want %d", got.Connections, want)
	}
	if got.BytesIn != int64(workers*each) {
		t.Errorf("bytes in = %d, want %d", got.BytesIn, workers*each)
	}
	if got.Active != 0 {
		t.Errorf("active = %d, want 0 after every connection closed", got.Active)
	}
}

func TestTotalAndOrdering(t *testing.T) {
	r := NewRegistry()
	r.RecordTransfer("zebra", 1, 1, true)
	r.RecordTransfer("alpha", 2, 3, false)

	all := r.All()
	if len(all) != 2 {
		t.Fatalf("got %d tunnels, want 2", len(all))
	}
	if all[0].Name != "alpha" {
		t.Errorf("results are not sorted: %s came first", all[0].Name)
	}

	total := r.Total()
	if total.BytesIn != 3 || total.BytesOut != 4 || total.Connections != 2 {
		t.Errorf("total = %+v", total)
	}
	if total.Spliced != 1 || total.Buffered != 1 {
		t.Errorf("total splice split = %d/%d, want 1/1", total.Spliced, total.Buffered)
	}
}

func TestForgetRemovesATunnel(t *testing.T) {
	r := NewRegistry()
	r.RecordTransfer("gone", 1, 1, true)

	r.Forget("gone")
	if len(r.All()) != 0 {
		t.Error("the tunnel survived Forget")
	}

	if got := r.Snapshot("gone"); got.Connections != 0 {
		t.Errorf("recreated counters are not empty: %+v", got)
	}
}
