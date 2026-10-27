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
type RiskStats struct {
	requests        atomic.Int64
	sessionsCreated atomic.Int64
	cacheHits       atomic.Int64
	completions     []atomic.Int64
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
