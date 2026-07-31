package auth

import (
	"sync"
	"time"
)

// limiter is the per-IP token bucket of arch §8.2: [LoginBurst] tokens,
// refilling one per [LoginRate].
//
// It is a bucket rather than a counter-per-window so that a client that has
// exhausted its budget recovers gradually instead of getting five fresh
// attempts on the stroke of the minute.
//
// Memory is bounded by eviction, not by a cap on distinct addresses: entries
// idle for longer than it takes to refill a full bucket are indistinguishable
// from an address never seen, so dropping them changes no answer. A sweep runs
// at most once per [LoginRate], amortised over calls, which keeps a flood from
// a rotating source address from growing the map without bound.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	burst   float64
	rate    time.Duration
	now     func() time.Time
	swept   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(burst int, rate time.Duration, now func() time.Time) *limiter {
	return &limiter{
		buckets: make(map[string]*bucket),
		burst:   float64(burst),
		rate:    rate,
		now:     now,
	}
}

// allow spends one token for `key`. When the bucket is empty it reports the
// wait until the next token, which becomes the `Retry-After` of the 429.
func (l *limiter) allow(key string) (retryAfter time.Duration, ok bool) {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepLocked(now)

	b, seen := l.buckets[key]
	if !seen {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		if refill := now.Sub(b.last); refill > 0 {
			b.tokens += float64(refill) / float64(l.rate)
			if b.tokens > l.burst {
				b.tokens = l.burst
			}
		}
		b.last = now
	}

	if b.tokens < 1 {
		missing := 1 - b.tokens
		wait := time.Duration(missing * float64(l.rate))
		if wait < time.Second {
			wait = time.Second
		}
		return wait, false
	}
	b.tokens--
	return 0, true
}

// sweepLocked drops buckets that have been idle long enough to have refilled
// completely. Such a bucket answers identically to an absent one, so removing
// it is free.
func (l *limiter) sweepLocked(now time.Time) {
	if now.Sub(l.swept) < l.rate {
		return
	}
	l.swept = now
	idle := time.Duration(l.burst) * l.rate
	for k, b := range l.buckets {
		if now.Sub(b.last) >= idle {
			delete(l.buckets, k)
		}
	}
}

// size reports how many addresses are being tracked. It exists for the test
// that pins the eviction property.
func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
