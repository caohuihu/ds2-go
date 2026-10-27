package handlers

import (
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
	b := l.getBucket(deviceID, qpm, maxConcurrent)
	// Acquire a rate-limit token (blocks if exhausted this minute).
	<-b.acquireToken()
	// Acquire a concurrency slot (blocks if at in-flight limit).
	<-b.sem
	return func() {
		b.sem <- struct{}{}
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
	// Grow the semaphore capacity if needed.
	if cap(b.sem) < maxConcurrent {
		old := b.sem
		b.sem = make(chan struct{}, maxConcurrent)
		for i := 0; i < cap(old); i++ {
			<-old
		}
		for i := 0; i < maxConcurrent; i++ {
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
