package netutil

import (
	"sync"
	"time"
)

const (
	limiterWindow = 100 * time.Millisecond

	limiterMinChunk = 4 << 10

	limiterMaxChunk = 1 << 20
)

type Limiter struct {
	rate int64

	mu      sync.Mutex
	tokens  int64
	updated time.Time
}

func NewLimiter(bytesPerSecond int64) *Limiter {
	if bytesPerSecond <= 0 {
		return nil
	}
	l := &Limiter{rate: bytesPerSecond, updated: time.Now()}
	l.tokens = l.burst()
	return l
}

func (l *Limiter) Rate() int64 {
	if l == nil {
		return 0
	}
	return l.rate
}

func (l *Limiter) burst() int64 {
	return max(l.rate*int64(limiterWindow)/int64(time.Second), limiterMinChunk)
}

func (l *Limiter) chunk() int64 {
	return min(l.burst(), limiterMaxChunk)
}

func (l *Limiter) wait(n int64) {
	if l == nil || n <= 0 {
		return
	}

	l.mu.Lock()
	now := time.Now()
	burst := l.burst()

	l.tokens = min(l.tokens+int64(now.Sub(l.updated))*l.rate/int64(time.Second), burst)
	l.updated = now
	l.tokens -= n
	owed := l.tokens
	l.mu.Unlock()

	if owed < 0 {
		time.Sleep(time.Duration(-owed) * time.Second / time.Duration(l.rate))
	}

}
