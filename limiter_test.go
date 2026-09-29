package main

import (
	"testing"
	"time"
)

// fakeClock is a clock the test moves by hand and a sleep that only records
// what it was asked to wait for. A limiter test should not take real time to
// run, and this is what makes that possible.
//
// l.now and l.sleep are given f.Now and f.Sleep, which are method values and so
// keep working after the test moves the clock. A plain closure over a variable
// the test also reassigns would quietly stop being what l.now points at.
type fakeClock struct {
	now   time.Time
	slept time.Duration
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (f *fakeClock) Now() time.Time { return f.now }

func (f *fakeClock) Sleep(d time.Duration) { f.slept += d }

func (f *fakeClock) Advance(d time.Duration) { f.now = f.now.Add(d) }

// testLimiter builds a limiter wired to a fake clock, so a test can run an hour
// of waiting without an hour of waiting.
func testLimiter(t *testing.T, capacity int, refill time.Duration) (*burstLimiter, *fakeClock) {
	t.Helper()
	l := newBurstLimiter(capacity, refill)
	c := newFakeClock(time.Unix(1000, 0))
	l.now, l.sleep = c.Now, c.Sleep
	return l, c
}

func TestBurstLimiterLetsABurstStraightThrough(t *testing.T) {
	l, c := testLimiter(t, 3, time.Second)

	// A person searching three times in a row should not be made to wait. This
	// is the whole reason for a bucket rather than a sleep on every request.
	for range 3 {
		l.wait()
	}

	if c.slept != 0 {
		t.Fatalf("a burst of 3 waited %v, want no wait at all", c.slept)
	}
}

func TestBurstLimiterWaitsOnceEmpty(t *testing.T) {
	l, c := testLimiter(t, 2, 5*time.Second)

	l.wait()
	l.wait()
	l.wait() // nothing left, so this one waits

	if c.slept != 5*time.Second {
		t.Fatalf("waited %v past the end of the bucket, want one refill (5s)", c.slept)
	}
}

func TestBurstLimiterRefillsOverTime(t *testing.T) {
	l, c := testLimiter(t, 1, 4*time.Second)

	l.wait() // spends the only token
	l.wait() // has to wait for it back

	if c.slept != 4*time.Second {
		t.Fatalf("waited %v, want one refill (4s)", c.slept)
	}

	// Ten seconds later the bucket has earned a token back, so ordinary
	// listening must not be slowed down at all.
	c.slept = 0
	c.Advance(10 * time.Second)
	l.wait()

	if c.slept != 0 {
		t.Fatalf("waited %v after the bucket refilled, want no wait", c.slept)
	}
}

func TestBurstLimiterQueuesCallersBehindEachOther(t *testing.T) {
	l, c := testLimiter(t, 1, time.Second)

	l.wait() // spends the token
	l.wait() // waits, and moves the bucket's idea of now forward by a second
	l.wait() // still nothing, so this one queues up behind the last

	// Two waits, not one. Callers must not all decide at the same moment that
	// there is nothing left and then all proceed at once.
	if c.slept != 2*time.Second {
		t.Fatalf("waited %v across three requests on a bucket of one, want 2s", c.slept)
	}
}

func TestBurstLimiterNeverExceedsItsCapacity(t *testing.T) {
	l, c := testLimiter(t, 2, time.Second)

	// A long idle stretch earns far more than the bucket can hold. It must not
	// be allowed to bank the difference, or one quiet afternoon would buy a
	// huge burst later.
	c.Advance(time.Hour)
	l.wait()
	l.wait()

	// An hour is 3600 tokens' worth, so an uncapped bucket would still have
	// plenty here and this third request would not wait at all. Waiting proves
	// the surplus was thrown away rather than kept.
	c.slept = 0
	l.wait()

	if c.slept != time.Second {
		t.Fatalf("waited %v after an hour idle, want the bucket capped at 2 (1s)", c.slept)
	}
}
