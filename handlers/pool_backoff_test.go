package handlers

import (
	"errors"
	"testing"
	"time"
)

// TestRateCooldownEscalation: consecutive 429s escalate the account's
// cross-request cooldown 60s → 120s → … capped at 900s; success resets.
func TestRateCooldownEscalation(t *testing.T) {
	pool := NewPool([]string{"a", "b"}, 0, 0, 0)
	pool.MarkRateLimited(0)
	if left := pool.CoolingLeft(0); left < 55*time.Second || left > 65*time.Second {
		t.Fatalf("first 429 should cool ~60s, got %s", left)
	}
	// NextLive routes around the cooling account
	idx, ok := pool.NextLive()
	if !ok || idx != 1 {
		t.Fatalf("NextLive should offer the healthy account, got %d/%v", idx, ok)
	}
	pool.MarkRateLimited(0)
	if left := pool.CoolingLeft(0); left < 115*time.Second || left > 125*time.Second {
		t.Fatalf("second 429 should cool ~120s, got %s", left)
	}
	// cap at 900s
	for i := 0; i < 5; i++ {
		pool.MarkRateLimited(0)
	}
	if left := pool.CoolingLeft(0); left > 900*time.Second {
		t.Fatalf("cooldown must cap at 900s, got %s", left)
	}
	// a success clears the escalation immediately
	pool.MarkRateRecovery(0)
	if left := pool.CoolingLeft(0); left != 0 {
		t.Fatalf("recovery must clear the cooldown, got %s", left)
	}
	// recovery on a never-limited account is a silent no-op
	pool.MarkRateRecovery(1)
}

// TestNextLiveGrace: when every account is in cooldown, the one closest to
// recovery is still offered — but only when less than the 45s grace window
// remains.
func TestNextLiveGrace(t *testing.T) {
	pool := NewPool([]string{"a", "b"}, 0, 0, 0)
	// both cooling hard → nobody offered
	pool.MarkRateLimited(0) // 60s
	pool.MarkRateLimited(1) // 60s
	pool.MarkRateLimited(1) // 120s
	if _, ok := pool.NextLive(); ok {
		t.Fatal("all accounts cooling >45s must yield no account")
	}
	// shrink account 0's cooldown below the grace window (white-box: same
	// package, same lock discipline as the pool methods)
	pool.mu.Lock()
	pool.cooldownUntil[0] = time.Now().Add(30 * time.Second)
	pool.mu.Unlock()
	idx, ok := pool.NextLive()
	if !ok || idx != 0 {
		t.Fatalf("grace must offer the soonest-recovering account, got %d/%v", idx, ok)
	}
	// sanity: account 1 is still deep in its ~120s cooldown (out of grace)
	if left := pool.CoolingLeft(1); left < 115*time.Second || left > 125*time.Second {
		t.Fatalf("account 1 should have ~120s left, got %s", left)
	}
}

// TestBanHold: a BAN hold blocks probes until it expires; zero keeps the
// legacy "probe until success" behaviour.
func TestBanHold(t *testing.T) {
	pool := NewPool([]string{"a"}, 0, 0, 0)
	if pool.BanHeld(0) {
		t.Fatal("fresh account must not be BAN-held")
	}
	pool.SetBanUntil(0, time.Now().Add(time.Hour))
	if !pool.BanHeld(0) {
		t.Fatal("account must be BAN-held inside the window")
	}
	if left := pool.BanHeldLeft(0); left <= 0 || left > time.Hour {
		t.Fatalf("BanHeldLeft out of range: %s", left)
	}
	// Revive clears the hold
	if !pool.Quarantine(0, "ban") {
		t.Fatal("setup: quarantine")
	}
	pool.Revive(0)
	if pool.BanHeld(0) {
		t.Fatal("revive must clear the BAN hold")
	}
	// expired window = not held
	pool.SetBanUntil(0, time.Now().Add(time.Minute))
	pool.SetBanUntil(0, time.Now().Add(-time.Second))
	if pool.BanHeld(0) {
		t.Fatal("an expired BAN hold must not hold the account")
	}
}

// TestBanKeyword: ban wording is recognised on the error path only.
func TestBanKeyword(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errors.New("deepseek api: http 403, code 1: 您的账号已被封禁，请稍后再试"), "封禁"},
		{errors.New("upstream said: your account has been banned by the platform"), "banned"},
		{errors.New("账号异常，请联系管理员"), "账号异常"},
		{errors.New("deepseek api: http 401: token expired"), ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := banKeyword(c.err); got != c.want {
			t.Fatalf("banKeyword(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
