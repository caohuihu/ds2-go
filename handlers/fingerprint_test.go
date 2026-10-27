package handlers

import (
	"encoding/json"
	"testing"

	"github.com/0xgetz/deepseek2api/config"
)

func taskMessages(withAssistant bool) []chatMessage {
	msgs := []chatMessage{
		{Role: "system", Content: json.RawMessage(`"you are a bot"`)},
		{Role: "user", Content: json.RawMessage(`"write a diary"`)},
	}
	if withAssistant {
		msgs = append(msgs,
			chatMessage{Role: "assistant", Content: json.RawMessage(`"ok, starting"`)},
			chatMessage{Role: "user", Content: json.RawMessage(`"make it longer"`)},
		)
	}
	return msgs
}

// ── convFingerprint stability ─────────────────────────────────

func TestConvFingerprintStableAcrossTurns(t *testing.T) {
	// turn 1 (no assistant) registers an anchor-less key; turn 2 (assistant
	// present) must compute the same fingerprint the store registered after
	// turn 1.
	a1 := chatMessage{Role: "assistant", Content: json.RawMessage(`"ok, starting"`)}
	turn1 := append(taskMessages(false), a1) // what we register after turn 1
	turn2 := taskMessages(true)              // what the client sends next
	if convFingerprint(turn1, "deepseek-chat", "key1") != convFingerprint(turn2, "deepseek-chat", "key1") {
		t.Fatal("fingerprint must be stable across turns of one task")
	}
}

func TestConvFingerprintDiffers(t *testing.T) {
	base := taskMessages(true)
	otherUser := taskMessages(true)
	otherUser[1].Content = json.RawMessage(`"a completely different task"`)

	if convFingerprint(base, "deepseek-chat", "key1") == convFingerprint(otherUser, "deepseek-chat", "key1") {
		t.Fatal("different first user must give different fingerprints")
	}
	if convFingerprint(base, "deepseek-chat", "key1") == convFingerprint(base, "deepseek-reasoner", "key1") {
		t.Fatal("different model must give different fingerprints")
	}

	// two tasks with the same opening text but different first replies
	otherAsst := taskMessages(true)
	otherAsst[2].Content = json.RawMessage(`"different first reply"`)
	if convFingerprint(base, "deepseek-chat", "key1") == convFingerprint(otherAsst, "deepseek-chat", "key1") {
		t.Fatal("first-reply anchor must separate same-opening tasks")
	}
}

func TestKeyIsolatesFingerprints(t *testing.T) {
	base := taskMessages(true)
	// identical content but different clients (keyID) must NOT collide
	if convFingerprint(base, "deepseek-chat", "keyA") == convFingerprint(base, "deepseek-chat", "keyB") {
		t.Fatal("distinct proxy keys must give different fingerprints (client isolation)")
	}
	if PrefixKey(base[:1], "deepseek-chat", "keyA") == PrefixKey(base[:1], "deepseek-chat", "keyB") {
		t.Fatal("distinct proxy keys must give different prefix keys (client isolation)")
	}
	// the same key is stable regardless of how many keys the handler knows
	if keyIDOf("some-key") != keyIDOf("some-key") {
		t.Fatal("keyIDOf must be deterministic")
	}
}

func TestConvFingerprintToolCallsCanonical(t *testing.T) {
	// assistant with tool_calls: fingerprint uses the canonical form, so the
	// client re-serializing the echoed tool_calls (key order, arguments as
	// embedded object) must not break the match.
	canonical := json.RawMessage(`[{"id":"c1","type":"function","function":{"name":"bash","arguments":"{}"}}]`)
	reshaped := json.RawMessage(`[{"function":{"arguments":{},"name":"bash"},"type":"function","id":"c1"}]`)

	a := []chatMessage{
		{Role: "user", Content: json.RawMessage(`"hi"`)},
		{Role: "assistant", ToolCalls: canonical},
	}
	b := []chatMessage{
		{Role: "user", Content: json.RawMessage(`"hi"`)},
		{Role: "assistant", ToolCalls: reshaped},
	}
	if convFingerprint(a, "deepseek-chat", "key1") != convFingerprint(b, "deepseek-chat", "key1") {
		t.Fatal("fingerprint must be stable across tool_calls re-serialization")
	}
}

// ── ConvStore fingerprint map ─────────────────────────────────

func TestConvStoreFingerprint(t *testing.T) {
	s := NewConvStore(nil, 10, 0)
	conv := &Conversation{SessionID: "sess-0001"}
	s.Register("", conv)
	s.RegisterFingerprint("fp-1", conv)

	if c := s.ByFingerprint("fp-1"); c != conv {
		t.Fatal("fingerprint lookup must return the conversation")
	}
	if s.ByFingerprint("fp-other") != nil {
		t.Fatal("unknown fingerprint must miss")
	}
	// evicting the conversation must drop its fingerprint entry
	s.Evict("sess-0001")
	if s.ByFingerprint("fp-1") != nil {
		t.Fatal("fingerprint entry must be cleaned up on eviction")
	}
}

// ── round-robin binding (the 3-line bug fix) ──────────────────

func newTestHandler(t *testing.T, accounts int, dead ...int) *Handler {
	t.Helper()
	tokens := make([]string, accounts)
	for i := range tokens {
		tokens[i] = "token-" + string(rune('a'+i))
	}
	pool := NewPool(tokens, 0, 0, 0)
	for _, d := range dead {
		pool.Quarantine(d, "test")
	}
	cfg := &config.Config{AccountQPM: 6, MaxConcurrentPerAccount: 5}
	h := New(cfg, pool, NewConvStore(nil, 10, 0))
	return h
}

func TestResolveBoundAccountRoundRobin(t *testing.T) {
	h := newTestHandler(t, 3)
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()

	// three fresh keys take three different accounts in rotation
	got := map[string]bool{}
	for _, k := range []string{"k1", "k2", "k3"} {
		idx := h.resolveBoundAccount(k)
		if idx < 0 {
			t.Fatalf("no live account for %s", k)
		}
		got[string(rune('0'+idx))+k] = true
	}
	if len(got) != 3 {
		t.Fatalf("round-robin must spread fresh keys across accounts, got %v", got)
	}

	// a bound key keeps its account on the next call (no counter advance)
	again := h.resolveBoundAccount("k1")
	if first := h.resolveBoundAccount("k1"); first != again {
		t.Fatalf("bound key must keep its account: %d vs %d", first, again)
	}
}

func TestResolveBoundAccountSkipsDead(t *testing.T) {
	h := newTestHandler(t, 3, 0) // account 0 quarantined
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()

	seen := map[int]int{}
	for i := 0; i < 8; i++ {
		idx := h.resolveBoundAccount("fresh-key-" + string(rune('0'+i)))
		if idx < 0 {
			t.Fatal("live accounts exist")
		}
		seen[idx]++
		if idx == 0 {
			t.Fatal("quarantined account must never be picked")
		}
	}
	if len(seen) != 2 {
		t.Fatalf("only the two live accounts may be offered, got %v", seen)
	}

	// binding a prefix to a live account survives; a binding to the dead one
	// is rebound to a live account
	h.prefixToClientIdx["alive-key"] = 1
	if idx := h.resolveBoundAccount("alive-key"); idx != 1 {
		t.Fatalf("alive binding must be kept, got %d", idx)
	}
	h.prefixToClientIdx["stale-key"] = 0
	if idx := h.resolveBoundAccount("stale-key"); idx == 0 {
		t.Fatal("binding to a dead account must be rebound to a live one")
	}
}

func TestResolveBoundAccountAllDead(t *testing.T) {
	h := newTestHandler(t, 2, 0, 1)
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	if idx := h.resolveBoundAccount("k"); idx != -1 {
		t.Fatal("all accounts dead must report no account")
	}
}
