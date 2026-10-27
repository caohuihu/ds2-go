package handlers

import (
	"testing"

	"github.com/0xgetz/deepseek2api/config"
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
