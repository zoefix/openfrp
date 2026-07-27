// Package scheduler runs the periodic work: certificate renewal, expiry
// notices, and anything else that has to happen without a person present.
//
// It is a small hand-rolled ticker rather than a cron library. The jobs here
// are few, their cadence is measured in hours, and the one property that
// actually matters — that a job which panics or overruns cannot take down the
// daemon or stack up against itself — is easier to guarantee directly than to
// verify through a dependency.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"
)

// Job is one unit of periodic work.
type Job interface {
	// Name identifies the job in logs.
	Name() string
	// Interval is how often it should run.
	Interval() time.Duration
	// Run performs the work. Returning an error is logged, not fatal.
	Run(ctx context.Context) error
}

// Scheduler runs jobs on their own cadence.
type Scheduler struct {
	logger *slog.Logger

	mu   sync.Mutex
	jobs []Job

	// now is injectable for tests.
	now func() time.Time
}

// New returns a scheduler.
func New(logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{logger: logger, now: time.Now}
}

// Add registers a job. Jobs must be added before Run.
func (s *Scheduler) Add(job Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, job)
}

// Run executes every job on its interval until ctx is cancelled.
//
// Each job gets its own goroutine and its own ticker, so a slow one delays
// only itself. Run blocks until every job has stopped.
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.Lock()
	jobs := append([]Job(nil), s.jobs...)
	s.mu.Unlock()

	if len(jobs) == 0 {
		s.logger.Debug("scheduler has no jobs")
		return
	}

	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runJob(ctx, job)
		}()
	}

	s.logger.Info("scheduler started", "jobs", len(jobs))
	wg.Wait()
	s.logger.Info("scheduler stopped")
}

// runJob loops one job on its interval.
func (s *Scheduler) runJob(ctx context.Context, job Job) {
	interval := job.Interval()
	if interval <= 0 {
		s.logger.Warn("job has no interval, skipping", "job", job.Name())
		return
	}

	// Stagger the first run. Without this every job fires at startup, which on
	// a router means several API calls the instant the network comes up — the
	// least reliable moment there is.
	initial := jitter(interval / 10)
	timer := time.NewTimer(initial)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		s.invoke(ctx, job)

		// Reset from the end of the run, not the start, so a job that takes
		// longer than its interval cannot overlap with itself.
		timer.Reset(interval)
	}
}

// invoke runs a job once, containing any panic.
//
// A panic in a renewal job must not take the tunnel daemon down with it. The
// tunnels are the product; the scheduler is a convenience.
func (s *Scheduler) invoke(ctx context.Context, job Job) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("job panicked",
				"job", job.Name(),
				"panic", fmt.Sprint(recovered),
				"stack", string(debug.Stack()))
		}
	}()

	started := s.now()
	if err := job.Run(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		s.logger.Error("job failed", "job", job.Name(), "error", err)
		return
	}

	s.logger.Debug("job completed",
		"job", job.Name(), "took", s.now().Sub(started).Round(time.Millisecond))
}

// jitter returns a duration uniformly in [d/2, d], so a fleet of routers does
// not synchronise on the same instant.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Second
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// JobFunc adapts a function into a Job.
type JobFunc struct {
	JobName  string
	Every    time.Duration
	Function func(ctx context.Context) error
}

func (j JobFunc) Name() string            { return j.JobName }
func (j JobFunc) Interval() time.Duration { return j.Every }
func (j JobFunc) Run(ctx context.Context) error {
	return j.Function(ctx)
}

var _ Job = JobFunc{}
