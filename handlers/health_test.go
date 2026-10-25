package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountPoolQuarantine(t *testing.T) {
	pool := NewPool([]string{"aaa", "bbb", "ccc"}, 0, 0, 0)
	if pool.LiveCount() != 3 || !pool.HasLive() {
		t.Fatal("all accounts should start live")
	}

	if !pool.Quarantine(1, "upstream http 401") {
		t.Fatal("first quarantine should report state change")
	}
	if pool.Quarantine(1, "again") {
		t.Fatal("second quarantine should be a no-op")
	}
	if pool.LiveCount() != 2 || !pool.IsDead(1) {
		t.Fatal("account 1 must be dead, others live")
	}

	// NextLive skips the dead account
	seen := map[int]bool{}
	for i := 0; i < 20; i++ {
		idx, ok := pool.NextLive()
		if !ok {
			t.Fatal("expected a live account")
		}
		seen[idx] = true
	}
	if seen[1] {
		t.Fatal("NextLive must never return a quarantined account")
	}
	if !seen[0] || !seen[2] {
		t.Fatalf("both live accounts must be offered, got %v", seen)
	}

	// Revive
	if !pool.Revive(1) {
		t.Fatal("revive of a dead account should report change")
	}
	if pool.IsDead(1) || pool.LiveCount() != 3 {
		t.Fatal("account 1 must be live again")
	}

	// All dead
	pool.Quarantine(0, "x")
	pool.Quarantine(1, "x")
	pool.Quarantine(2, "x")
	if pool.HasLive() {
		t.Fatal("all accounts quarantined")
	}
	if _, ok := pool.NextLive(); ok {
		t.Fatal("NextLive must fail when all are dead")
	}

	// IndexOf
	if pool.IndexOf(pool.clients[1]) != 1 {
		t.Fatal("IndexOf broken")
	}
}

func TestAccountPoolNextLiveErrWhenEmpty(t *testing.T) {
	pool := NewPool(nil, 0, 0, 0)
	if _, ok := pool.NextLive(); ok {
		t.Fatal("empty pool must report no live account")
	}
	if pool.IsDead(0) != true {
		t.Fatal("out-of-range index must count as dead")
	}
}

func TestHealthCheckerWritesLogFile(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "health.log")
	pool := NewPool([]string{"token-aaaaaaaa-bbbb", "token-cccccccc-dddd"}, 0, 0, 0)

	hc, err := NewHealthChecker(pool, logFile)
	if err != nil {
		t.Fatalf("open health log: %v", err)
	}
	hc.EventQuarantine(0, "upstream http 401 during completion")
	hc.writeSummary()
	hc.Close()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read health log: %v", err)
	}
	out := string(data)
	for _, want := range []string{
		"[request]",
		"#1/2",
		"token-…bbbb", // masked, never the full token
		"quarantined",
		"[summary]",
		"total=2 live=1 quarantined=1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("health log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "token-aaaaaaaa-bbbb") {
		t.Fatal("full token leaked into health log")
	}
}

func TestHealthCheckerNilFile(t *testing.T) {
	pool := NewPool([]string{"x"}, 0, 0, 0)
	hc, err := NewHealthChecker(pool, filepath.Join(t.TempDir(), "no", "such", "dir", "f.log"))
	if err == nil {
		t.Fatal("expected error for unopenable log path")
	}
	// Must still be usable (events fall back to the main log only).
	hc.EventQuarantine(0, "test")
	hc.Close()
	if !pool.IsDead(0) {
		t.Fatal("event must quarantine the account even without a file")
	}
}
