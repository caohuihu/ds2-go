package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/0xgetz/deepseek2api/config"
)

func newAuthHandler(t *testing.T, keys ...string) *Handler {
	t.Helper()
	pool := NewPool([]string{"token-a", "token-b"}, 0, 0, 0)
	cfg := &config.Config{AccountQPM: 6, MaxConcurrentPerAccount: 5, ProxyKeys: keys}
	h := New(cfg, pool, NewConvStore(nil, 10, 0))
	return h
}

// TestAuthMultipleKeys: every configured key is accepted; anything else 401s.
func TestAuthMultipleKeys(t *testing.T) {
	h := newAuthHandler(t, "keyA", "keyB", "keyC")

	for _, k := range []string{"keyA", "keyB", "keyC"} {
		rr := httptest.NewRecorder()
		h.Auth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })(rr, newReq(t, "Bearer "+k, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("key %q must be accepted, got %d", k, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	h.Auth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })(rr, newReq(t, "Bearer no-such-key", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unknown key must be rejected, got %d", rr.Code)
	}
}

// TestClientKeyIDFromContext: the matched key's stable id rides the context.
func TestClientKeyIDFromContext(t *testing.T) {
	h := newAuthHandler(t, "keyA", "keyB")
	var got string
	h.Auth(func(w http.ResponseWriter, r *http.Request) { got = clientKeyID(r.Context()) })(
		httptest.NewRecorder(), newReq(t, "Bearer keyB", nil))
	if got != keyIDOf("keyB") {
		t.Fatalf("context keyID = %q, want %q", got, keyIDOf("keyB"))
	}
	// a request that did not go through Auth has an empty keyID
	if clientKeyID(nil) != "" {
		t.Fatal("nil context must yield empty keyID")
	}
}

// TestKeyIDStableAcrossOrdering: keyID depends only on the key value, so
// reordering the keys file cannot change it (persistence stays valid).
func TestKeyIDStableAcrossOrdering(t *testing.T) {
	a := newAuthHandler(t, "alpha", "beta")
	b := newAuthHandler(t, "beta", "alpha")
	if a.keyIDs["alpha"] != b.keyIDs["alpha"] || a.keyIDs["beta"] != b.keyIDs["beta"] {
		t.Fatal("keyID must be independent of key ordering")
	}
	if len(a.keyIDs["alpha"]) != 16 {
		t.Fatalf("keyID should be 16 hex chars, got %d", len(a.keyIDs["alpha"]))
	}
}

func newReq(t *testing.T, auth string, _ map[string]string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}
