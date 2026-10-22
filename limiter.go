package main

// Paces requests to YouTube without getting in the way of normal listening.

import (
	"sync"
	"time"
)

// A bucket holding a handful of requests that refills slowly. A burst of ten
// requests is what makes YouTube answer "Sign in to confirm you're not a bot".
type burstLimiter struct {
	// Guards the whole bucket; wait is called from background commands.
	mu sync.Mutex

	// How many requests can be made right now, and as many as it will ever hold. A
	// float because time earns a fraction of a request at a time.
	tokens   float64
	capacity float64

	// How long it takes to earn one whole request.
	refill time.Duration

	// When the bucket was last looked at. Set into the future while a caller is
	// waiting, which is how waiting callers queue up behind each other.
	last time.Time

	// Swapped out in the tests, so one can run a minute of waiting without a
	// minute of waiting.
	now   func() time.Time
	sleep func(time.Duration)
}

// Lets capacity requests straight through, then earns one more every refill until
// it is back to full.
func newBurstLimiter(capacity int, refill time.Duration) *burstLimiter {
	return &burstLimiter{
		tokens:   float64(capacity),
		capacity: float64(capacity),
		refill:   refill,
		now:      time.Now,
		sleep:    time.Sleep,
	}
}

// Blocks until a request may be made.
func (l *burstLimiter) wait() {
	l.mu.Lock()

	// Credit whatever has been earned since the last look. A negative elapsed means
	// a previous caller is already waiting for this instant, and the queue below
	// handles it.
	now := l.now()
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens += float64(elapsed) / float64(l.refill)
		if l.tokens > l.capacity {
			l.tokens = l.capacity
		}
	}
	l.last = now

	if l.tokens >= 1 {
		l.tokens--
		l.mu.Unlock()
		return
	}

	// Empty, so this caller has to wait. last jumps forward by the length of the
	// wait, which is what makes the next caller wait behind this one rather than
	// alongside it.
	missed := 1 - l.tokens
	shortfall := time.Duration(missed * float64(l.refill))
	l.last = now.Add(shortfall)
	l.tokens = 0
	l.mu.Unlock()

	l.sleep(shortfall)
}

// Shared by every request the program makes to YouTube, so a search and a stream
// resolution spend from the same budget.
var youtubeLimiter = newBurstLimiter(10, 5*time.Second)
