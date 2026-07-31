package auth

import (
	"testing"
	"time"
)

// The bucket's arithmetic, tested directly: five tokens to spend at once, one
// new token every twelve seconds, and a wait that is honest about when the next
// one arrives.
func TestLimiter_burstThenRefill(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	l := newLimiter(LoginBurst, LoginRate, func() time.Time { return now })

	for i := range LoginBurst {
		if _, ok := l.allow("client"); !ok {
			t.Fatalf("attempt %d was refused inside the burst", i+1)
		}
	}
	wait, ok := l.allow("client")
	if ok {
		t.Fatal("the bucket allowed one attempt more than the burst")
	}
	if wait <= 0 || wait > LoginRate {
		t.Errorf("retry-after = %v, want a positive wait no longer than %v", wait, LoginRate)
	}

	now = now.Add(LoginRate)
	if _, ok := l.allow("client"); !ok {
		t.Error("a token did not refill after one rate interval")
	}
	if _, ok := l.allow("client"); ok {
		t.Error("more than one token refilled in one interval")
	}
}

// A bucket idle long enough to have refilled completely answers identically to
// an absent one, so it is dropped. That is what bounds the map against a flood
// from a rotating source address.
func TestLimiter_evictsFullyRefilledBuckets(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	l := newLimiter(LoginBurst, LoginRate, func() time.Time { return now })

	for i := range 50 {
		l.allow(string(rune('a' + i%26)))
	}
	if l.size() == 0 {
		t.Fatal("no buckets were tracked at all")
	}

	// Long enough for every bucket to be back at full.
	now = now.Add(time.Duration(LoginBurst) * LoginRate * 2)
	l.allow("trigger-the-sweep")

	if got := l.size(); got != 1 {
		t.Errorf("tracked buckets = %d, want only the one just used", got)
	}
}

// The bucket never exceeds its burst, however long a client stays away.
func TestLimiter_doesNotAccumulatePastTheBurst(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	l := newLimiter(LoginBurst, LoginRate, func() time.Time { return now })

	l.allow("client")
	now = now.Add(time.Hour)

	for i := range LoginBurst {
		if _, ok := l.allow("client"); !ok {
			t.Fatalf("attempt %d after a long idle was refused", i+1)
		}
	}
	if _, ok := l.allow("client"); ok {
		t.Error("an hour of idling banked more than the burst")
	}
}
