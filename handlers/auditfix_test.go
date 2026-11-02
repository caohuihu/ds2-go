package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xgetz/deepseek2api/deepseek"
)

// fakeCreateUpstream returns an httptest server answering
// /api/v0/chat_session/create with a valid envelope carrying sessionID.
// Other paths get a plain 500.
func fakeCreateUpstream(sessionID string, delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v0/chat_session/create":
			w.Write([]byte(`{"code":0,"msg":"","data":{"biz_code":0,"biz_msg":"","biz_data":{"chat_session":{"id":"` + sessionID + `"}}}}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"code":0,"msg":"not found"}`))
		}
	}))
}

// ── 1. empty messages must 400, not panic the process ─────────

func TestChatCompletionRejectsEmptyMessages(t *testing.T) {
	h := newPersistHandler(t, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be contacted for an empty messages request")
	}))
	defer srv.Close()
	h.pool.clients[0].BaseURL = srv.URL

	// "messages": [] and omitted (zero-length) both used to underflow the
	// req.Messages[:len(req.Messages)-1] slice and panic the whole process.
	for _, body := range []string{
		`{"model":"deepseek-chat","messages":[]}`,
		`{"model":"deepseek-chat"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ChatCompletion(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400 (body %s)", body, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "messages must not be empty") {
			t.Fatalf("body %s: want 'messages must not be empty', got %s", body, rec.Body.String())
		}
	}
}

// ── 2. coalesceOrCreate must broadcast to every coalesced waiter ──

// With the old buffered-channel + uncancelable-context entry, the first
// waiter swallowed the single result and every other coalesced request
// blocked forever. All waiters must get the SAME conversation (or error).
func TestCoalesceOrCreateBroadcastsToAllWaiters(t *testing.T) {
	h := newPersistHandler(t, 1)
	srv := fakeCreateUpstream("sess-broadcast", 300*time.Millisecond) // force overlap
	defer srv.Close()
	h.pool.clients[0].BaseURL = srv.URL

	const n = 4
	results := make([]*Conversation, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = h.coalesceOrCreate("shared-prefix-key", "deepseek-chat")
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("waiter %d: unexpected error %v", i, errs[i])
		}
		if results[i] == nil {
			t.Fatalf("waiter %d: nil conversation", i)
		}
		if results[i] != results[0] {
			t.Fatalf("waiter %d got a different conversation than waiter 0 — coalescing broken", i)
		}
	}
	if results[0].SessionID != "sess-broadcast" {
		t.Fatalf("session id = %q, want %q", results[0].SessionID, "sess-broadcast")
	}
}

// The failure path must broadcast the error too (no waiter may hang).
func TestCoalesceOrCreateFailureBroadcast(t *testing.T) {
	h := newPersistHandler(t, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"code":0,"msg":"boom"}`))
	}))
	defer srv.Close()
	h.pool.clients[0].BaseURL = srv.URL

	const n = 3
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = h.coalesceOrCreate("fail-key", "deepseek-chat")
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] == nil {
			t.Fatalf("waiter %d: expected an error from the failed create, got nil", i)
		}
	}
}

// ── 3. daily quota counters must reset for Count across midnight ──

func TestUsageCountResetsOnDayRollover(t *testing.T) {
	u := NewUsageTracker("") // in-memory only
	u.Use("acct-a")
	u.Use("acct-a")

	// Simulate the process having crossed midnight: the tracker still holds
	// yesterday's day and yesterday's totals.
	u.mu.Lock()
	u.day = "1999-01-01"
	u.counts = map[string]int{"acct-a": 100}
	u.mu.Unlock()

	// Count() — used by dailyExhausted for NEW sessions — must observe the
	// rollover, or a fully-quotaed pool would 503 until some cached request
	// happened to Use() an account first.
	if got := u.Count("acct-a"); got != 0 {
		t.Fatalf("Count after day rollover = %d, want 0 (stale counter)", got)
	}
	if got := u.Use("acct-a"); got != 1 {
		t.Fatalf("Use after day rollover = %d, want 1", got)
	}
}

// ── 4. sweep delete failure must re-link EVERY lookup key ──────

func TestSweepDeleteFailureRelinksAllKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // every delete fails
		w.Write([]byte(`{"code":0,"msg":"denied"}`))
	}))
	defer srv.Close()
	client := deepseek.NewClient("acct-sweeper-0001", 0, 0, 0)
	client.BaseURL = srv.URL

	// 30ms TTL so the session is expired the moment sweep runs.
	s := NewConvStore([]time.Duration{30 * time.Millisecond}, 10, 0)
	conv := &Conversation{Client: client, SessionID: "sess-sweep", ModelID: "deepseek-chat"}
	conv.lastUsed.Store(time.Now().Add(-time.Hour).Unix())
	s.Register("pk-sweep", conv)
	s.RegisterFingerprint("fp-sweep", conv)
	if s.ByPrefix("pk-sweep") == nil || s.ByFingerprint("fp-sweep") == nil {
		t.Fatal("setup: keys must be registered before the sweep")
	}

	s.sweepOnce(context.Background())

	// The delete failed, so the session stays retryable; requests must keep
	// routing to it (no fresh upstream session + full history replay).
	if s.BySession("sess-sweep") != conv {
		t.Fatal("bySession not re-linked after failed delete")
	}
	if s.ByPrefix("pk-sweep") != conv {
		t.Fatal("byPrefix not re-linked after failed delete (would orphan the session)")
	}
	if s.ByFingerprint("fp-sweep") != conv {
		t.Fatal("byFingerprint not re-linked after failed delete (would orphan the session)")
	}
}

// A key that a concurrent request re-binds to a DIFFERENT session while
// the delete is in flight must never be overwritten by the sweep re-link.
func TestSweepRelinkNeverClobersReboundKey(t *testing.T) {
	var started sync.Once
	deleteSeen := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Do(func() { close(deleteSeen) })
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"code":0,"msg":"denied"}`))
	}))
	defer srv.Close()
	client := deepseek.NewClient("acct-sweeper-0003", 0, 0, 0)
	client.BaseURL = srv.URL

	s := NewConvStore([]time.Duration{30 * time.Millisecond}, 10, 0)
	conv := &Conversation{Client: client, SessionID: "sess-a", ModelID: "m"}
	conv.lastUsed.Store(time.Now().Add(-time.Hour).Unix())
	s.Register("pk-shared", conv) // at expiry time the key points at sess-a

	other := &Conversation{Client: client, SessionID: "sess-b", ModelID: "m"}
	other.lastUsed.Store(time.Now().Unix())

	// While the failing delete is in flight, a concurrent request re-binds
	// the prefix to a fresh session; the re-link guard must not clobber it.
	go func() {
		<-deleteSeen
		s.mu.Lock()
		s.byPrefix["pk-shared"] = other
		s.mu.Unlock()
	}()

	s.sweepOnce(context.Background())

	if got := s.ByPrefix("pk-shared"); got != other {
		t.Fatalf("ByPrefix(pk-shared) = %s, want sess-b (guard must not clobber the rebind)", got.SessionID)
	}
	if c := s.BySession("sess-a"); c == nil {
		t.Fatal("bySession must still be re-linked for the delete retry")
	}
}

// ── 5. growing the semaphore must not deadlock with in-flight slots ──

func TestLimiterGrowSemSwapNoDeadlock(t *testing.T) {
	l := NewAccountLimiter(100)
	hold := l.Wait("dev-1", 100, 1) // hold the only slot of a cap-1 bucket
	if got := l.InFlight("dev-1"); got != 1 {
		t.Fatalf("InFlight = %d, want 1", got)
	}

	// Grow to cap 5 while one slot is in flight. The old blocking drain of
	// the old channel deadlocked here (its release goes to the NEW
	// channel, so the old one never refills).
	done := make(chan struct{})
	go func() {
		rel := l.Wait("dev-1", 100, 5)
		rel() // release back before signaling, so the count is settled
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("growing the semaphore with an in-flight slot deadlocks")
	}
	// The goroutine's slot was re-issued into the new channel, so only the
	// held slot still counts.
	if got := l.InFlight("dev-1"); got != 1 {
		t.Fatalf("InFlight after grow+release = %d, want 1", got)
	}
	hold() // release the first slot: it re-issues into the new channel
	if got := l.InFlight("dev-1"); got != 0 {
		t.Fatalf("InFlight after all releases = %d, want 0", got)
	}
}
