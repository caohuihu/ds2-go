package handlers

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"
)

// RiskStats aggregates the risk-relevant signals operators want to watch on a
// live deployment. All counters are monotonic since process start:
//
//   - requests: chat requests that resolved a session (reached the completion
//     path — errors before that, e.g. dead pool, are not counted)
//   - sessionsCreated / cacheHits: how many requests had to create a fresh
//     upstream session vs. reusing a cached/restored one. New sessions are
//     the expensive, fingerprintable upstream action, so a rising
//     created/hits ratio is the first risk signal.
//   - completions[i]: successful completions served by pool account i
//     (429 escalations, cooldowns, BAN holds and daily quota come from the
//     pool / usage tracker themselves — see LogRiskStats).
//   - err429 / errAuth / errBan / errUpstream / errEmpty / errCancelled:
//     per-request OUTCOME classes, so the operator can tell which failure
//     mode is showing up (rate pressure vs dead tokens vs bans vs upstream
//     vs silent empty replies vs clients giving up) instead of just
//     "something failed".
//   - firstByteSum / firstByteCount: time-to-first-content latency (ms)
//     across successful requests — the user-visible responsiveness.
type RiskStats struct {
	requests        atomic.Int64
	sessionsCreated atomic.Int64
	cacheHits       atomic.Int64
	completions     []atomic.Int64
	err429          atomic.Int64
	errAuth         atomic.Int64
	errBan          atomic.Int64
	errUpstream     atomic.Int64
	errEmpty        atomic.Int64
	errCancelled    atomic.Int64
	firstByteSum    atomic.Int64 // milliseconds
	firstByteCount  atomic.Int64
}

// NewRiskStats builds the counter set for a pool of poolSize accounts.
func NewRiskStats(poolSize int) *RiskStats {
	return &RiskStats{completions: make([]atomic.Int64, poolSize)}
}

func (s *RiskStats) RecordRequest()  { s.requests.Add(1) }
func (s *RiskStats) RecordSession()  { s.sessionsCreated.Add(1) }
func (s *RiskStats) RecordCacheHit() { s.cacheHits.Add(1) }

// RecordCompletion credits one successful completion to the serving account.
func (s *RiskStats) RecordCompletion(idx int) {
	if idx >= 0 && idx < len(s.completions) {
		s.completions[idx].Add(1)
	}
}

func (s *RiskStats) TotalRequests() int64 { return s.requests.Load() }

func (s *RiskStats) SessionsCreated() int64 { return s.sessionsCreated.Load() }

func (s *RiskStats) CacheHits() int64 { return s.cacheHits.Load() }

// Completions returns the account's successful-completion counter.
func (s *RiskStats) Completions(idx int) int64 {
	if idx < 0 || idx >= len(s.completions) {
		return 0
	}
	return s.completions[idx].Load()
}

// Outcome-class counters. Each is safe to call from any goroutine, and
// from a nil receiver (test handlers may be built without stats). Note
// the guard comes before touching any field: taking the address of a
// field of a nil struct pointer panics, so the short form
// `s.addOutcome(&s.err429)` is not nil-safe.
func (s *RiskStats) Record429() {
	if s == nil {
		return
	}
	s.err429.Add(1)
}

func (s *RiskStats) RecordAuth() {
	if s == nil {
		return
	}
	s.errAuth.Add(1)
}

func (s *RiskStats) RecordBan() {
	if s == nil {
		return
	}
	s.errBan.Add(1)
}

func (s *RiskStats) RecordUpstream() {
	if s == nil {
		return
	}
	s.errUpstream.Add(1)
}

func (s *RiskStats) RecordEmpty() {
	if s == nil {
		return
	}
	s.errEmpty.Add(1)
}

func (s *RiskStats) RecordCancelled() {
	if s == nil {
		return
	}
	s.errCancelled.Add(1)
}

// RecordFirstContent accumulates the latency of a successful request
// (streaming: until the stream finished; non-streaming: full completion
// time). FirstContentAvg reports the mean across successful requests.
func (s *RiskStats) RecordFirstContent(d time.Duration) {
	if s == nil {
		return
	}
	s.firstByteSum.Add(int64(d / time.Millisecond))
	s.firstByteCount.Add(1)
}

// ErrorCounts returns the six outcome-class counters as a slice of
// (label, count) pairs, for the risk summary.
func (s *RiskStats) ErrorCounts() []errorCount {
	return []errorCount{
		{"429", s.err429.Load()},
		{"auth", s.errAuth.Load()},
		{"ban", s.errBan.Load()},
		{"upstream", s.errUpstream.Load()},
		{"empty", s.errEmpty.Load()},
		{"cancelled", s.errCancelled.Load()},
	}
}

type errorCount struct {
	label string
	n     int64
}

// FirstContentAvg returns the mean time-to-first-content across successful
// requests (0 when none recorded yet).
func (s *RiskStats) FirstContentAvg() time.Duration {
	n := s.firstByteCount.Load()
	if n == 0 {
		return 0
	}
	return time.Duration(s.firstByteSum.Load()/n) * time.Millisecond
}

// LogRiskStats writes the multi-line per-account risk summary to the main log
// (one line for the global counters, one per account). It is safe to call
// from any goroutine; the pool/usage accessors take their own locks.
func (h *Handler) LogRiskStats() {
	s := h.stats
	if s == nil {
		return
	}
	log.Printf("risk: requests=%d sessions_created=%d cache_hits=%d live=%d/%d",
		s.TotalRequests(), s.SessionsCreated(), s.CacheHits(), h.pool.LiveCount(), len(h.pool.clients))
	// Outcome-class breakdown: which failure mode is showing up (all zero =
	// healthy). first_content is the mean time-to-first-content latency.
	ec := s.ErrorCounts()
	log.Printf("risk: outcomes 429=%d auth=%d ban=%d upstream=%d empty=%d cancelled=%d first_content=%s",
		ec[0].n, ec[1].n, ec[2].n, ec[3].n, ec[4].n, ec[5].n, s.FirstContentAvg().Round(time.Millisecond))
	reasons := h.pool.DeadReasons()
	for i, c := range h.pool.clients {
		daily := "-"
		if h.usage != nil && h.cfg.DailyRequestLimit > 0 {
			daily = fmt.Sprintf("%d/%d", h.usage.Count(maskToken(c.Token)), h.cfg.DailyRequestLimit)
		}
		state := "live"
		if reason, dead := reasons[i]; dead {
			state = "quarantined (" + reason + ")"
		}
		if h.pool.BanHeld(i) {
			if left := h.pool.BanHeldLeft(i); left > 0 {
				state += ", ban-held " + left.Round(time.Second).String()
			} else {
				state += ", ban-held indefinitely"
			}
		}
		if cool := h.pool.CoolingLeft(i); cool > 0 {
			state += ", cooling " + cool.Round(time.Second).String()
		}
		log.Printf("  account %d/%d (%s): completions=%d rate429s=%d busy=%d daily=%s [%s]",
			i+1, len(h.pool.clients), maskToken(c.Token), s.Completions(i), h.pool.RateHits(i), h.limiter.InFlight(c.DeviceID), daily, state)
	}
}
