package oidc

import (
	"crypto/sha256"
	"crypto/subtle"
	"sync"
	"time"
)

// Pre-auth limits of ruling E-74 §2.5. They bound the memory an
// unauthenticated client can make the server hold.
const (
	pendingTTL     = 10 * time.Minute
	pendingMax     = 1000
	startsPerIP    = 20
	startWindow    = time.Minute
	bucketsMax     = 4096
	loggedOutGrace = 10 * time.Minute
)

// pending is one login in flight, bound to the browser that started it by
// the hash of its pre-auth cookie.
type pending struct {
	loginHash   [32]byte
	verifier    string
	nonce       string
	returnTo    string
	reauth      bool
	sessionHash string // reauth only: the session the login must come back to
	promptNone  bool
	fellBack    bool
	expires     time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

type pendingStore struct {
	mu      sync.Mutex
	entries map[string]*pending
	buckets map[string]*bucket
	now     func() time.Time
}

func newPendingStore(now func() time.Time) *pendingStore {
	return &pendingStore{entries: map[string]*pending{}, buckets: map[string]*bucket{}, now: now}
}

// admit spends one start from ip's bucket; ok is false with the wait until
// the next one when the bucket is empty or the store is full.
func (p *pendingStore) admit(ip string) (wait time.Duration, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.sweepLocked(now)
	if len(p.entries) >= pendingMax {
		return pendingTTL / 10, false
	}
	rate := float64(startsPerIP) / startWindow.Seconds()
	b := p.buckets[ip]
	if b == nil {
		if len(p.buckets) >= bucketsMax {
			p.buckets = map[string]*bucket{}
		}
		b = &bucket{tokens: startsPerIP, last: now}
		p.buckets[ip] = b
	}
	b.tokens = min(float64(startsPerIP), b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return time.Duration((1 - b.tokens) / rate * float64(time.Second)), false
	}
	b.tokens--
	return 0, true
}

// put stores e unless the store is full; admit's check runs before the
// discovery fetch, so concurrent starts can all pass it and the cap is
// enforced again here, under the same lock as the insert.
func (p *pendingStore) put(state string, e *pending) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if len(p.entries) >= pendingMax {
		p.sweepLocked(now)
		if len(p.entries) >= pendingMax {
			return false
		}
	}
	e.expires = now.Add(pendingTTL)
	p.entries[state] = e
	return true
}

func (p *pendingStore) sweep() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepLocked(p.now())
}

// take returns and removes the entry for state only when loginID is the
// pre-auth cookie of the browser that started it. A wrong cookie leaves the
// entry in place, so a forged callback cannot spend someone else's login.
func (p *pendingStore) take(state, loginID string) (*pending, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[state]
	if !ok || state == "" || loginID == "" {
		return nil, false
	}
	if p.now().After(e.expires) {
		delete(p.entries, state)
		return nil, false
	}
	h := sha256.Sum256([]byte(loginID))
	if subtle.ConstantTimeCompare(h[:], e.loginHash[:]) != 1 {
		return nil, false
	}
	delete(p.entries, state)
	return e, true
}

func (p *pendingStore) sweepLocked(now time.Time) {
	for k, e := range p.entries {
		if now.After(e.expires) {
			delete(p.entries, k)
		}
	}
}
