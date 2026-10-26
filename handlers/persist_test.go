package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xgetz/deepseek2api/config"
	"github.com/0xgetz/deepseek2api/deepseek"
)

// distinctAccountTokens returns pool tokens whose maskToken forms are
// pairwise unique (mask = first 6 + "…" + last 4, so tokens must be >12 chars
// with distinct tails).
func distinctAccountTokens(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("acct-%s-%04d", "alphabravocharliewhole"[i*6:i*6+6], i+1)
	}
	return out
}

func newPersistHandler(t *testing.T, accounts int, dead ...int) *Handler {
	t.Helper()
	pool := NewPool(distinctAccountTokens(accounts), 0, 0, 0)
	for _, d := range dead {
		pool.Quarantine(d, "test")
	}
	cfg := &config.Config{AccountQPM: 6, MaxConcurrentPerAccount: 5}
	return New(cfg, pool, NewConvStore(nil, 10, 0))
}

// ── account re-mapping ────────────────────────────────────────

func TestFindByMask(t *testing.T) {
	h := newPersistHandler(t, 3)
	tok := distinctAccountTokens(3)

	if idx := h.pool.FindByMask(maskToken(tok[1])); idx != 1 {
		t.Fatalf("FindByMask = %d, want 1", idx)
	}
	if idx := h.pool.FindByMask(maskToken("no-such-account-9999")); idx != -1 {
		t.Fatalf("unknown account must be -1, got %d", idx)
	}
	if idx := h.pool.FindByMask(""); idx != -1 {
		t.Fatalf("empty mask must be -1")
	}
	h.pool.Quarantine(2, "test")
	if idx := h.pool.FindByMask(maskToken(tok[2])); idx != -1 {
		t.Fatalf("quarantined account must not be returned, got %d", idx)
	}
}

// ── round trip: store → disk → new process ────────────────────

func TestPersistRoundTrip(t *testing.T) {
	h := newPersistHandler(t, 3)

	// a task living on account #2 (not the default first account)
	conv := &Conversation{
		Client:    h.pool.clients[1],
		SessionID: "sess-roundtrip",
		ModelID:   "deepseek-chat",
		Turn:      2,
		turnCount: 2,
		LastUsed:  time.Now(),
		ttl:       30 * time.Minute,
	}
	h.convs.Register("pk-roundtrip", conv) // bumps turnCount to 3
	h.convs.RegisterFingerprint("fp-roundtrip", conv)

	data, err := json.Marshal(persistFile{
		Version: 1,
		Entries: h.convs.Snapshot(),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "conversations.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// "new process": fresh handler with the same token pool
	h2 := newPersistHandler(t, 3)
	p := NewConvPersister(path, h2.convs.Snapshot, h2.restoreConvRecords)
	restored, dropped := p.Load()
	if dropped != 0 || restored != 1 {
		t.Fatalf("restored=%d dropped=%d, want 1/0", restored, dropped)
	}

	// every lookup key must resolve to the restored session
	if c := h2.convs.ByPrefix("pk-roundtrip"); c == nil || c.SessionID != "sess-roundtrip" {
		t.Fatal("prefix lookup lost after restore")
	}
	if c := h2.convs.ByFingerprint("fp-roundtrip"); c == nil || c.SessionID != "sess-roundtrip" {
		t.Fatal("fingerprint lookup lost after restore")
	}
	if c := h2.convs.BySession("sess-roundtrip"); c == nil {
		t.Fatal("session lookup lost after restore")
	}
	// the session must sit back on account #2 (its original account)
	if c := h2.convs.BySession("sess-roundtrip"); c.Client != h2.pool.clients[1] {
		t.Fatalf("restored session must keep its original account")
	}
	// and the prefix→account binding must be restored too (task a stays on A)
	h2.pendingMu.Lock()
	bound := h2.prefixToClientIdx["pk-roundtrip"]
	h2.pendingMu.Unlock()
	if bound != 1 {
		t.Fatalf("restored binding = %d, want 1 (original account)", bound)
	}
}

// ── restore filtering ─────────────────────────────────────────

func TestRestoreConvRecordsFiltering(t *testing.T) {
	h := newPersistHandler(t, 2)
	tok := distinctAccountTokens(2)

	entries := map[string]ConvRecord{
		"stale-key": {
			SessionID: "s-stale",
			Account:   maskToken(tok[0]),
			Expiry:    time.Now().Add(-time.Hour).Unix(), // idle TTL already lapsed
		},
		"gone-key": {
			SessionID: "s-gone",
			Account:   maskToken("acct-deleted-0001"), // account no longer in the pool
		},
		"good-key": {
			SessionID: "s-good",
			Account:   maskToken(tok[0]),
			LastUsed:  time.Now().Unix(),
		},
	}
	restored, dropped := h.restoreConvRecords(entries)
	if restored != 1 {
		t.Fatalf("restored = %d, want 1", restored)
	}
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2 (stale + gone)", dropped)
	}
	if c := h.convs.BySession("s-good"); c == nil || c.Client != h.pool.clients[0] {
		t.Fatal("good session must restore onto account 0")
	}
	if h.convs.BySession("s-stale") != nil || h.convs.BySession("s-gone") != nil {
		t.Fatal("filtered sessions must not be restored")
	}
}

// ── corruption recovery ───────────────────────────────────────

func TestPersisterCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conversations.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newPersistHandler(t, 1)
	p := NewConvPersister(path, h.convs.Snapshot, h.restoreConvRecords)
	restored, dropped := p.Load()
	if restored != 0 || dropped != 0 {
		t.Fatalf("corrupt file must load empty, got %d/%d", restored, dropped)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	quarantined := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && !strings.Contains(e.Name(), "corrupt") {
			quarantined++
		}
	}
	if quarantined != 0 {
		t.Fatal("corrupt file must be moved out of the way")
	}
}

// ── writer goroutine: flush semantics ─────────────────────────

func TestPersisterFlush(t *testing.T) {
	h := newPersistHandler(t, 1)
	conv := &Conversation{
		Client:    h.pool.clients[0],
		SessionID: "sess-flush",
		ModelID:   "deepseek-chat",
		LastUsed:  time.Now(),
		ttl:       time.Hour,
	}
	h.convs.Register("pk-flush", conv)

	path := filepath.Join(t.TempDir(), "sub", "conversations.json")
	p := NewConvPersister(path, h.convs.Snapshot, h.restoreConvRecords)
	p.Start()
	p.Close() // force-flush on shutdown

	var f persistFile
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("flush must write the file: %v", err)
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 {
		t.Fatalf("version = %d, want 1", f.Version)
	}
	if rec, ok := f.Entries["sess-flush"]; !ok || rec.SessionID != "sess-flush" {
		t.Fatalf("flushed entries missing the session: %v", f.Entries)
	}
}

// ── self-heal predicate ───────────────────────────────────────

func TestShouldSelfHeal(t *testing.T) {
	if !shouldSelfHeal(&deepseek.APIError{HTTPStatus: 404}, false, true, false, false) {
		t.Fatal("404 on a cached session must self-heal")
	}
	if !shouldSelfHeal(errors.New("network reset"), false, true, false, false) {
		t.Fatal("plain errors must self-heal")
	}
	for _, code := range []int{401, 403, 429} {
		if shouldSelfHeal(&deepseek.APIError{HTTPStatus: code}, false, true, false, false) {
			t.Fatalf("http %d must not self-heal", code)
		}
	}
	if shouldSelfHeal(errors.New("x"), true, true, false, false) {
		t.Fatal("streaming responses must not self-heal")
	}
	if shouldSelfHeal(errors.New("x"), false, false, false, false) {
		t.Fatal("disabled persistence must not self-heal")
	}
	if shouldSelfHeal(errors.New("x"), false, true, true, false) {
		t.Fatal("fresh sessions must not self-heal")
	}
	if shouldSelfHeal(errors.New("x"), false, true, false, true) {
		t.Fatal("client-gone must not self-heal")
	}
	if shouldSelfHeal(errRequestAborted, false, true, false, false) {
		t.Fatal("pre-completion aborts must not self-heal")
	}
	if shouldSelfHeal(nil, false, true, false, false) {
		t.Fatal("no error must not self-heal")
	}
}
