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

type Job interface {
	Name() string

	Interval() time.Duration

	Run(ctx context.Context) error
}

type Scheduler struct {
	logger *slog.Logger

	mu   sync.Mutex
	jobs []Job

	now func() time.Time
}

func New(logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{logger: logger, now: time.Now}
}

func (s *Scheduler) Add(job Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, job)
}

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

func (s *Scheduler) runJob(ctx context.Context, job Job) {
	interval := job.Interval()
	if interval <= 0 {
		s.logger.Warn("job has no interval, skipping", "job", job.Name())
		return
	}

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

		timer.Reset(interval)
	}
}

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

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Second
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

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
