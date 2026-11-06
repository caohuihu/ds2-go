package handlers

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/0xgetz/deepseek2api/config"
	"github.com/0xgetz/deepseek2api/deepseek"
)

// TestQuarantineOnSessionCreateFailures: an auth failure or BAN wording
// surfaced by CreateSession quarantines the account on the spot — the
// passive path the 2026-11-07 retry storm was missing (a dead token waited
// for the periodic probe and kept receiving create attempts).
func TestQuarantineOnSessionCreateFailures(t *testing.T) {
	h := newStatsHandler(t, 2)
	h.cfg.BanQuarantineHours = 168

	invalidToken := &deepseek.APIError{HTTPStatus: 200, Code: 40003, Msg: "Authorization Failed (invalid token)"}
	h.quarantineOnUpstreamFailure(h.pool.clients[0], invalidToken, "on session create")
	if got := h.pool.LiveCount(); got != 1 {
		t.Fatalf("after invalid-token quarantine LiveCount = %d, want 1", got)
	}

	banned := errors.New("deepseek api: http 200, code 10001: 账号已被封禁")
	h.quarantineOnUpstreamFailure(h.pool.clients[1], banned, "on session create")
	if got := h.pool.LiveCount(); got != 0 {
		t.Fatalf("after BAN quarantine LiveCount = %d, want 0", got)
	}

	// Generic failures leave the account live.
	h2 := newStatsHandler(t, 1)
	h2.quarantineOnUpstreamFailure(h2.pool.clients[0], errors.New("boom"), "on session create")
	if got := h2.pool.LiveCount(); got != 1 {
		t.Fatalf("generic failure quarantined the account: LiveCount = %d, want 1", got)
	}

	// nil client / nil error are safe.
	h2.quarantineOnUpstreamFailure(nil, nil, "on session create")
}

// TestToolLoopGapFloorAndScaling: the tool-loop wait is the TOOL_LOOP_MIN_GAP
// floor, grown by the previous reply's length (1s per 2000 chars, 30s cap).
func TestToolLoopGapFloorAndScaling(t *testing.T) {
	cfg := &config.Config{ToolLoopMinGap: 8 * time.Second}
	cases := []struct {
		name     string
		chars    int
		want     time.Duration
	}{
		{"short reply keeps the floor", 0, 8 * time.Second},
		{"reply under the floor (4s read) keeps the floor", 8000, 8 * time.Second},
		{"reply over the floor (10s read) grows the gap", 20000, 10 * time.Second},
		{"very long reply (50s read) hits the cap", 100000, 30 * time.Second},
	}
	for _, tc := range cases {
		if got := toolLoopGap(cfg, tc.chars); got != tc.want {
			t.Fatalf("%s: gap = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// TestToolLoopGapEnforced: after a recorded request, the next tool-loop
// request waits at least the floor; TOOL_LOOP_MIN_GAP=0 disables pacing.
func TestToolLoopGapEnforced(t *testing.T) {
	h := newStatsHandler(t, 1)
	h.cfg.ToolLoopMinGap = 80 * time.Millisecond
	client := h.pool.clients[0]

	// No history → no wait.
	start := time.Now()
	h.enforceReadTimeCooldown(client, true)
	if wait := time.Since(start); wait > 25*time.Millisecond {
		t.Fatalf("first request waited %s, want ~0", wait)
	}

	// A recorded request → the floor is enforced (jitter adds at most
	// gap/2, bounded by the 4s cap — here 40ms, so the sleep stays short).
	h.recordRequestTime(client, 0)
	start = time.Now()
	h.enforceReadTimeCooldown(client, true)
	if wait := time.Since(start); wait < 75*time.Millisecond {
		t.Fatalf("tool-loop wait = %s, want ≥ ~80ms", wait)
	}

	// 0 disables tool-loop pacing entirely.
	h.cfg.ToolLoopMinGap = 0
	h.recordRequestTime(client, 0)
	start = time.Now()
	h.enforceReadTimeCooldown(client, true)
	if wait := time.Since(start); wait > 10*time.Millisecond {
		t.Fatalf("disabled pacing waited %s, want ~0", wait)
	}
}

// TestWriteUpstreamErrorDeadPoolRetryAfter: with no 429 cooldown in flight,
// a 503 (whole pool dead or over quota) still carries a 60s backoff hint so
// clients stop re-sending the same conversation every second.
func TestWriteUpstreamErrorDeadPoolRetryAfter(t *testing.T) {
	h := newStatsHandler(t, 1)

	fr := &flushRecorder{}
	h.writeUpstreamError(fr, errNoLiveAccount)
	if got := fr.header.Get("Retry-After"); got != "60" {
		t.Fatalf("dead-pool Retry-After = %q, want 60", got)
	}
	if fr.statusCode != http.StatusServiceUnavailable {
		t.Fatalf("dead-pool status = %d, want 503", fr.statusCode)
	}

	fr2 := &flushRecorder{}
	h.writeUpstreamError(fr2, errDailyLimit)
	if got := fr2.header.Get("Retry-After"); got != "60" {
		t.Fatalf("daily-limit Retry-After = %q, want 60", got)
	}

	// A 429 with no cooldown active still carries no hint.
	fr3 := &flushRecorder{}
	h.writeUpstreamError(fr3, &deepseek.APIError{HTTPStatus: 429})
	if got := fr3.header.Get("Retry-After"); got != "" {
		t.Fatalf("429 without cooldown Retry-After = %q, want empty", got)
	}
}
