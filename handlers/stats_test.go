package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/0xgetz/deepseek2api/config"
	"github.com/0xgetz/deepseek2api/deepseek"
)

// newStatsHandler builds a handler with poolSize accounts and no
// persistence / rotation, for counter and gate tests.
func newStatsHandler(t *testing.T, accounts int) *Handler {
	t.Helper()
	tokens := make([]string, 0, accounts)
	for i := 0; i < accounts; i++ {
		tokens = append(tokens, "token-xxxx-test-"+string(rune('0'+i)))
	}
	pool := NewPool(tokens, 0, 0, 0)
	cfg := &config.Config{
		AccountQPM:              6,
		MaxConcurrentPerAccount: 2,
		ProxyKeys:               []string{"k"},
	}
	h := New(cfg, pool, NewConvStore(nil, 10, 0))
	return h
}

// TestShouldAutoContinue: the gate fires only on fully-silent,
// non-tool-flow replies in a session that already has a parent message.
func TestShouldAutoContinue(t *testing.T) {
	withParent := &Conversation{HasParent: true, ParentID: 7}
	noParent := &Conversation{}
	calls := []ToolCall{{ID: "1"}}

	cases := []struct {
		name     string
		conv     *Conversation
		toolFlow bool
		reply    string
		thinking string
		calls    []ToolCall
		want     bool
	}{
		{"silent empty reply, has parent", withParent, false, "", "", nil, true},
		{"whitespace-only reply still counts as silent", withParent, false, "  \n", "", nil, true},
		{"fresh session has no parent to chain off", noParent, false, "", "", nil, false},
		{"non-empty body → nothing to nudge", withParent, false, "hello", "", nil, false},
		{"thinking present → the answer is coming, don't nudge", withParent, false, "", "pondering…", nil, false},
		{"tool calls emitted → empty content is the normal shape", withParent, false, "", "", calls, false},
		{"tool-flow turn → machine-paced, never nudge", withParent, true, "", "", nil, false},
		{"nil conversation is safe", nil, false, "", "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAutoContinue(tc.conv, tc.toolFlow, tc.reply, tc.thinking, tc.calls); got != tc.want {
				t.Fatalf("shouldAutoContinue = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRiskStatsCounters: monotonic counters and per-account completion
// credit (out-of-range indices are ignored, never panicked).
func TestRiskStatsCounters(t *testing.T) {
	h := newStatsHandler(t, 2)
	s := h.stats
	s.RecordRequest()
	s.RecordSession()
	s.RecordRequest()
	s.RecordCacheHit()
	s.RecordCompletion(0)
	s.RecordCompletion(1)
	s.RecordCompletion(1)
	s.RecordCompletion(5) // out of range: ignored

	if got := s.TotalRequests(); got != 2 {
		t.Fatalf("TotalRequests = %d, want 2", got)
	}
	if got := s.SessionsCreated(); got != 1 {
		t.Fatalf("SessionsCreated = %d, want 1", got)
	}
	if got := s.CacheHits(); got != 1 {
		t.Fatalf("CacheHits = %d, want 1", got)
	}
	if got := s.Completions(0); got != 1 {
		t.Fatalf("Completions(0) = %d, want 1", got)
	}
	if got := s.Completions(1); got != 2 {
		t.Fatalf("Completions(1) = %d, want 2", got)
	}
	if got := s.Completions(5); got != 0 {
		t.Fatalf("Completions(out-of-range) = %d, want 0", got)
	}
}

// TestLogRiskStats: the summary block must run to completion on a plain
// pool and report the current shape (nothing quarantined / cooling yet).
func TestLogRiskStats(t *testing.T) {
	h := newStatsHandler(t, 2)
	h.stats.RecordRequest()
	h.stats.RecordCompletion(0)
	h.LogRiskStats() // must not panic; output goes to the test log
}

// TestRiskStatsOutcomeCounters: the six outcome classes count independently,
// FirstContentAvg averages the recorded latencies, and nil receivers are
// safe (test handlers built without stats must not crash the paths that
// record outcomes).
func TestRiskStatsOutcomeCounters(t *testing.T) {
	var nilS *RiskStats
	nilS.Record429()
	nilS.RecordCancelled()
	nilS.RecordFirstContent(time.Second) // no panic

	s := NewRiskStats(2)
	s.Record429()
	s.RecordAuth()
	s.RecordBan()
	s.RecordUpstream()
	s.RecordEmpty()
	s.RecordCancelled()
	s.Record429()
	want := map[string]int64{"429": 2, "auth": 1, "ban": 1, "upstream": 1, "empty": 1, "cancelled": 1}
	ec := s.ErrorCounts()
	if len(ec) != len(want) {
		t.Fatalf("ErrorCounts = %v, want %d entries", ec, len(want))
	}
	for _, p := range ec {
		if p.n != want[p.label] {
			t.Fatalf("outcome %q = %d, want %d", p.label, p.n, want[p.label])
		}
	}

	s.RecordFirstContent(200 * time.Millisecond)
	s.RecordFirstContent(400 * time.Millisecond)
	if got := s.FirstContentAvg(); got != 300*time.Millisecond {
		t.Fatalf("FirstContentAvg = %s, want 300ms", got)
	}
	// No recordings yet on a fresh counter set → 0, not NaN/panic.
	if got := NewRiskStats(1).FirstContentAvg(); got != 0 {
		t.Fatalf("FirstContentAvg (empty) = %s, want 0", got)
	}
}

// TestRecordUpstreamErrorClasses: classification mirrors
// mapUpstreamError's precedence — ban wording beats auth, 429 is its own
// class, and everything else (dead pool, daily limit, generic upstream)
// counts as "upstream".
func TestRecordUpstreamErrorClasses(t *testing.T) {
	h := newStatsHandler(t, 2)
	h.recordUpstreamError(&deepseek.APIError{HTTPStatus: 429})
	h.recordUpstreamError(&deepseek.APIError{HTTPStatus: 401})
	h.recordUpstreamError(errors.New("deepseek upstream error: 账号已被封禁"))
	h.recordUpstreamError(errNoLiveAccount)
	h.recordUpstreamError(errors.New("boom"))
	h.recordUpstreamError(nil)                                                             // safe
	(&Handler{pool: NewPool([]string{"t"}, 0, 0, 0)}).recordUpstreamError(errors.New("x")) // nil stats is safe

	want := map[string]int64{"429": 1, "auth": 1, "ban": 1, "upstream": 2, "empty": 0, "cancelled": 0}
	for _, p := range h.stats.ErrorCounts() {
		if p.n != want[p.label] {
			t.Fatalf("outcome %q = %d, want %d", p.label, p.n, want[p.label])
		}
	}
}

// TestWriteUpstreamErrorRetryAfter: 429/503 responses carry a Retry-After
// hint from the pool's longest active 429 cooldown; no hint when nobody is
// cooling.
func TestWriteUpstreamErrorRetryAfter(t *testing.T) {
	h := newStatsHandler(t, 2)
	h.pool.cooldownUntil[0] = time.Now().Add(58 * time.Second)

	fr := &flushRecorder{}
	h.writeUpstreamError(fr, &deepseek.APIError{HTTPStatus: 429})
	if got := fr.header.Get("Retry-After"); got != "58" {
		t.Fatalf("429 Retry-After = %q, want 58", got)
	}
	if got := fr.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	fr2 := &flushRecorder{}
	h.writeUpstreamError(fr2, errNoLiveAccount) // 503
	if got := fr2.header.Get("Retry-After"); got != "58" {
		t.Fatalf("503 Retry-After = %q, want 58", got)
	}

	// No cooldown active → no hint.
	h.pool.cooldownUntil[0] = time.Time{}
	fr3 := &flushRecorder{}
	h.writeUpstreamError(fr3, &deepseek.APIError{HTTPStatus: 429})
	if got := fr3.header.Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After without cooldown = %q, want empty", got)
	}
}
