package handlers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/0xgetz/deepseek2api/config"
)

func newUsageHandler(t *testing.T, dailyLimit int, accounts int) *Handler {
	t.Helper()
	tokens := make([]string, 0, accounts)
	for i := 0; i < accounts; i++ {
		tokens = append(tokens, fmt.Sprintf("token-xxxx-%04d", i))
	}
	pool := NewPool(tokens, 0, 0, 0)
	cfg := &config.Config{
		AccountQPM:              6,
		MaxConcurrentPerAccount: 2,
		ProxyKeys:               []string{"k"},
		DailyRequestLimit:       dailyLimit,
	}
	h := New(cfg, pool, NewConvStore(nil, 10, 0))
	if dailyLimit > 0 {
		h.SetUsageTracker(NewUsageTracker("")) // memory only, no file
	}
	return h
}

// TestUsageTrackerQuota: Use/Count drive the exhaustion decision.
func TestUsageTrackerQuota(t *testing.T) {
	h := newUsageHandler(t, 2, 2)
	mask0 := maskToken(h.pool.clients[0].Token)
	if h.dailyExhausted(0) {
		t.Fatal("fresh account must not be daily-exhausted")
	}
	h.usage.Use(mask0)
	h.usage.Use(mask0)
	if !h.dailyExhausted(0) {
		t.Fatal("account at its limit must be daily-exhausted")
	}
	if h.dailyExhausted(1) {
		t.Fatal("other account must stay available")
	}
}

// TestUsageTrackerPersistence: counters survive a restart; a stale
// (yesterday's) file resets cleanly.
func TestUsageTrackerPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daily-usage.json")

	u1 := NewUsageTracker(path)
	mask := "ab12cd…ef90"
	u1.Use(mask)
	u1.Use(mask)
	u1.Use(mask)
	u1.Close()

	// reload (same day) → counters restored
	u2 := NewUsageTracker(path)
	if got := u2.Count(mask); got != 3 {
		t.Fatalf("persisted count must survive a restart, got %d", got)
	}

	// write a yesterday-dated file → must reset to zero
	data := map[string]any{"day": "1999-01-01", "counts": map[string]int{mask: 99}}
	raw, _ := json.Marshal(data)
	os.WriteFile(path, raw, 0644)
	u3 := NewUsageTracker(path)
	if got := u3.Count(mask); got != 0 {
		t.Fatalf("stale-day counters must reset, got %d", got)
	}
}

// TestResolveBoundAccountLeastBusy: new prefixes spread across the pool —
// the least-busy live account wins over the bare round-robin pointer.
func TestResolveBoundAccountLeastBusy(t *testing.T) {
	h := newUsageHandler(t, 0, 2)
	c1 := h.pool.clients[1]

	// make account 1 busy: hold one of its two concurrency slots
	release := h.limiter.Wait(c1.DeviceID, h.cfg.AccountQPM, h.cfg.MaxConcurrentPerAccount)

	if got := h.resolveBoundAccount("new-key-A"); got != 0 {
		t.Fatalf("new prefix must avoid the busy account, got #%d", got+1)
	}
	release()

	// both free again → either is acceptable, but the binding must exist
	if got := h.resolveBoundAccount("new-key-A"); got < 0 || got > 1 {
		t.Fatalf("unexpected account index %d", got)
	}
}

// TestResolveBoundAccountDailyExhausted: a bound account that spent its
// quota rebinds to the available one; with everyone exhausted, -1.
func TestResolveBoundAccountDailyExhausted(t *testing.T) {
	h := newUsageHandler(t, 1, 2)
	c0, c1 := h.pool.clients[0], h.pool.clients[1]
	h.usage.Use(maskToken(c0.Token)) // account 0 spent its quota
	h.usage.Use(maskToken(c1.Token)) // ... and account 1 too

	if got := h.resolveBoundAccount("some-key"); got != -1 {
		t.Fatalf("every account exhausted must yield -1, got #%d", got+1)
	}

	// quota reset (new day) → accounts available again
	u := NewUsageTracker("")
	h.SetUsageTracker(u)
	if got := h.resolveBoundAccount("some-key"); got < 0 || got > 1 {
		t.Fatalf("available accounts must be offered again, got %d", got)
	}
}
