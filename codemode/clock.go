package codemode

import (
	"context"
	"sync"
	"time"
)

// A run clock measures a session run's active time. The run timeout and the
// detach deadline are measured on it, and an operation handler can pause it
// while it waits on something outside the program's control, such as a
// human approving the call.
type runClock struct {
	mu      sync.Mutex
	paused  int
	acc     time.Duration
	since   time.Time
	changed chan struct{}
}

type clockKey struct{}

func newRunClock() *runClock {
	return &runClock{since: time.Now(), changed: make(chan struct{})}
}

func (c *runClock) elapsedLocked() time.Duration {
	if c.paused > 0 {
		return c.acc
	}
	return c.acc + time.Since(c.since)
}

func (c *runClock) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *runClock) pause() func() {
	c.mu.Lock()
	if c.paused == 0 {
		c.acc += time.Since(c.since)
	}
	c.paused++
	c.signalLocked()
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.paused--
			if c.paused == 0 {
				c.since = time.Now()
			}
			c.signalLocked()
			c.mu.Unlock()
		})
	}
}

// after returns a channel closed once the clock has run for d (paused time
// not counted), or never if ctx ends first.
func (c *runClock) after(ctx context.Context, d time.Duration) <-chan struct{} {
	out := make(chan struct{})
	go func() {
		for {
			c.mu.Lock()
			rem := d - c.elapsedLocked()
			paused := c.paused > 0
			changed := c.changed
			c.mu.Unlock()
			if !paused && rem <= 0 {
				close(out)
				return
			}
			var tick <-chan time.Time
			var t *time.Timer
			if !paused {
				t = time.NewTimer(rem)
				tick = t.C
			}
			select {
			case <-tick:
			case <-changed:
			case <-ctx.Done():
				if t != nil {
					t.Stop()
				}
				return
			}
			if t != nil {
				t.Stop()
			}
		}
	}()
	return out
}

// PauseClock stops a session run's clock (its timeout and detach deadline)
// until the returned resume function is called, for an operation handler
// that waits on a human or another slow outside party. Pauses from parallel
// handlers nest: the clock runs again when every one has resumed. Outside a
// session run it does nothing. Always call resume (defer it).
func PauseClock(ctx context.Context) (resume func()) {
	if c, ok := ctx.Value(clockKey{}).(*runClock); ok {
		return c.pause()
	}
	return func() {}
}
