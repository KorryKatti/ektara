package main

// Paces requests to YouTube without getting in the way of normal listening.

import (
	"sync"
	"time"
)

// The limit that matters is not how much can be asked for in an hour, it is how
// fast a burst can go. Ten requests in one second is what makes YouTube answer
// "Sign in to confirm you're not a bot", and enough of those flags an address for
// a good while.
//
// So this is a bucket holding a handful of requests that refills slowly. A search
// is normally followed by minutes of music, so ordinary use never comes near
// emptying it and never waits. Holding enter down on a search box does empty it,
// and from then on requests are spaced out until it fills again.
//
// A fixed sleep on every request would have been simpler, and would have made the
// first search of a session as slow as the fiftieth.
type burstLimiter struct {
	// Guards the whole bucket. wait is called from background commands, so more
	// than one can be in here at a time.
	mu sync.Mutex

	// How many requests can be made right now, and as many as it will ever hold. A
	// float because time earns a fraction of a request at a time.
	tokens   float64
	capacity float64

	// How long it takes to earn one whole request.
	refill time.Duration

	// When the bucket was last looked at. Set slightly into the future when a
	// caller has to wait, which is how waiting callers queue up behind each other
	// rather than all deciding at once that there is nothing left.
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

	// credit whatever has been earned since the last look. A negative elapsed means
	// a previous caller is already waiting for this instant, in which case there is
	// nothing to credit and the queue below handles it.
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
// resolution spend from the same budget rather than each getting their own.
//
// The numbers are deliberately loose. Ten straight requests covers anything a
// person does by hand, and five seconds to earn one more is the spacing yt-dlp's
// own guide suggests between downloads. Someone who needs more is not listening to
// music, they are scraping, and YouTube will stop them whatever this says.
var youtubeLimiter = newBurstLimiter(10, 5*time.Second)
