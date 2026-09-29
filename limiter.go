package main

import (
	"sync"
	"time"
)

// burstLimiter paces requests to YouTube without getting in the way of normal
// listening.
//
// The limit that actually matters is not how much can be asked for in an hour.
// It is how fast a burst can go. Ten requests in one second is what makes
// YouTube start answering "Sign in to confirm you're not a bot", and enough of
// those is enough to get an address flagged for a good while.
//
// So this is a bucket holding a handful of requests that refills slowly. A
// search is normally followed by minutes of music, so ordinary use never comes
// near emptying it and never waits. Holding enter down on a search box does
// empty it, and from then on requests are spaced out until it fills again.
//
// A fixed sleep on every request would have been simpler, and would have made
// the first search of a session as slow as the fiftieth.
type burstLimiter struct {
	// mu guards the whole bucket. Wait is called from background commands, so
	// more than one can be in here at a time.
	mu sync.Mutex

	// tokens is how many requests can be made right now, and capacity is as
	// many as it will ever hold. It is a float because time earns a fraction
	// of a request at a time.
	tokens   float64
	capacity float64

	// refill is how long it takes to earn one whole request.
	refill time.Duration

	// last is when the bucket was last looked at. It is set slightly into the
	// future when a caller has to wait, which is how the waiting callers queue
	// up behind each other rather than all deciding at once that there is
	// nothing left.
	last time.Time

	// now and sleep are swapped out in the tests, so that a test can run a
	// minute of waiting without a minute of waiting.
	now   func() time.Time
	sleep func(time.Duration)
}

// newBurstLimiter makes a limiter that lets capacity requests straight through,
// then earns one more every refill until it is back to full.
func newBurstLimiter(capacity int, refill time.Duration) *burstLimiter {
	return &burstLimiter{
		tokens:   float64(capacity),
		capacity: float64(capacity),
		refill:   refill,
		now:      time.Now,
		sleep:    time.Sleep,
	}
}

// wait blocks until a request may be made.
func (l *burstLimiter) wait() {
	l.mu.Lock()

	// credit whatever has been earned since the last look. A negative elapsed
	// means a previous caller is already waiting for this instant, in which
	// case there is nothing to credit and the queue below handles it.
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

	// Empty, so this caller has to wait. last jumps forward by the length of
	// the wait, which is what makes the next caller wait behind this one
	// instead of alongside it.
	missed := 1 - l.tokens
	shortfall := time.Duration(missed * float64(l.refill))
	l.last = now.Add(shortfall)
	l.tokens = 0
	l.mu.Unlock()

	l.sleep(shortfall)
}

// youtubeLimiter is shared by every request the program makes to YouTube, so a
// search and a stream resolution spend from the same budget rather than each
// getting their own.
//
// The numbers are deliberately loose. Ten straight requests covers anything a
// person does by hand, and five seconds to earn one more is the spacing
// yt-dlp's own guide suggests between downloads. Someone who needs more than
// that is not listening to music, they are scraping, and YouTube will stop them
// for it whatever this says.
var youtubeLimiter = newBurstLimiter(10, 5*time.Second)
