package fake

import (
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Clock is a fake clock for testing.
type Clock struct {
	now time.Time
}

// NewClock creates a new fake clock.
func NewClock() *Clock {
	return &Clock{now: time.Now()}
}

// SetNow sets the current time.
func (c *Clock) SetNow(t time.Time) {
	c.now = t
}

// Now returns the current time.
func (c *Clock) Now() time.Time {
	return c.now
}

// NewTimer creates a new timer.
func (c *Clock) NewTimer(duration time.Duration) model.Timer {
	return &timerImpl{
		duration: duration,
		clock:    c,
	}
}

// timerImpl is the concrete implementation of Timer.
type timerImpl struct {
	duration time.Duration
	clock    *Clock
	fired    bool
	ch       chan time.Time
}

// C returns the timer channel.
func (t *timerImpl) C() <-chan time.Time {
	if t.fired {
		return nil
	}
	if t.ch == nil {
		t.ch = make(chan time.Time, 1)
		go func() {
			t.clock.now = t.clock.now.Add(t.duration)
			t.fired = true
			t.ch <- t.clock.now
			close(t.ch)
		}()
	}
	return t.ch
}

// Stop stops the timer.
func (t *timerImpl) Stop() bool {
	t.fired = true
	return true
}

// Reset resets the timer.
func (t *timerImpl) Reset(duration time.Duration) bool {
	t.duration = duration
	t.fired = false
	t.ch = nil
	return true
}
