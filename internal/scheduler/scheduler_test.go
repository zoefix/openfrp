package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zoefix/openfrp/pkg/log"
)

func TestSchedulerRunsJobsRepeatedly(t *testing.T) {
	var runs atomic.Int32

	s := New(log.Discard())
	s.Add(JobFunc{
		JobName: "counter",
		Every:   20 * time.Millisecond,
		Function: func(context.Context) error {
			runs.Add(1)
			return nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	if got := runs.Load(); got < 2 {
		t.Errorf("job ran %d times, expected repeated execution", got)
	}
}

func TestPanicInOneJobDoesNotStopTheRest(t *testing.T) {
	var (
		panics   atomic.Int32
		survivor atomic.Int32
	)

	s := New(log.Discard())
	s.Add(JobFunc{
		JobName: "exploding",
		Every:   20 * time.Millisecond,
		Function: func(context.Context) error {
			panics.Add(1)
			panic("boom")
		},
	})
	s.Add(JobFunc{
		JobName: "survivor",
		Every:   20 * time.Millisecond,
		Function: func(context.Context) error {
			survivor.Add(1)
			return nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	if panics.Load() < 2 {
		t.Errorf("the panicking job ran %d times; its loop should survive a panic",
			panics.Load())
	}
	if survivor.Load() < 2 {
		t.Errorf("the other job ran %d times; it should be unaffected",
			survivor.Load())
	}
}

func TestJobErrorsAreLoggedNotFatal(t *testing.T) {
	var runs atomic.Int32

	s := New(log.Discard())
	s.Add(JobFunc{
		JobName: "failing",
		Every:   20 * time.Millisecond,
		Function: func(context.Context) error {
			runs.Add(1)
			return errors.New("expected failure")
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	if runs.Load() < 2 {
		t.Errorf("job ran %d times; an error should not stop the loop", runs.Load())
	}
}

func TestSlowJobDoesNotOverlapItself(t *testing.T) {
	var (
		concurrent atomic.Int32
		maxSeen    atomic.Int32
	)

	s := New(log.Discard())
	s.Add(JobFunc{
		JobName: "slow",
		Every:   10 * time.Millisecond,
		Function: func(context.Context) error {
			active := concurrent.Add(1)
			for {
				peak := maxSeen.Load()
				if active <= peak || maxSeen.CompareAndSwap(peak, active) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			concurrent.Add(-1)
			return nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	if maxSeen.Load() > 1 {
		t.Errorf("saw %d concurrent runs of one job; runs must not overlap",
			maxSeen.Load())
	}
}

func TestCancellationStopsPromptly(t *testing.T) {
	s := New(log.Discard())
	s.Add(JobFunc{
		JobName:  "idle",
		Every:    time.Hour,
		Function: func(context.Context) error { return nil },
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly after cancellation")
	}
}

func TestJobWithoutIntervalIsSkipped(t *testing.T) {
	var runs atomic.Int32

	s := New(log.Discard())
	s.Add(JobFunc{
		JobName: "no-interval",
		Function: func(context.Context) error {
			runs.Add(1)
			return nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	if runs.Load() != 0 {
		t.Errorf("a job with no interval ran %d times", runs.Load())
	}
}

func TestJitterStaysInRange(t *testing.T) {
	const d = 100 * time.Millisecond
	for range 200 {
		got := jitter(d)
		if got < d/2 || got > d {
			t.Fatalf("jitter(%s) = %s, outside [%s, %s]", d, got, d/2, d)
		}
	}
	if jitter(0) <= 0 {
		t.Error("jitter(0) must still return a usable delay")
	}
}
