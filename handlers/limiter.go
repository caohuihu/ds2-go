package handlers

import (
	"context"
	"sync"
	"time"
)

// AccountLimiter enforces per-account request rate and concurrency to reduce
// DeepSeek web anti-fraud detection risk.
//
// Two independent controls:
//   - QPM (requests per minute): sliding-window token bucket, one token per request.
//   - MaxConcurrent: semaphore limiting in-flight requests at any moment.
type AccountLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*accountBucket
	defaultQPM int
}

type accountBucket struct {
	mu         sync.Mutex
	tokens     int
	defaultQPM int
	resetAt    time.Time
	sem        chan struct{} // in-flight concurrency semaphore
}

func NewAccountLimiter(defaultQPM int) *AccountLimiter {
	return &AccountLimiter{
		buckets:    make(map[string]*accountBucket),
		defaultQPM: defaultQPM,
	}
}

// Wait blocks until the account has an available quota token and a free
// concurrency permit, then returns the release function to call when done.
// The caller should defer the returned release function.
func (l *AccountLimiter) Wait(deviceID string, qpm, maxConcurrent int) (release func()) {
	releaseToken := l.WaitQPM(deviceID, qpm, maxConcurrent)
	releaseSlot := l.WaitSlot(context.Background(), deviceID, qpm, maxConcurrent)
	return func() {
		releaseSlot()
		releaseToken()
	}
}

// WaitQPM blocks until a rate-limit token is available for the account,
// then consumes one. The returned release is a no-op bookend (tokens are
// consumed per minute window, not returned); defer it for readability.
//
// Used in place of Wait when the caller wants to hold ONLY the rate token
// while the concurrency slot is managed separately (e.g. released during
// 429 backoff so idle waits never park in-flight slots).
func (l *AccountLimiter) WaitQPM(deviceID string, qpm, maxConcurrent int) func() {
	b := l.getBucket(deviceID, qpm, maxConcurrent)
	<-b.acquireToken()
	return func() {}
}

// WaitSlot blocks until a free concurrency permit is available, then
// returns the release function for it. If ctx is cancelled while waiting,
// no slot is acquired and the returned release is a no-op — the caller
// must check ctx.Err() itself and stop.
func (l *AccountLimiter) WaitSlot(ctx context.Context, deviceID string, qpm, maxConcurrent int) func() {
	b := l.getBucket(deviceID, qpm, maxConcurrent)
	select {
	case <-b.sem:
		return func() { b.sem <- struct{}{} }
	case <-ctx.Done():
		return func() {}
	}
}

// InFlight reports how many completion requests are currently executing on
// this account (semaphore slots in use). Request-picking can use it to
// spread new traffic across accounts instead of piling onto the busy one.
func (l *AccountLimiter) InFlight(deviceID string) int {
	l.mu.Lock()
	b, ok := l.buckets[deviceID]
	l.mu.Unlock()
	if !ok {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return cap(b.sem) - len(b.sem)
}

func (l *AccountLimiter) getBucket(deviceID string, qpm, maxConcurrent int) *accountBucket {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[deviceID]
	if !ok {
		b = &accountBucket{
			tokens:     qpm,
			defaultQPM: qpm,
			resetAt:    time.Now().Add(time.Minute),
			sem:        make(chan struct{}, maxConcurrent),
		}
		for i := 0; i < maxConcurrent; i++ {
			b.sem <- struct{}{}
		}
		l.buckets[deviceID] = b
		return b
	}
	// Refresh the rate window if expired.
	b.mu.Lock()
	if time.Now().After(b.resetAt) {
		b.tokens = qpm
		b.resetAt = time.Now().Add(time.Minute)
	}
	// Grow the semaphore capacity if needed. The new channel is pre-filled
	// to leave exactly one free slot per IN-FLIGHT request (a slot still
	// held against the old channel): every holder's deferred release writes
	// to b.sem — i.e. this NEW channel, re-read at release time — so each
	// one re-issues a token as it completes. The old channel is discarded
	// whole: after the swap nothing may ever acquire from it again, and a
	// blocking drain of it (the previous code) would deadlock here holding
	// both l.mu and b.mu whenever any slot was in flight, because those
	// releases never return to the old channel.
	if cap(b.sem) < maxConcurrent {
		inFlight := cap(b.sem) - len(b.sem)
		b.sem = make(chan struct{}, maxConcurrent)
		for i := 0; i < maxConcurrent-inFlight; i++ {
			b.sem <- struct{}{}
		}
	}
	b.mu.Unlock()
	return b
}

// acquireToken waits until a rate-limit token is available, then consumes one.
func (b *accountBucket) acquireToken() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for {
			b.mu.Lock()
			now := time.Now()
			if now.After(b.resetAt) {
				b.resetAt = now.Add(time.Minute)
				b.tokens = b.defaultQPM
			}
			if b.tokens > 0 {
				b.tokens--
				b.mu.Unlock()
				close(done)
				return
			}
			wait := time.Until(b.resetAt)
			b.mu.Unlock()
			if wait <= 0 {
				wait = time.Second
			}
			select {
			case <-time.After(wait):
				continue
			}
		}
	}()
	return done
}
