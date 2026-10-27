package handlers

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// UsageTracker counts how many requests each account has served today and
// persists the counters to <path> so a restart does not reset the quota.
//
// Counts are kept per day (local date): when the calendar day rolls over
// every counter starts at zero again. Accounts are keyed by their masked
// token (maskToken), so reordering accounts.txt never shifts the counts.
//
// The file is written throttled (at most once a minute) + on day rollover +
// on Close, with atomic tmp+rename, mirroring the session-index persister.
type UsageTracker struct {
	path string

	mu     sync.Mutex
	day    string
	counts map[string]int // maskToken prefix → requests today
	last   time.Time
}

type usageSnapshot struct {
	Day    string         `json:"day"`
	Counts map[string]int `json:"counts"`
}

// NewUsageTracker loads the on-disk counters for today (a missing or corrupt
// file just starts from zero). pass an empty path for an in-memory-only
// tracker (no persistence).
func NewUsageTracker(path string) *UsageTracker {
	u := &UsageTracker{
		path:   path,
		day:    time.Now().Format("2006-01-02"),
		counts: make(map[string]int),
		last:   time.Now(),
	}
	if path != "" {
		u.load()
	}
	return u
}

func (u *UsageTracker) load() {
	data, err := os.ReadFile(u.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("usage: cannot read %s: %v (starting from zero)", u.path, err)
		}
		return
	}
	var snap usageSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		// Quarantine the corrupt file instead of crashing.
		bad := u.path + ".corrupt-" + time.Now().Format("20060102-150405")
		if rerr := os.Rename(u.path, bad); rerr != nil {
			log.Printf("usage: %s unreadable (%v), leaving in place", u.path, err)
		} else {
			log.Printf("usage: %s was corrupt, quarantined as %s (starting from zero)", u.path, filepath.Base(bad))
		}
		return
	}
	if snap.Day == u.day {
		u.counts = snap.Counts
		if len(u.counts) > 0 {
			log.Printf("usage: restored today's per-account request counts from %s", u.path)
		}
		return
	}
	log.Printf("usage: %s holds counts for %s (today is %s); counters reset", u.path, snap.Day, u.day)
}

// Use records one request on the account and returns the account's running
// total for today (after this increment). Callers compare it against the
// configured quota to decide whether to route around the account.
func (u *UsageTracker) Use(masked string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.day != time.Now().Format("2006-01-02") {
		// Day rolled over while running: zero the counters and re-assert.
		u.day = time.Now().Format("2006-01-02")
		u.counts = make(map[string]int)
	}
	u.counts[masked]++
	total := u.counts[masked]
	if time.Since(u.last) >= time.Minute {
		u.writeLocked()
	}
	return total
}

// Count returns the account's current counter (0 when none / quota unset).
func (u *UsageTracker) Count(masked string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.counts[masked]
}

func (u *UsageTracker) writeLocked() {
	if u.path == "" {
		return
	}
	snap := usageSnapshot{Day: u.day, Counts: u.counts}
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(u.path), 0755); err != nil {
		return
	}
	tmp := u.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("usage: write %s failed: %v", u.path, err)
		return
	}
	if err := os.Rename(tmp, u.path); err != nil {
		log.Printf("usage: rename %s failed: %v", tmp, err)
		return
	}
	u.last = time.Now()
}

// Close flushes the counters (forced) — call at shutdown.
func (u *UsageTracker) Close() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.counts) > 0 {
		u.writeLocked()
	}
}
