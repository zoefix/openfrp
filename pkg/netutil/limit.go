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

	// parent is a wider limit this one also draws from — a tunnel's rate
	// nested inside the rate for the whole client. Both have to allow a
	// chunk before it moves, which is what makes the outer figure a real
	// ceiling rather than a suggestion each tunnel ignores separately.
	parent *Limiter

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

// Under binds this limiter beneath a wider one and returns whichever should
// be used.
//
// Called once, where the limiter is built — never per connection. Copying a
// limiter to attach a parent would give every connection its own bucket, and
// a per-tunnel rate would quietly become a per-connection rate: ten visitors
// at a megabyte each rather than a megabyte between them.
//
// Either may be nil. A tunnel with no rate of its own still answers to the
// client-wide limit, and a tunnel with one is unaffected when there is no
// wider limit to sit under.
func (l *Limiter) Under(parent *Limiter) *Limiter {
	if l == nil {
		return parent
	}
	l.parent = parent
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
	chunk := min(l.burst(), limiterMaxChunk)
	if l.parent != nil {
		chunk = min(chunk, l.parent.chunk())
	}
	return chunk
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

	l.parent.wait(n)
}
