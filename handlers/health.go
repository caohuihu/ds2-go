package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/0xgetz/deepseek2api/deepseek"
)

// errNoLiveAccount is returned when every account in the pool is
// quarantined (tokens expired) and nobody can serve a request.
var errNoLiveAccount = errors.New("all deepseek accounts are quarantined (tokens expired); refresh accounts.txt and restart")

// errDailyLimit is returned when every live account has already spent its
// DAILY_REQUEST_LIMIT budget for today; new sessions wait until midnight
// (existing cached sessions keep working on their bound accounts).
var errDailyLimit = errors.New("all live deepseek accounts reached their daily request limit (DAILY_REQUEST_LIMIT); retry after midnight or raise the limit")

// ---- AccountPool quarantine state ----
//
// The pool (defined in handler.go) carries a dead map: quarantined account
// index → reason. An account is quarantined only on upstream auth failures
// (401/403) — never on 429, 5xx or network errors, which are transient.

// IsDead reports whether the account at idx is quarantined.
func (p *AccountPool) IsDead(idx int) bool {
	if idx < 0 || idx >= len(p.clients) {
		return true
	}
	p.mu.Lock()
	_, dead := p.dead[idx]
	p.mu.Unlock()
	return dead
}

// Quarantine marks the account as dead. Returns true when the state changed.
func (p *AccountPool) Quarantine(idx int, reason string) bool {
	if idx < 0 || idx >= len(p.clients) {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.dead[idx]; ok {
		return false
	}
	p.dead[idx] = reason
	return true
}

// Revive clears the quarantine (and any BAN hold). Returns true when the
// account was dead.
func (p *AccountPool) Revive(idx int) bool {
	if idx < 0 || idx >= len(p.clients) {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.dead[idx]
	delete(p.dead, idx)
	p.banUntil[idx] = time.Time{}
	return ok
}

// IndexOf finds the pool index of a client (-1 when unknown).
func (p *AccountPool) IndexOf(c *deepseek.Client) int {
	for i, cl := range p.clients {
		if cl == c {
			return i
		}
	}
	return -1
}

// FindByMask returns the pool index of the account whose token matches the
// given maskToken representation (-1 when unknown or quarantined). Used to
// re-map persisted session records after a restart: token prefixes survive
// accounts.txt reordering, bare indexes do not.
func (p *AccountPool) FindByMask(masked string) int {
	if masked == "" {
		return -1
	}
	for i, c := range p.clients {
		if maskToken(c.Token) == masked && !p.IsDead(i) {
			return i
		}
	}
	return -1
}

// LiveCount returns how many accounts are not quarantined.
func (p *AccountPool) LiveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.clients) - len(p.dead)
}

// HasLive reports whether at least one account can serve requests.
func (p *AccountPool) HasLive() bool {
	return p.LiveCount() > 0
}

// NextLive round-robins over the live accounts, skipping quarantined and
// rate-cooling ones. When every live account is in a 429 cooldown, it
// still lets through the one closest to recovery — but only when less than
// cooldownGraceWindow remains (letting a nearly-cooled account through
// beats a 503, while a full backoff window is respected).
func (p *AccountPool) NextLive() (int, bool) {
	n := len(p.clients)
	if n == 0 {
		return 0, false
	}
	start := int(p.next.Add(1)) % n
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		if !p.IsDead(idx) && p.CoolingLeft(idx) <= 0 {
			return idx, true
		}
	}
	// Grace pass: everyone is dead or cooling; take the live account whose
	// cooldown ends soonest, within the grace window.
	deadline := make([]time.Duration, n)
	for i := range deadline {
		deadline[i] = p.CoolingLeft(i)
	}
	grace := -1
	for i := 0; i < n; i++ {
		if p.IsDead(i) || deadline[i] <= 0 || deadline[i] > cooldownGraceWindow {
			continue
		}
		if grace == -1 || deadline[i] < deadline[grace] {
			grace = i
		}
	}
	if grace >= 0 {
		return grace, true
	}
	return 0, false
}

// cooldownGraceWindow: all accounts cooling → allow the one that recovers
// within this window.
const cooldownGraceWindow = 45 * time.Second

// ---- account-level 429 backoff ----
//
// Every 429 seen on an account escalates its cooldown (60s, 120s, 240s, …
// capped at 900s) so the next *request* skips the throttled account instead
// of ramming it again; a successful completion clears the escalation. This
// is the cross-request complement to chatWithRetry's in-request backoff.

// MarkRateLimited records a 429 on the account and puts it into rate
// cooldown. The nth consecutive 429 without a success cools for
// min(900s, 60s × 2^(n-1)).
func (p *AccountPool) MarkRateLimited(idx int) {
	if idx < 0 || idx >= len(p.clients) {
		return
	}
	p.mu.Lock()
	p.rateHits[idx]++
	n := p.rateHits[idx]
	cool := time.Duration(60*(1<<uint(n-1))) * time.Second
	if cool > 900*time.Second {
		cool = 900 * time.Second
	}
	p.cooldownUntil[idx] = time.Now().Add(cool)
	p.mu.Unlock()
	log.Printf("account #%d/%d rate-limited (429 x%d), cooling down for %s", idx+1, len(p.clients), n, cool.Round(time.Second))
}

// MarkRateRecovery clears the escalation after a successful completion.
func (p *AccountPool) MarkRateRecovery(idx int) {
	if idx < 0 || idx >= len(p.clients) {
		return
	}
	p.mu.Lock()
	if p.rateHits[idx] > 0 {
		p.rateHits[idx] = 0
		p.cooldownUntil[idx] = time.Time{}
		p.mu.Unlock()
		log.Printf("account #%d/%d recovered from rate limit", idx+1, len(p.clients))
		return
	}
	p.mu.Unlock()
}

// RateHits returns the account's current consecutive-429 escalation count
// (0 after a success cleared it). It is what MarkRateLimited escalates from
// and the risk summary reports; read under the pool mutex.
func (p *AccountPool) RateHits(idx int) int {
	if idx < 0 || idx >= len(p.clients) {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rateHits[idx]
}

// CoolingLeft returns the remaining 429 cooldown of the account (0 = none).
func (p *AccountPool) CoolingLeft(idx int) time.Duration {
	if idx < 0 || idx >= len(p.clients) {
		return 0
	}
	p.mu.Lock()
	left := p.cooldownUntil[idx].Sub(time.Now())
	p.mu.Unlock()
	if left < 0 {
		return 0
	}
	return left
}

// SetBanUntil holds a BAN-detected account out of rotation until then (a
// probe after the hold may still revive it). Until.IsZero() = no hold.
func (p *AccountPool) SetBanUntil(idx int, until time.Time) {
	if idx < 0 || idx >= len(p.clients) {
		return
	}
	p.mu.Lock()
	p.banUntil[idx] = until
	p.mu.Unlock()
}

// BanHeld reports whether the account is inside a BAN hold window (its
// probes are skipped; it re-arms automatically when the window passes).
func (p *AccountPool) BanHeld(idx int) bool {
	if idx < 0 || idx >= len(p.clients) {
		return false
	}
	p.mu.Lock()
	held := !p.banUntil[idx].IsZero() && p.banUntil[idx].After(time.Now())
	p.mu.Unlock()
	return held
}

// DeadReasons snapshots the quarantined accounts and their reasons.
func (p *AccountPool) DeadReasons() map[int]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[int]string, len(p.dead))
	for k, v := range p.dead {
		out[k] = v
	}
	return out
}

// ---- token masking ----

// maskToken shows just enough of a token to locate it in accounts.txt
// without ever logging the full credential.
func maskToken(t string) string {
	if len(t) <= 12 {
		if len(t) > 3 {
			return t[:3] + "…"
		}
		return "…"
	}
	return t[:6] + "…" + t[len(t)-4:]
}

// ---- HealthChecker ----

// HealthChecker actively probes the accounts to detect expired userTokens and
// records every finding in a dedicated log file, so an operator can tell at
// a glance which token in accounts.txt needs replacing:
//
//	2026-10-25T01:00:03+08:00 [probe] #1/5 ab12cd…ef90 EXPIRED (http 401) → quarantined
//	2026-10-25T01:00:03+08:00 [summary] total=5 live=3 quarantined=2 [#1 http 401, #4 http 403]
//
// Probing uses the lightweight PoW-challenge endpoint: it authenticates but
// has no side effects (no session created, no completion quota used).
type HealthChecker struct {
	pool *AccountPool
	mu   sync.Mutex
	f    *os.File // nil → events go to the main log only
}

// NewHealthChecker opens the health log (append mode) when logFile is
// non-empty. The checker is usable even if the file cannot be opened.
func NewHealthChecker(pool *AccountPool, logFile string) (*HealthChecker, error) {
	hc := &HealthChecker{pool: pool}
	if logFile == "" {
		return hc, nil
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return hc, err
	}
	hc.f = f
	return hc, nil
}

// Close flushes and closes the health log file.
func (hc *HealthChecker) Close() {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if hc.f != nil {
		hc.f.Close()
		hc.f = nil
	}
}

// event appends one timestamped line to the health log and mirrors it to the
// main log for live visibility.
func (hc *HealthChecker) event(kind, msg string) {
	log.Printf("health: [%s] %s", kind, msg)
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if hc.f != nil {
		fmt.Fprintf(hc.f, "%s [%s] %s\n", time.Now().Format("2006-01-02T15:04:05-07:00"), kind, msg)
	}
}

func probeLabel(pool *AccountPool, idx int) string {
	return fmt.Sprintf("#%d/%d %s", idx+1, len(pool.clients), maskToken(pool.clients[idx].Token))
}

// ProbeAccount checks one account. Success revives a quarantined account;
// 401/403 quarantines it; transient errors leave the state unchanged.
func (hc *HealthChecker) ProbeAccount(idx int) {
	p := hc.pool
	if idx < 0 || idx >= len(p.clients) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	_, err := p.clients[idx].CreatePowChallenge(ctx)
	cancel()

	label := probeLabel(p, idx)
	var ae *deepseek.APIError
	switch {
	case err == nil:
		if p.Revive(idx) {
			hc.event("probe", label+" REVIVED (was quarantined)")
		} else {
			hc.event("probe", label+" OK")
		}
	case errors.As(err, &ae) && (ae.HTTPStatus == 401 || ae.HTTPStatus == 403):
		reason := fmt.Sprintf("upstream http %d", ae.HTTPStatus)
		if p.Quarantine(idx, reason) {
			hc.event("probe", fmt.Sprintf("%s EXPIRED (%s) → quarantined", label, reason))
		} else {
			hc.event("probe", fmt.Sprintf("%s EXPIRED (%s), already quarantined", label, reason))
		}
	default:
		hc.event("probe", fmt.Sprintf("%s transient error (%v), state unchanged", label, err))
	}
}

// ProbeAll checks every account with a random stagger, then appends a
// one-line summary so `tail -f` on the health log always shows the current
// situation.
func (hc *HealthChecker) ProbeAll() {
	p := hc.pool
	for idx := range p.clients {
		hc.ProbeAccount(idx)
		if idx < len(p.clients)-1 {
			time.Sleep(time.Duration(rand.Intn(2000)) * time.Millisecond)
		}
	}
	hc.writeSummary()
}

// RetryDead re-probes quarantined accounts only (useful after a token has
// been refreshed in accounts.txt). Accounts still inside a BAN hold window
// are skipped — hammering a banned account only makes the ban worse; the
// hold expires and the account re-arms itself automatically.
func (hc *HealthChecker) RetryDead() {
	p := hc.pool
	if p.LiveCount() == len(p.clients) {
		return // all live; nothing to retry
	}
	for idx := range p.clients {
		if !p.IsDead(idx) {
			continue
		}
		if p.BanHeld(idx) {
			hc.event("probe", probeLabel(p, idx)+" still in BAN hold ("+p.BanHeldLeft(idx).Round(time.Hour).String()+" left), probe skipped")
			continue
		}
		hc.ProbeAccount(idx)
	}
	hc.writeSummary()
}

// BanHeldLeft returns the remaining BAN hold of the account (0 = none).
func (p *AccountPool) BanHeldLeft(idx int) time.Duration {
	if idx < 0 || idx >= len(p.clients) {
		return 0
	}
	p.mu.Lock()
	left := p.banUntil[idx].Sub(time.Now())
	p.mu.Unlock()
	if left < 0 {
		return 0
	}
	return left
}

func (hc *HealthChecker) writeSummary() {
	p := hc.pool
	dead := p.DeadReasons()
	var parts []string
	for idx, reason := range dead {
		parts = append(parts, fmt.Sprintf("#%d %s", idx+1, reason))
	}
	sort.Strings(parts)
	extra := ""
	if len(parts) > 0 {
		extra = " [" + strings.Join(parts, ", ") + "]"
	}
	hc.event("summary", fmt.Sprintf("total=%d live=%d quarantined=%d%s", len(p.clients), p.LiveCount(), len(dead), extra))
}

// EventQuarantine is the passive path: a 401/403 observed mid-request
// quarantines the account so later requests skip it.
func (hc *HealthChecker) EventQuarantine(idx int, reason string) {
	p := hc.pool
	if idx < 0 || idx >= len(p.clients) {
		return
	}
	if p.Quarantine(idx, reason) {
		hc.event("request", probeLabel(p, idx)+" quarantined: "+reason)
	}
}

// Start launches the background probe loops. liveEvery=0 disables periodic
// probing of all accounts; deadEvery=0 disables the quarantined re-check.
func (hc *HealthChecker) Start(ctx context.Context, liveEvery, deadEvery time.Duration) {
	if liveEvery > 0 {
		go func() {
			tick := time.NewTicker(liveEvery)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					hc.ProbeAll()
				}
			}
		}()
	}
	if deadEvery > 0 {
		go func() {
			tick := time.NewTicker(deadEvery)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					hc.RetryDead()
				}
			}
		}()
	}
}
