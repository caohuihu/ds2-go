package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	rand2 "math/rand"
	"sync"
	"time"

	"github.com/0xgetz/deepseek2api/deepseek"
)

// Conversation is one live DeepSeek session plus the bookkeeping needed to
// continue it (the last assistant message id and cumulative usage).
// Each conversation carries its own random TTL so idle-session cleanup is
// unpredictable — a hard uniform TTL is a machine fingerprint.
type Conversation struct {
	mu        sync.Mutex
	Client    *deepseek.Client
	SessionID string
	ParentID  int64
	HasParent bool
	Turn      int
	LastUsage int64
	HasUsage  bool
	LastUsed  time.Time
	ttl       time.Duration // per-conversation TTL; 0 = never delete
	ModelID   string        // the model this session was created for
	turnCount int           // number of requests served by this session (0 = not yet registered)
}

// Lock guards the conversation for one completion round trip.
func (c *Conversation) Lock() { c.mu.Lock() }
func (c *Conversation) Unlock() {
	c.LastUsed = time.Now()
	c.mu.Unlock()
}

// ConvStore maps derived conversation keys and DeepSeek session ids to
// conversations, and removes idle sessions upstream after each session's own
// randomized TTL.
type ConvStore struct {
	ttlOptions         []time.Duration // possible TTL values; empty duration means "never delete"
	maxSize            int
	requestsPerSession int // max requests per session before forcing a new one (0 = disabled)
	mu                 sync.Mutex
	byPrefix           map[string]*Conversation
	bySession          map[string]*Conversation
	byFingerprint      map[string]*Conversation // conversation-opening fingerprint -> conversation
}

func NewConvStore(ttlOptions []time.Duration, maxSize, requestsPerSession int) *ConvStore {
	return &ConvStore{
		ttlOptions:         ttlOptions,
		maxSize:            maxSize,
		requestsPerSession: requestsPerSession,
		byPrefix:           make(map[string]*Conversation),
		bySession:          make(map[string]*Conversation),
		byFingerprint:      make(map[string]*Conversation),
	}
}

// pickRandomTTL returns a random TTL from the configured options.
// An empty duration in the slice represents "never delete".
func (s *ConvStore) pickRandomTTL() time.Duration {
	if len(s.ttlOptions) == 0 {
		return 30 * time.Minute
	}
	return s.ttlOptions[rand2.Intn(len(s.ttlOptions))]
}

func (s *ConvStore) ByPrefix(key string) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byPrefix[key]
}

// ByFingerprint looks up the conversation for a conversation-opening
// fingerprint (see convFingerprint).
func (s *ConvStore) ByFingerprint(key string) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byFingerprint[key]
}

// Evict removes a conversation from both maps. Call this when you want to
// force the next request for the same session to create a fresh one.
func (s *ConvStore) Evict(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.bySession[sessionID]; ok {
		s.removeLocked(sessionID, c)
		log.Printf("chat: evicted session %s from cache", sessionID[:8])
	}
}

// RecentForModel returns the most recently used conversation for the given
// model, or nil if no conversation exists for that model yet. This is used
// as a fallback so that a client which sends only its latest message (without
// full history) still continues the same DeepSeek session across turns.
func (s *ConvStore) RecentForModel(modelID string) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Conversation
	for _, c := range s.bySession {
		if c.ModelID != modelID {
			continue // skip conversations for other models
		}
		if best == nil || c.LastUsed.After(best.LastUsed) {
			best = c
		}
	}
	return best
}

func (s *ConvStore) BySession(sessionID string) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bySession[sessionID]
}

// Register stores a conversation under a derived prefix key; the session-id
// mapping is kept in every case so conversation_id passthrough keeps working.
func (s *ConvStore) Register(prefixKey string, conv *Conversation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Assign a random TTL on first registration (fresh conversation).
	if conv.ttl == 0 {
		conv.ttl = s.pickRandomTTL()
	}
	// Increment turn count. When it exceeds the configured limit, evict
	// this conversation so the next request will create a fresh session.
	conv.turnCount++
	if s.requestsPerSession > 0 && conv.turnCount > s.requestsPerSession {
		log.Printf("chat: session %s reached max turns (%d), evicting", conv.SessionID, s.requestsPerSession)
		s.removeLocked(conv.SessionID, conv)
		return
	}
	s.bySession[conv.SessionID] = conv
	if prefixKey != "" {
		s.byPrefix[prefixKey] = conv
	}
	for len(s.bySession) > s.maxSize {
		var oldestKey string
		var oldest *Conversation
		for key, c := range s.bySession {
			if oldest == nil || c.LastUsed.Before(oldest.LastUsed) {
				oldestKey, oldest = key, c
			}
		}
		s.removeLocked(oldestKey, oldest)
	}
}

// removeLocked drops a conversation from both maps; callers hold s.mu.
func (s *ConvStore) removeLocked(sessionKey string, conv *Conversation) {
	delete(s.bySession, sessionKey)
	for k, v := range s.byPrefix {
		if v == conv {
			delete(s.byPrefix, k)
		}
	}
	for k, v := range s.byFingerprint {
		if v == conv {
			delete(s.byFingerprint, k)
		}
	}
}

// RegisterFingerprint stores a conversation under its conversation-opening
// fingerprint. Unlike Register it does not bump the per-session request
// counter or assign a TTL — the conversation is already tracked in
// bySession; this only adds a second lookup key.
func (s *ConvStore) RegisterFingerprint(key string, conv *Conversation) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byFingerprint[key] = conv
}

// SweepLoop deletes DeepSeek sessions that have been idle past the TTL so the
// account's session list stays clean.
func (s *ConvStore) SweepLoop(ctx context.Context) {
	// Use 1/6 of the shortest non-zero TTL as the sweep interval,
	// but never less than 1 minute so we don't hammer upstream.
	minTTL := time.Hour * 24
	for _, d := range s.ttlOptions {
		if d > 0 && d < minTTL {
			minTTL = d
		}
	}
	if minTTL == time.Hour*24 {
		minTTL = time.Hour // fallback if all options are "never"
	}
	interval := minTTL / 6
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepOnce(ctx)
		}
	}
}

func (s *ConvStore) sweepOnce(ctx context.Context) {
	s.mu.Lock()
	var expired []*Conversation
	for key, c := range s.bySession {
		// ttl == 0 means "never delete".
		if c.ttl == 0 {
			continue
		}
		if time.Since(c.LastUsed) >= c.ttl {
			expired = append(expired, c)
			s.removeLocked(key, c)
		}
	}
	s.mu.Unlock()

	for _, conv := range expired {
		dctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := conv.Client.DeleteSession(dctx, conv.SessionID)
		cancel()
		if err != nil {
			log.Printf("conversation cleanup: delete session %s failed: %v", conv.SessionID, err)
			// Queue for a retry on the next sweep; do not hammer upstream.
			s.mu.Lock()
			conv.LastUsed = time.Now().Add(-conv.ttl)
			s.bySession[conv.SessionID] = conv
			s.mu.Unlock()
		}
		// The upstream API rate-limits bursts of session operations.
		time.Sleep(1500 * time.Millisecond)
	}
}

// PrefixKey derives the cache key for "messages except the last one", so a
// client that appends one exchange per request keeps hitting the same
// DeepSeek session. The model is part of the key: switching models starts a
// new session, matching DeepSeek's per-session model. Image references are
// fingerprinted too, so image turns cache correctly. Assistant tool_calls
// and tool message ids are canonicalized into the key so agent tool loops
// map back onto the same session.
func PrefixKey(messages []chatMessage, model string) string {
	h := sha256.New()
	for _, m := range messages {
		h.Write([]byte(m.Role))
		h.Write([]byte{0x1f})
		h.Write([]byte(contentFingerprint(m.Content)))
		h.Write([]byte{0x1e})
		h.Write([]byte(canonicalToolCalls(m.ToolCalls)))
		h.Write([]byte{0x1e})
		h.Write([]byte(m.ToolCallID))
		h.Write([]byte{0x1e})
	}
	h.Write([]byte{0x00})
	h.Write([]byte(model))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// convFingerprint identifies a client-side conversation by its opening:
// system + first user message + first assistant reply + model. Used when
// CONV_FINGERPRINT_ROUTE is enabled:
//
//   - the first assistant reply anchors the key (like webai2api), so two
//     conversations with the same opening text but different replies never
//     collide;
//   - a first-turn request (no assistant yet) computes a fingerprint that
//     can never match a stored one (stored keys always carry the anchor),
//     which is exactly what makes a new task start a fresh session;
//   - the first assistant is taken in canonical tool_calls form so client
//     re-serialization cannot break the match.
func convFingerprint(messages []chatMessage, model string) string {
	var system, firstUser, firstAsst string
	for _, m := range messages {
		switch m.Role {
		case "system", "developer":
			if system == "" {
				system = contentText(m.Content)
			}
		case "user":
			if firstUser == "" {
				firstUser = contentFingerprint(m.Content)
			}
		case "assistant":
			if firstAsst == "" {
				if c := canonicalToolCalls(m.ToolCalls); c != "" {
					firstAsst = c
				} else {
					firstAsst = contentFingerprint(m.Content)
				}
			}
		}
	}
	// Bounded prefixes keep the key stable under cosmetic client prompt drift.
	return fingerprintHash(system, firstUser, firstAsst, model)
}

func fingerprintHash(system, firstUser, firstAsst, model string) string {
	bound := func(s string, n int) string {
		if len(s) > n {
			return s[:n]
		}
		return s
	}
	h := sha256.New()
	h.Write([]byte(bound(system, 200)))
	h.Write([]byte{0x00})
	h.Write([]byte(bound(firstUser, 500)))
	h.Write([]byte{0x00})
	h.Write([]byte(bound(firstAsst, 200)))
	h.Write([]byte{0x00})
	h.Write([]byte(model))
	return hex.EncodeToString(h.Sum(nil)[:16])
}
