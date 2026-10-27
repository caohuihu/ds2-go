package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	rand2 "math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0xgetz/deepseek2api/config"
	"github.com/0xgetz/deepseek2api/deepseek"
)

// ModelInfo maps a served model id to DeepSeek web parameters.
type ModelInfo struct {
	ID       string
	Aliases  []string
	Thinking bool
	Search   bool
}

var models = []ModelInfo{
	{ID: "deepseek-chat", Aliases: []string{"deepseek-v3"}},
	{ID: "deepseek-reasoner", Aliases: []string{"deepseek-r1"}, Thinking: true},
	{ID: "deepseek-search", Search: true},
	{ID: "deepseek-reasoner-search", Thinking: true, Search: true},
}

func lookupModel(name string) (*ModelInfo, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for i := range models {
		if models[i].ID == name {
			return &models[i], true
		}
		for _, a := range models[i].Aliases {
			if a == name {
				return &models[i], true
			}
		}
	}
	return nil, false
}

// resolveModel matches a requested model id, falling back to heuristics for
// unknown deepseek-* names so downstream clients that hardcode future or
// renamed ids (deepseek-v4.1-flash & co) keep working.
//
// Heuristic rules (ordered priority):
//  1. Exact alias match → use the named model as-is
//  2. Contains "reasoner" AND "search" → deepseek-reasoner-search
//  3. Contains "reasoner" or "think" or "-r1" → deepseek-reasoner
//  4. Contains "search" → deepseek-search
//  5. Otherwise → deepseek-chat (default)
func resolveModel(name string) (*ModelInfo, bool) {
	if m, ok := lookupModel(name); ok {
		return m, true
	}
	lower := strings.ToLower(name)
	if !strings.HasPrefix(lower, "deepseek") {
		return nil, false
	}
	// Heuristic: check for combined model FIRST, then individual flags.
	// This prevents deepseek-search from being matched as deepseek-reasoner-search.
	if strings.Contains(lower, "reasoner") && strings.Contains(lower, "search") {
		return &models[3], true // deepseek-reasoner-search
	}
	if strings.Contains(lower, "reasoner") || strings.Contains(lower, "think") || strings.Contains(lower, "-r1") {
		return &models[1], true // deepseek-reasoner
	}
	if strings.Contains(lower, "search") {
		return &models[2], true // deepseek-search
	}
	// Fallback: default model
	return &models[0], true // deepseek-chat
}

// AccountPool round-robins the configured DeepSeek tokens.
type AccountPool struct {
	clients []*deepseek.Client
	next    atomic.Uint64
	// mu protects dead: quarantined account index → reason. Quarantine
	// happens only on upstream auth failures (401/403), never on 429, 5xx
	// or network errors. See health.go for the state-machine methods.
	// It also protects rateHits (429 escalations) and cooldownUntil
	// (account-level rate backoff) and banUntil (BAN hold).
	mu            sync.Mutex
	dead          map[int]string
	rateHits      []int
	cooldownUntil []time.Time
	banUntil      []time.Time
}

func NewPool(tokens []string, powJitterMin, powJitterMax, rotateInterval time.Duration) *AccountPool {
	p := &AccountPool{
		dead:          make(map[int]string),
		rateHits:      make([]int, len(tokens)),
		cooldownUntil: make([]time.Time, len(tokens)),
		banUntil:      make([]time.Time, len(tokens)),
	}
	for _, t := range tokens {
		p.clients = append(p.clients, deepseek.NewClient(t, powJitterMin, powJitterMax, rotateInterval))
	}
	return p
}

func (p *AccountPool) Pick() *deepseek.Client {
	return p.clients[p.next.Add(1)%uint64(len(p.clients))]
}

// Handler serves the OpenAI-compatible API.
type Handler struct {
	cfg     *config.Config
	pool    *AccountPool
	convs   *ConvStore
	limiter *AccountLimiter
	// health is optional; when set, 401/403 in the request path quarantine
	// the offending account and are recorded in the health log file.
	health *HealthChecker
	// persister is optional; when set, the conversation index is snapshotted
	// to the local data file (PERSIST_SESSIONS) so tasks survive a restart.
	persister *ConvPersister
	// keyIDs maps every accepted proxy API key to its stable id, which is
	// mixed into the conversation keys so distinct clients (distinct keys)
	// can never share a web session.
	keyIDs map[string]string
	// usage is optional; when set (DAILY_REQUEST_LIMIT > 0) per-account
	// request counters are kept (and persisted) so a fresh account rotation
	// respects each account's daily quota.
	usage *UsageTracker
	mu    sync.Mutex // protects lastRequestAt
	// lastRequestAt tracks the time of the last request per client pointer,
	// enforcing a cooldown to simulate a human reading the reply.
	lastRequestAt map[*deepseek.Client]time.Time

	// pendingMu protects pending and prefixToClientIdx so concurrent requests
	// for the same conversation prefix deduplicate instead of each spawning
	// their own upstream session.
	pendingMu sync.Mutex
	pending   map[string]*pendingEntry // key = PrefixKey (prompt hash)
	// prefixToClientIdx binds a conversation prefix to a specific account
	// index so that all requests sharing that prefix always use the same
	// DeepSeek account — keeping the conversation context intact.
	prefixToClientIdx map[string]int
}

// pendingEntry represents an in-flight session creation for one prompt prefix.
// Waiters block on the done channel until the session is created (or cancelled).
type pendingEntry struct {
	done chan *Conversation
	ctx  context.Context
}

func New(cfg *config.Config, pool *AccountPool, convs *ConvStore) *Handler {
	h := &Handler{
		cfg:               cfg,
		pool:              pool,
		convs:             convs,
		limiter:           NewAccountLimiter(cfg.AccountQPM),
		lastRequestAt:     make(map[*deepseek.Client]time.Time),
		pending:           make(map[string]*pendingEntry),
		prefixToClientIdx: make(map[string]int),
		keyIDs:            make(map[string]string, len(cfg.ProxyKeys)),
	}
	for _, k := range cfg.ProxyKeys {
		h.keyIDs[k] = keyIDOf(k)
	}
	// Start background goroutine for periodic DeviceID rotation.
	if cfg.DeviceIDRotateHours > 0 {
		go h.startDeviceIDRotation()
	}
	return h
}

// keyIDOf returns a stable, order-independent identifier for a proxy API key
// (first 8 bytes of sha256). It is what joins the conversation keys, so the
// same key always yields the same id across restarts and reordering of the
// keys file, while distinct keys yield distinct ids.
func keyIDOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// SetHealthChecker attaches the optional token-health checker used to
// quarantine accounts that fail upstream auth checks.
func (h *Handler) SetHealthChecker(hc *HealthChecker) {
	h.health = hc
}

// SetPersister attaches the optional conversation persister used to snapshot
// the session index to disk (PERSIST_SESSIONS).
func (h *Handler) SetPersister(p *ConvPersister) {
	h.persister = p
}

// SetUsageTracker attaches the optional per-account daily quota tracker
// (DAILY_REQUEST_LIMIT). When present, fresh account rotation skips accounts
// that already hit their daily budget today.
func (h *Handler) SetUsageTracker(u *UsageTracker) {
	h.usage = u
}

// persistMark requests a throttled persistence write. Safe to call from any
// goroutine, including store-locked paths (the trigger send is non-blocking).
func (h *Handler) persistMark() {
	if h.persister != nil {
		h.persister.Mark()
	}
}

// WirePersistence builds the conversation persister for path, loads the
// on-disk index (restoring tasks that survived the last shutdown), and wires
// every store removal to a re-persist. Call before serving; Close() the
// returned persister on shutdown to flush the final snapshot.
func (h *Handler) WirePersistence(path string) *ConvPersister {
	p := NewConvPersister(path, h.convs.Snapshot, h.restoreConvRecords)
	restored, dropped := p.Load()
	h.SetPersister(p)
	h.convs.SetRemoveHook(h.persistMark)
	p.Start()
	log.Printf("persist: session index %s (restored %d sessions, dropped %d stale entries)", path, restored, dropped)
	return p
}

// startDeviceIDRotation periodically rotates DeviceIDs across all accounts
// when DEVICE_ID_ROTATE_HOURS is configured. This reduces long-term
// fingerprint similarity for each account.
func (h *Handler) startDeviceIDRotation() {
	interval := time.Duration(h.cfg.DeviceIDRotateHours) * time.Hour
	log.Printf("device ID rotation enabled: every %s", interval)
	for range time.Tick(interval) {
		for _, c := range h.pool.clients {
			c.RotateDeviceID()
		}
		log.Printf("device IDs rotated (%d accounts)", len(h.pool.clients))
	}
}

// ---- OpenAI wire types ----

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Stream         bool          `json:"stream"`
	ConversationID string        `json:"conversation_id"` // non-standard: continue a session
	StreamOptions  *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Tools          json.RawMessage `json:"tools,omitempty"`
	ToolChoice     json.RawMessage `json:"tool_choice,omitempty"`
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

type openAIError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code,omitempty"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, errType, msg string) {
	var e openAIError
	e.Error.Message = msg
	e.Error.Type = errType
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(e)
}

// clientKeyIDKey is the context key carrying the authenticated client's key id.
type clientKeyIDKey struct{}

// clientKeyID returns the stable id of the proxy API key this request
// authenticated with, or "" when the request did not go through Auth.
func clientKeyID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(clientKeyIDKey{}).(string)
	return id
}

// Auth guards /v1/* with the accepted proxy API keys (PROXY_API_KEY +
// PROXY_KEYS_FILE). The matching key's stable id is attached to the request
// context so downstream conversation keys can isolate distinct clients.
func (h *Handler) Auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			key = strings.TrimPrefix(auth, "Bearer ")
		}
		if key == "" {
			key = r.Header.Get("X-Api-Key")
		}
		id, ok := h.keyIDs[key]
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid proxy API key")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), clientKeyIDKey{}, id))
		next(w, r)
	}
}

// ListModels serves GET /v1/models.
func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	now := time.Now().Unix()
	out := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list"}
	for _, m := range models {
		out.Data = append(out.Data, model{ID: m.ID, Object: "model", Created: now, OwnedBy: "deepseek"})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// ChatCompletion serves POST /v1/chat/completions.
func (h *Handler) ChatCompletion(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "bad JSON body: "+err.Error())
		return
	}

	modelName := req.Model
	if modelName == "" {
		modelName = h.cfg.DefaultModel
	}
	m, ok := resolveModel(modelName)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("unknown model %q; available: deepseek-chat, deepseek-reasoner, deepseek-search, deepseek-reasoner-search", req.Model))
		return
	}
	log.Printf("chat: resolved model=%s (request=%s) thinking=%v search=%v", m.ID, req.Model, m.Thinking, m.Search)

	// The last user message is the prompt; the rest is context that either the
	// conversation cache or the upstream session already holds.
	var systemParts []string
	for _, msg := range req.Messages {
		if msg.Role == "system" || msg.Role == "developer" {
			if text := contentText(msg.Content); text != "" {
				systemParts = append(systemParts, text)
			}
		}
	}

	tools := parseTools(req.Tools)
	choice := parseToolChoice(req.ToolChoice)
	activeTools := len(tools) > 0 && choice.kind != "none"
	toolFlow := hasToolFlow(req.Messages)
	useToolPath := activeTools || toolFlow

	// keyID isolates this client's conversations: it is mixed into the prefix
	// and fingerprint keys so distinct proxy API keys never share a session.
	keyID := clientKeyID(r.Context())

	conv, fresh, how, err := h.conversation(&req, m, toolFlow, keyID)
	if err != nil {
		log.Printf("chat: create session failed: %v", err)
		status, msg := mapUpstreamError(err)
		writeError(w, status, "api_error", msg)
		return
	}

	// If the chosen conversation sits on an account that has since been
	// quarantined (its token expired), migrate it to a live account before
	// locking: evict the stale session and re-resolve so the prefix re-binds
	// to a live account with a full history replay.
	conv, fresh, how, err = h.migrateOffDeadAccount(&req, m, toolFlow, conv, fresh, how, keyID)
	if err != nil {
		log.Printf("chat: create session failed: %v", err)
		status, msg := mapUpstreamError(err)
		writeError(w, status, "api_error", msg)
		return
	}

	conv.Lock()
	defer conv.Unlock()

	completionID := "chatcmpl-" + randHex(16)
	createdAt := time.Now().Unix()

	var reply, thinking strings.Builder
	var usageTotal int64 = -1
	var stream *sseWriter

	// The marker detector runs on every reply stream: in tool mode it extracts
	// calls; without tools it still strips any DSML / sentinel fragments the
	// model may leak so artifacts never reach the client.
	detector := newStreamingToolDetector(tools, !activeTools, h.cfg.ToolJSONFallback)
	var emittedCalls []ToolCall

	routeDeltas := func(kind deepseek.DeltaKind, text string) {
		if kind == deepseek.DeltaThink {
			thinking.WriteString(text)
			if stream != nil {
				stream.delta(kind, text)
			}
			return
		}
		reply.WriteString(text)
		if stream == nil {
			return // non-streaming: the full reply is parsed after completion
		}
		for _, act := range detector.feed(text) {
			switch act.kind {
			case actionText:
				if act.text != "" {
					stream.delta(deepseek.DeltaReply, act.text)
				}
			case actionToolCalls:
				emittedCalls = append(emittedCalls, act.calls...)
				stream.toolCalls(act.calls)
			}
		}
	}

	cb := &deepseek.StreamCallbacks{
		OnReady: func(requestID, responseID int64) {
			conv.ParentID = responseID
			conv.HasParent = true
			conv.Turn++
		},
		OnDelta: routeDeltas,
		OnUsage: func(accumulated int64) {
			usageTotal = accumulated
		},
	}

	// buildAndComplete builds this turn's prompt / images / input for the
	// given conversation and runs the upstream completion with 429 retries.
	// It is a closure so the session self-heal path below can rerun it on a
	// freshly recreated session with full history replay. Pre-completion
	// aborts (invalid request, image upload failure, client gone) return
	// errRequestAborted and never trigger self-heal.
	buildAndComplete := func(c *Conversation, isFresh bool) error {
		var prompt string
		var images []imageRef
		if useToolPath {
			// Agent / tool flow. The upstream session already holds everything
			// up to the last assistant message, so on a cache hit only the
			// newer messages are sent; a fresh session gets the full history.
			// The tool protocol and system directives are re-injected on every
			// turn so the model keeps the format as the session grows.
			start := 0
			if !isFresh {
				if la := lastAssistantIndex(req.Messages); la >= 0 {
					start = la + 1
				}
			}
			if activeTools {
				if tp := buildToolPrompt(tools, choice); tp != "" {
					systemParts = append(systemParts, tp)
				}
			}
			if isJSONMode(req.ResponseFormat) {
				systemParts = append(systemParts, "Reply with ONLY a valid JSON object. No extra text, no markdown.")
			}
			if len(systemParts) > 0 {
				prompt = "[System]\n" + strings.Join(systemParts, "\n\n") + "\n[/System]"
			}
			var lines []string
			for _, msg := range req.Messages[start:] {
				switch msg.Role {
				case "system", "developer":
					// already collected into systemParts above
				case "user":
					if text := contentText(msg.Content); text != "" {
						lines = append(lines, text)
					}
					images = append(images, extractImages(msg.Content)...)
				case "assistant":
					if s := renderToolCallsSentinel(msg.ToolCalls); s != "" {
						lines = append(lines, s)
					}
					if text := contentText(msg.Content); text != "" {
						lines = append(lines, text)
					}
				case "tool":
					lines = append(lines, "[Tool Result id="+msg.ToolCallID+"]: "+contentText(msg.Content))
				}
			}
			if len(lines) > 0 {
				prompt += "\n\n" + strings.Join(lines, "\n")
			}
			if strings.TrimSpace(prompt) == "" {
				writeError(w, http.StatusBadRequest, "invalid_request_error", "messages carry no content to send")
				return errRequestAborted
			}
		} else {
			// Plain chat path (unchanged): only the last user message is new input.
			var lastUserText string
			var lastUserImages []imageRef
			hasUser := false
			for _, msg := range req.Messages {
				if msg.Role != "user" {
					continue
				}
				hasUser = true
				lastUserText = contentText(msg.Content)
				lastUserImages = extractImages(msg.Content)
			}
			if !hasUser || (strings.TrimSpace(lastUserText) == "" && len(lastUserImages) == 0) {
				writeError(w, http.StatusBadRequest, "invalid_request_error", "messages must contain a user message with text or images")
				return errRequestAborted
			}
			prompt = lastUserText
			images = lastUserImages
			if isFresh && len(systemParts) > 0 {
				prompt = "[System]\n" + strings.Join(systemParts, "\n\n") + "\n[/System]\n\n" + prompt
			}
		}

		// Images ride along as uploaded files referenced by ref_file_ids. Only
		// the current turn's images are sent; older ones are already in the
		// upstream session context.
		var refFileIDs []string
		if len(images) > 0 {
			ids, err := uploadImages(r.Context(), c.Client, images)
			if err != nil {
				log.Printf("chat: image upload failed: %v", err)
				writeError(w, http.StatusBadGateway, "api_error", "image upload failed: "+err.Error())
				return errRequestAborted
			}
			refFileIDs = ids
		}

		input := deepseek.CompletionInput{
			SessionID:       c.SessionID,
			ThinkingEnabled: m.Thinking,
			SearchEnabled:   m.Search,
			Prompt:          prompt,
			RefFileIDs:      refFileIDs,
		}
		if c.HasParent {
			parent := c.ParentID
			input.ParentMessageID = &parent
			input.ModelType = nil
		} else {
			defaultModel := "default"
			input.ModelType = &defaultModel
		}

		// Rate-limit per-account to stay under DeepSeek's anti-fraud
		// thresholds. Blocks until a quota token and a concurrency slot are
		// both available. Also enforces a minimum cooldown between requests
		// to the same account (simulates a human reading the reply before
		// asking the next question).
		h.enforceReadTimeCooldown(c.Client, toolFlow)
		release := h.limiter.Wait(c.Client.DeviceID, h.cfg.AccountQPM, h.cfg.MaxConcurrentPerAccount)
		defer release()

		return h.chatWithRetry(r.Context(), c, input, cb)
	}

	if req.Stream {
		stream = newSSEWriter(w, completionID, modelName, createdAt, conv.SessionID)
		if err := stream.open(); err != nil {
			log.Printf("chat: client went away before stream start: %v", err)
			return
		}
	}

	err = buildAndComplete(conv, fresh)

	// Passive detection: an upstream failure observed mid-request quarantines
	// the account immediately so the next request migrates off it (see
	// migrateOffDeadAccount).
	//
	// BAN detection first: an upstream ban ("账号已被封禁", "banned", …) is not
	// an expiring token — retrying a banned account only makes the ban worse,
	// so it is held out of rotation for BanQuarantineHours (0 = until a
	// probe succeeds). Only 401/403 without ban wording are token problems.
	if err != nil {
		if idx := h.pool.IndexOf(conv.Client); idx >= 0 {
			if kw := banKeyword(err); kw != "" {
				reason := "upstream ban detected (" + kw + ")"
				if h.pool.Quarantine(idx, reason) {
					if h.cfg.BanQuarantineHours > 0 {
						hold := time.Duration(h.cfg.BanQuarantineHours) * time.Hour
						h.pool.SetBanUntil(idx, time.Now().Add(hold))
						log.Printf("health: account #%d/%d %s → held for %s (BAN_QUARANTINE_HOURS=%d)", idx+1, len(h.pool.clients), reason, hold, h.cfg.BanQuarantineHours)
					} else {
						log.Printf("health: account #%d/%d %s → quarantined until a probe succeeds", idx+1, len(h.pool.clients), reason)
					}
				}
				if h.health != nil {
					h.health.EventQuarantine(idx, reason)
				}
			} else {
				var ae *deepseek.APIError
				if errors.As(err, &ae) && (ae.HTTPStatus == 401 || ae.HTTPStatus == 403) {
					reason := fmt.Sprintf("upstream http %d during completion", ae.HTTPStatus)
					if h.health != nil {
						h.health.EventQuarantine(idx, reason)
					} else {
						h.pool.Quarantine(idx, reason)
					}
				}
			}
		}
	}

	// Session self-heal (PERSIST_SESSIONS, non-streaming only): a cached or
	// restored upstream session may be dead — e.g. it expired upstream while
	// the proxy was restarting. Recreate the session on the same account
	// (full history replay) and retry the completion once. Auth failures
	// stay on the quarantine/migration path; 429s are rate limits, not dead
	// sessions; pre-completion aborts never retry.
	if shouldSelfHeal(err, req.Stream, h.cfg.PersistSessions, fresh, r.Context().Err() != nil) {
		log.Printf("chat: cached session %s looks dead (%v); recreating on the same account and retrying once", conv.SessionID[:8], err)
		h.convs.Evict(conv.SessionID)
		key := PrefixKey(req.Messages[:len(req.Messages)-1], m.ID, keyID)
		nc, cerr := h.coalesceOrCreate(key, m.ID)
		if cerr != nil {
			log.Printf("chat: self-heal session create failed: %v", cerr)
		} else {
			conv, fresh, how = nc, true, howCreated
			// Reset the per-response state so the failed first attempt
			// (partial text, stale detector, usage baseline) cannot
			// contaminate the retry.
			reply.Reset()
			thinking.Reset()
			usageTotal = -1
			emittedCalls = nil
			detector = newStreamingToolDetector(tools, !activeTools, h.cfg.ToolJSONFallback)
			h.persistMark()
			err = buildAndComplete(conv, true)
		}
	}

	// Record this request time for the next cooldown calculation.
	h.recordRequestTime(conv.Client)

	// Non-streaming replies are parsed for marker blocks once complete; the
	// streaming detector already flushed its residue at the end of the stream.
	var cleanText string
	if !req.Stream {
		calls, clean := parseToolCalls(reply.String(), tools, h.cfg.ToolJSONFallback)
		emittedCalls = calls
		cleanText = clean
	} else {
		for _, act := range detector.finalize() {
			switch act.kind {
			case actionText:
				if act.text != "" {
					stream.delta(deepseek.DeltaReply, act.text)
				}
			case actionToolCalls:
				emittedCalls = append(emittedCalls, act.calls...)
				stream.toolCalls(act.calls)
			}
		}
	}

	// Reasoning fallback: web models sometimes write the final answer only
	// into the THINK fragment, leaving the body empty (the client would get
	// a blank reply). Promote the thinking text to content — skipped when
	// the turn ended in tool calls, where empty content is the normal shape.
	reasoningSwapped, promoted := shouldPromoteReasoning(reply.String(), thinking.String(), h.cfg.ReasoningFallback, len(emittedCalls) > 0)
	if reasoningSwapped {
		if !req.Stream {
			cleanText = promoted
		} else {
			stream.delta(deepseek.DeltaReply, promoted)
		}
	}

	// Register the continuation key even on partial streams: the ready event
	// already advanced the parent message id, so follow-ups can chain. When
	// the reply carried tool calls, the next request's prefix will contain
	// the echoed assistant tool_calls + tool results, so they must be part
	// of the registered key (canonical form).
	var nextMsg chatMessage
	if len(emittedCalls) > 0 {
		nextMsg = chatMessage{Role: "assistant", ToolCalls: toolCallsJSON(emittedCalls)}
	} else {
		nextMsg = chatMessage{Role: "assistant", Content: json.RawMessage(mustJSON(reply.String()))}
	}
	key := PrefixKey(append(append([]chatMessage{}, req.Messages...), nextMsg), m.ID, keyID)
	h.convs.Register(key, conv)
	log.Printf("chat: registered conv session=%s turns=%d keyLen=%d toolCalls=%d", conv.SessionID[:8], conv.Turn, len(key), len(emittedCalls))

	// Conversation-opening fingerprint (CONV_FINGERPRINT_ROUTE): the next
	// request of this task will compute the same fingerprint — system +
	// first user + first assistant (now known) + model + keyID — and re-attach
	// to this session/account no matter how its rolling prefix hashing turns
	// out.
	//
	// Register only when the request genuinely belongs to this session:
	// a freshly created session, or one matched by content (fingerprint /
	// prefix / conversation_id). Never on a strategy-3 most-recent fallback,
	// which is what allowed a foreign client's fingerprint to be locked into
	// another client's session.
	if h.cfg.ConvFingerprintRoute && how != howRecent {
		full := append(append([]chatMessage{}, req.Messages...), nextMsg)
		h.convs.RegisterFingerprint(convFingerprint(full, m.ID, keyID), conv)
	}
	h.persistMark()

	if err != nil {
		if r.Context().Err() != nil {
			return // client gone; upstream message completes on its own
		}
		log.Printf("chat: upstream completion failed: %v", err)
		if req.Stream {
			stream.errorText(err.Error())
			return
		}
		status, msg := mapUpstreamError(err)
		writeError(w, status, "api_error", msg)
		return
	}

	finishReason := "stop"
	if len(emittedCalls) > 0 {
		finishReason = "tool_calls"
	}

	usage := h.buildUsage(conv, usageTotal, reply.Len())
	if req.Stream {
		stream.finish(usage, req.StreamOptions != nil && req.StreamOptions.IncludeUsage, finishReason)
		log.Printf("chat %s %s conv=%s turn=%d stream %s", completionID, modelName, conv.SessionID, conv.Turn, time.Since(start).Round(time.Millisecond))
		return
	}

	message := map[string]any{"role": "assistant"}
	if len(emittedCalls) > 0 {
		message["content"] = nil
		message["tool_calls"] = openAIToolCalls(emittedCalls)
	} else {
		message["content"] = cleanText
	}
	if t := thinking.String(); t != "" && !reasoningSwapped {
		message["reasoning_content"] = t
	}
	resp := map[string]any{
		"id":              completionID,
		"object":          "chat.completion",
		"created":         createdAt,
		"model":           modelName,
		"conversation_id": conv.SessionID,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": usage,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
	log.Printf("chat %s %s conv=%s turn=%d %s", completionID, modelName, conv.SessionID, conv.Turn, time.Since(start).Round(time.Millisecond))
}

// conversation finds a usable conversation or creates a new DeepSeek session.
// With a configurable probability (SessionResetChance), it intentionally
// discards a cached session and starts fresh — mimicking how real users
// occasionally open a new chat instead of continuing an old one.
//
// Lookup strategy:
//  1. Explicit conversation_id passthrough (client controls session)
//  2. Prefix-key match (historical messages match an existing session)
//  3. Fallback to most-recently-used session for the same model (enables
//     continuous Q&A even when the client sends only the latest message)
//
// howResolved records how conversation() resolved the session, so
// fingerprint registration can avoid locking in a wrong association.
type howResolved int

const (
	howCreated        howResolved = iota // this request created a fresh session
	howConversationID                    // strategy 1: explicit conversation_id
	howPrefix                            // strategy 2: full-history prefix match
	howFingerprint                       // strategy 2.5: conversation-opening match
	howRecent                            // strategy 3: most-recent fallback (no identity proof)
)

func (h *Handler) conversation(req *chatRequest, m *ModelInfo, toolLoop bool, keyID string) (*Conversation, bool, howResolved, error) {
	// Debug: log the incoming request structure
	msgCount := len(req.Messages)
	lastMsgText := ""
	if msgCount > 0 {
		lastMsgText = contentText(req.Messages[msgCount-1].Content)
		if len(lastMsgText) > 60 {
			lastMsgText = lastMsgText[:60] + "..."
		}
	}
	log.Printf("chat: conversation lookup model=%s msgs=%d convID=%s toolLoop=%v keyID=%s lastMsg=%q",
		m.ID, msgCount, req.ConversationID, toolLoop, keyID, lastMsgText)

	// Strategy 1: explicit conversation ID
	if req.ConversationID != "" {
		if c := h.convs.BySession(req.ConversationID); c != nil {
			log.Printf("chat: strategy1 hit convID=%s session=%s", req.ConversationID, c.SessionID[:8])
			return c, false, howConversationID, nil
		}
		log.Printf("chat: strategy1 miss convID=%s (not found)", req.ConversationID)
	}

	// Random eviction is skipped for agent tool loops: an agent's turns
	// share one growing prefix, and dropping the session mid-loop would lose
	// every tool result sent so far.
	evict := func(c *Conversation) {
		if !toolLoop && h.cfg.SessionResetChance > 0 && rand2.Float64() < h.cfg.SessionResetChance {
			log.Printf("chat: evicting cached session %s (chance %.0f%%), creating new one", c.SessionID[:8], h.cfg.SessionResetChance*100)
			h.convs.Evict(c.SessionID)
			return
		}
	}

	// Strategy 2: prefix-based cache hit (the prefix key mixes in the client's
	// keyID, so distinct clients never share a prefix).
	// On a hit, we may evict the cached session (with SessionResetChance) so
	// that future requests can start fresh — this is what enables parallel
	// agent tool-calls to each get their own DeepSeek session.
	key := PrefixKey(req.Messages[:len(req.Messages)-1], m.ID, keyID)
	if c := h.convs.ByPrefix(key); c != nil {
		log.Printf("chat: strategy2 hit session=%s turns=%d", c.SessionID[:8], c.Turn)
		evict(c)
		if h.convs.ByPrefix(key) == c {
			return c, false, howPrefix, nil
		}
	}

	// Strategy 2.5 (only when the fingerprint router is on, and never on the
	// first turn of a task): match the conversation by its opening (system +
	// first user + first assistant + model + keyID). Robust to
	// rolling-prefix misses: mid-task requests re-attach to their own
	// session even when the client's echoed history no longer hashes to a
	// registered prefix. The keyID keeps distinct clients from ever matching
	// each other's openings.
	firstTurn := lastAssistantIndex(req.Messages) < 0
	if h.cfg.ConvFingerprintRoute && !firstTurn {
		fp := convFingerprint(req.Messages, m.ID, keyID)
		if c := h.convs.ByFingerprint(fp); c != nil {
			log.Printf("chat: fingerprint hit session=%s turns=%d", c.SessionID[:8], c.Turn)
			evict(c)
			if h.convs.ByFingerprint(fp) == c {
				return c, false, howFingerprint, nil
			}
		}
	}

	// Strategy 3: fallback — reuse the most recently used session for this
	// model. It is only consulted when the fingerprint router is OFF; with it
	// ON, an unknown (first-turn or unregistered) conversation starts a fresh
	// session instead of leaking into some other client's most-recent one.
	if !h.cfg.ConvFingerprintRoute {
		if c := h.convs.RecentForModel(m.ID); c != nil {
			log.Printf("chat: strategy3 hit session=%s turns=%d", c.SessionID[:8], c.Turn)
			evict(c)
			if h.convs.BySession(c.SessionID) == c {
				return c, false, howRecent, nil
			}
		}
	}
	// Coalesce concurrent requests with the same prefix: instead of each
	// spawning its own session, share one in-flight session creation.
	conv, err := h.coalesceOrCreate(key, m.ID)
	if err != nil {
		return nil, false, howCreated, err
	}
	log.Printf("chat: new session created session=%s model=%s", conv.SessionID[:8], m.ID)
	return conv, true, howCreated, nil
}

// migrateOffDeadAccount moves a conversation whose account has since been
// quarantined onto a live account. Each iteration evicts the stale session
// and re-resolves; coalesceOrCreate rebinds the prefix to a live account via
// NextLive and the new session replays the full history (fresh=true). It
// stops as soon as the conversation lands on a live account.
func (h *Handler) migrateOffDeadAccount(req *chatRequest, m *ModelInfo, toolLoop bool, conv *Conversation, fresh bool, how howResolved, keyID string) (*Conversation, bool, howResolved, error) {
	for attempt := 0; attempt < 4; attempt++ {
		idx := h.pool.IndexOf(conv.Client)
		if idx >= 0 && !h.pool.IsDead(idx) {
			return conv, fresh, how, nil
		}
		log.Printf("chat: conversation %s sits on dead account #%d, migrating to a live account", conv.SessionID[:8], idx+1)
		h.convs.Evict(conv.SessionID)
		var err error
		conv, fresh, how, err = h.conversation(req, m, toolLoop, keyID)
		if err != nil {
			return conv, fresh, how, err
		}
	}
	return conv, fresh, how, nil
}

// restoreConvRecords rebuilds in-memory state from persisted session records
// (called at startup, before the server listens). Drops, counted, are:
// records past their idle TTL (the sweep already deleted those upstream
// sessions) and records whose account no longer exists or is quarantined
// (its web sessions died with the token; the next request rebinds via
// round-robin). Restored tasks keep their prefix→account binding, so task a
// resumes on account A after a restart.
func (h *Handler) restoreConvRecords(entries map[string]ConvRecord) (restored, dropped int) {
	if len(entries) == 0 {
		return 0, 0
	}
	now := time.Now().Unix()
	bySession := make(map[string]*Conversation, len(entries))
	prefixKeys := make(map[string]string)
	fingerprintKeys := make(map[string]string)
	for _, rec := range entries {
		if rec.SessionID == "" {
			dropped++
			continue
		}
		if rec.Expiry > 0 && now > rec.Expiry {
			dropped++
			continue
		}
		idx := h.pool.FindByMask(rec.Account)
		if idx < 0 {
			dropped++
			continue
		}
		if conv, ok := bySession[rec.SessionID]; !ok {
			conv = &Conversation{
				Client:    h.pool.clients[idx],
				SessionID: rec.SessionID,
				ModelID:   rec.Model,
				Turn:      rec.Turns,
				turnCount: rec.Turns,
			}
			if rec.LastUsed > 0 {
				conv.LastUsed = time.Unix(rec.LastUsed, 0)
			} else {
				conv.LastUsed = time.Now()
			}
			if rec.Expiry > 0 {
				conv.ttl = time.Until(time.Unix(rec.Expiry, 0))
			}
			bySession[rec.SessionID] = conv
		}
		for _, k := range rec.PrefixKeys {
			prefixKeys[k] = rec.SessionID
		}
		for _, k := range rec.FingerprintKeys {
			fingerprintKeys[k] = rec.SessionID
		}
	}
	restored = h.convs.LoadSnapshot(bySession, prefixKeys, fingerprintKeys)

	// Restore the prefix→account bindings so restored tasks stay on their
	// accounts (the whole point of persistence in multi-account setups).
	h.pendingMu.Lock()
	for k, sid := range prefixKeys {
		conv := bySession[sid]
		if conv == nil {
			continue
		}
		if idx := h.pool.IndexOf(conv.Client); idx >= 0 {
			h.prefixToClientIdx[k] = idx
		}
	}
	h.pendingMu.Unlock()
	return restored, dropped
}

// resolveBoundAccount returns the pool index serving the prefix. Existing
// bindings are kept; unbound prefixes and bindings pointing at quarantined
// accounts take the next live account in round-robin order. The caller must
// hold h.pendingMu. Returns -1 when no account is live.
//
// Note the two-value map read: a missing key must be treated as "unbound",
// not as account 0 — the old single-value read returned Go's zero value and
// pinned every new conversation to the first account.
func (h *Handler) resolveBoundAccount(key string) int {
	clientIdx, bound := h.prefixToClientIdx[key]
	if !bound || clientIdx >= len(h.pool.clients) || h.pool.IsDead(clientIdx) || h.dailyExhausted(clientIdx) {
		// Pick a replacement: least-busy live account (spreads new traffic
		// instead of piling onto whoever the bare round-robin points at),
		// skipping accounts whose daily quota is already used up. The bare
		// round-robin order still breaks load ties, keeping fairness.
		start, ok := h.pool.NextLive()
		if !ok {
			return -1
		}
		n := len(h.pool.clients)
		best, bestLoad := -1, 0
		for i := 0; i < n; i++ {
			idx := (start + i) % n
			if h.pool.IsDead(idx) || h.dailyExhausted(idx) {
				continue
			}
			load := h.limiter.InFlight(h.pool.clients[idx].DeviceID)
			if best < 0 || load < bestLoad {
				best, bestLoad = idx, load
			}
		}
		if best < 0 {
			return -1 // no live account, or every live one is daily-exhausted
		}
		h.prefixToClientIdx[key] = best
		return best
	}
	return clientIdx
}

// dailyExhausted reports whether the account has already used up today's
// daily request quota (0/unset limit or no tracker = never exhausted).
func (h *Handler) dailyExhausted(idx int) bool {
	if h.usage == nil || h.cfg.DailyRequestLimit <= 0 || idx < 0 || idx >= len(h.pool.clients) {
		return false
	}
	mask := maskToken(h.pool.clients[idx].Token)
	return h.usage.Count(mask) >= h.cfg.DailyRequestLimit
}

// coalesceOrCreate ensures all concurrent requests sharing the same prefix
// reuse a single in-flight session creation. It also respects the prefix-to-
// account binding so that the same prefix always lands on the same DeepSeek
// account, keeping the conversation context intact.
func (h *Handler) coalesceOrCreate(key, modelID string) (*Conversation, error) {
	// Phase 1: check / create entry while holding the lock.
	h.pendingMu.Lock()
	if entry, ok := h.pending[key]; ok {
		h.pendingMu.Unlock()
		log.Printf("chat: coalescing onto existing in-flight session prefix=%s", key[:8])
		select {
		case conv := <-entry.done:
			if conv != nil {
				return conv, nil
			}
			return nil, context.Canceled
		case <-entry.ctx.Done():
			return nil, entry.ctx.Err()
		}
	}
	entry := &pendingEntry{done: make(chan *Conversation, 1), ctx: context.Background()}
	h.pending[key] = entry

	// Determine which account to use for this prefix. A bound account keeps
	// the conversation on the same DeepSeek session, preventing context loss.
	// Unbound prefixes and prefixes bound to a quarantined account are (re)
	// bound to the next live account in round-robin order.
	clientIdx := h.resolveBoundAccount(key)
	if clientIdx < 0 {
		// Either nobody is live, or everyone is live but out of daily
		// quota — give the matching error (mapUpstreamError maps both to 503).
		delete(h.pending, key)
		h.pendingMu.Unlock()
		if h.pool.HasLive() && h.usage != nil && h.cfg.DailyRequestLimit > 0 {
			return nil, errDailyLimit
		}
		return nil, errNoLiveAccount
	}
	boundClient := h.pool.clients[clientIdx]

	// Spawn the goroutine while still holding the lock so no other caller can
	// enter the create-path. We drop the lock IMMEDIATELY after spawning —
	// the goroutine only re-acquires it briefly at the end to clean up the
	// map entry (avoiding a ~20 s hold that would deadlock all other requests).
	go func() {
		client := boundClient
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		sessionID, err := client.CreateSession(ctx)
		// Briefly re-acquire lock only for map cleanup.
		h.pendingMu.Lock()
		delete(h.pending, key)
		h.pendingMu.Unlock()
		if err != nil {
			log.Printf("chat: session create failed: %v", err)
			entry.done <- nil
			return
		}
		entry.done <- &Conversation{
			Client:    client,
			SessionID: sessionID,
			LastUsed:  time.Now(),
			ModelID:   modelID,
		}
	}()
	// Drop the lock now — the goroutine runs independently.
	h.pendingMu.Unlock()

	// Phase 2: wait for the upstream session to be created.
	select {
	case conv := <-entry.done:
		if conv != nil {
			return conv, nil
		}
		return nil, context.Canceled
	case <-entry.ctx.Done():
		return nil, entry.ctx.Err()
	}
}

func (h *Handler) buildUsage(conv *Conversation, total int64, replyChars int) map[string]int {
	var span int64
	if conv.HasUsage {
		span = total - conv.LastUsage
	} else {
		span = total
	}
	if total >= 0 {
		conv.LastUsage = total
		conv.HasUsage = true
	}
	if span < 0 {
		span = 0
	}
	completion := int64(replyChars / 4)
	if span == 0 {
		span = completion // upstream gave no usage frame; fall back to an estimate
	}
	prompt := span - completion
	if prompt < 0 {
		prompt = 0
	}
	return map[string]int{
		"prompt_tokens":     int(prompt),
		"completion_tokens": int(completion),
		"total_tokens":      int(span),
	}
}

// errRequestAborted signals a pre-completion abort (invalid request, image
// upload failure, client gone). Self-heal never recreates a session for
// these — the request itself is the problem, not the upstream session.
var errRequestAborted = errors.New("request aborted before completion")

// shouldSelfHeal decides whether a failed completion on a cached (or
// restored) upstream session warrants recreating the session on the same
// account — full history replay — and retrying once.
//
// It refuses to fire for: success, streaming responses (the client has
// already received partial deltas), disabled persistence, fresh sessions
// (there is nothing cached to have died), aborted requests (client gone),
// and auth/rate-limit API errors (401/403 → quarantine+migrate; 429 →
// backoff, not a dead session).
func shouldSelfHeal(err error, stream, enabled, isFresh, clientGone bool) bool {
	if err == nil || stream || !enabled || isFresh || clientGone {
		return false
	}
	if errors.Is(err, errRequestAborted) {
		return false
	}
	var ae *deepseek.APIError
	if errors.As(err, &ae) && (ae.HTTPStatus == 401 || ae.HTTPStatus == 403 || ae.HTTPStatus == 429) {
		return false
	}
	return true
}

// banKeywords are upstream error wordings that signal an account ban rather
// than a token expiry or a transient fault. Matched only on the error path
// (API error messages), never on model reply content.
var banKeywords = []string{
	"封禁", "被封", "封号", "封停", "账号已被限制", "账号异常", "banned", "suspended",
}

// banKeyword returns the first ban keyword found in the error's text, or "".
func banKeyword(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	for _, kw := range banKeywords {
		if strings.Contains(text, strings.ToLower(kw)) {
			return kw
		}
	}
	return ""
}

func mapUpstreamError(err error) (int, string) {
	if errors.Is(err, errNoLiveAccount) {
		return http.StatusServiceUnavailable, errNoLiveAccount.Error()
	}
	if errors.Is(err, errDailyLimit) {
		return http.StatusServiceUnavailable, errDailyLimit.Error()
	}
	var ae *deepseek.APIError
	if errors.As(err, &ae) {
		switch {
		case ae.HTTPStatus == 401 || ae.HTTPStatus == 403:
			return http.StatusUnauthorized, "deepseek rejected the configured token (invalid or expired)"
		case ae.HTTPStatus == 429:
			return http.StatusTooManyRequests, "deepseek rate limited this account; retry later"
		default:
			return http.StatusBadGateway, "deepseek upstream error: " + ae.Error()
		}
	}
	return http.StatusBadGateway, "deepseek upstream error: " + err.Error()
}

// chatWithRetry calls conv.Client.Completion with exponential-backoff retry
// on 429 responses. It stays on the same account for the entire conversation
// to preserve context; only the read-time cooldown and QPM token bucket provide
// back-pressure.
func (h *Handler) chatWithRetry(ctx context.Context, conv *Conversation, input deepseek.CompletionInput, cb *deepseek.StreamCallbacks) error {
	// Daily quota: count this request against the serving account so the
	// rotation skips it once the budget is spent (counted once per request,
	// not per 429 retry).
	if h.usage != nil && h.cfg.DailyRequestLimit > 0 {
		idx := h.pool.IndexOf(conv.Client)
		if idx >= 0 {
			mask := maskToken(h.pool.clients[idx].Token)
			if total := h.usage.Use(mask); total == h.cfg.DailyRequestLimit {
				log.Printf("chat: account #%d/%d hit its daily limit (%d); fresh sessions now route around it", idx+1, len(h.pool.clients), total)
			}
		}
	}
	// Account-level backoff: each 429 seen on this account escalates the
	// pool-wide cooldown for it (so later requests route around it); a
	// success clears the escalation.
	idx := h.pool.IndexOf(conv.Client)
	var lastErr error
	baseDelay := h.cfg.RetryBaseDelay
	if baseDelay <= 0 {
		baseDelay = 2 * time.Second
	}
	for attempt := 0; attempt <= h.cfg.RetryMaxAttempts; attempt++ {
		lastErr = h.doCompletionWithRetry(ctx, conv.Client, input, cb, baseDelay, attempt)
		if lastErr == nil {
			h.pool.MarkRateRecovery(idx)
			return nil
		}
		var ae *deepseek.APIError
		if !errors.As(lastErr, &ae) || ae.HTTPStatus != 429 {
			break // non-429 error: don't retry
		}
		h.pool.MarkRateLimited(idx)
		// Rate-limited — retry on the same account with increasing delay.
		// Switching accounts would break the conversation context, so we
		// stay put and wait.
		wait := baseDelay * time.Duration(1<<uint(attempt-1)) // 2s, 4s, 8s...
		if wait > time.Minute*5 {
			wait = time.Minute * 5
		}
		log.Printf("chat: account rate-limited (429), retry %d/%d in %s", attempt+1, h.cfg.RetryMaxAttempts+1, wait.Round(time.Second))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return lastErr
}

// doCompletionWithRetry handles session setup and the actual Completion call.
// If the session is fresh (new), it tries to reuse an existing one first.
func (h *Handler) doCompletionWithRetry(ctx context.Context, client *deepseek.Client, input deepseek.CompletionInput, cb *deepseek.StreamCallbacks, baseDelay time.Duration, attempt int) error {
	if input.SessionID == "" {
		newCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		// Add random delay before creating a new session to mimic human browsing patterns.
		jitter := time.Duration(rand2.Int63n(int64(h.cfg.SessionCreateJitter)))
		if jitter > 0 {
			time.Sleep(jitter)
		}
		sid, err := client.CreateSession(newCtx)
		if err != nil {
			return err
		}
		input.SessionID = sid
	}
	return client.Completion(ctx, input, cb, h.cfg.RequestJitterMin, h.cfg.RequestJitterMax)
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func mustJSON(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

// shouldPromoteReasoning decides whether the thinking block must stand in
// for an empty reply body. It fires only when the body is blank, the
// thinking is not, no tool calls were emitted (empty content is the normal
// tool-call shape), and the feature is enabled. Returns whether to promote
// and the promoted text.
func shouldPromoteReasoning(reply, thinking string, enabled, hasCalls bool) (bool, string) {
	if !enabled || hasCalls {
		return false, ""
	}
	if strings.TrimSpace(reply) == "" && strings.TrimSpace(thinking) != "" {
		return true, strings.TrimSpace(thinking)
	}
	return false, ""
}

// recordRequestTime updates the last-request timestamp for this client
// so the next request will enforce the read-time cooldown.
func (h *Handler) recordRequestTime(client *deepseek.Client) {
	h.mu.Lock()
	h.lastRequestAt[client] = time.Now()
	h.mu.Unlock()
}

// enforceReadTimeCooldown waits until enough time has passed since the last
// request to this account, simulating a human reading the reply before
// sending the next message. This creates a natural inter-request rhythm
// on top of the QPM token bucket. Agent tool loops are machine-paced, so
// their "reading" wait is capped to keep the loop responsive.
//
// LONG_PAUSE_CHANCE additionally rolls a dice per request: with that
// probability the wait stretches into a 20–60s "stepped away from the
// keyboard" pause — a long-tail human habit (checked the phone, read the
// mail) that makes the request rhythm even less machine-like. Skipped for
// agent tool loops, which must stay responsive.
func (h *Handler) enforceReadTimeCooldown(client *deepseek.Client, toolLoop bool) {
	if !toolLoop && h.cfg.LongPauseChance > 0 && rand2.Float64() < h.cfg.LongPauseChance {
		pause := time.Duration(rand2.Int63n(40*int64(time.Second))) + 20*time.Second // 20–60s
		log.Printf("chat: human-like long pause before upstream request: %s", pause)
		time.Sleep(pause)
	}
	if h.cfg.RequestReadTimeMin <= 0 && h.cfg.RequestReadTimeMax <= 0 {
		return
	}
	h.mu.Lock()
	last := h.lastRequestAt[client]
	h.mu.Unlock()

	if !last.IsZero() {
		minWait := h.cfg.RequestReadTimeMin
		maxWait := h.cfg.RequestReadTimeMax
		if toolLoop {
			const toolLoopCap = 2 * time.Second
			if minWait > toolLoopCap {
				minWait = toolLoopCap
			}
			if maxWait > toolLoopCap {
				maxWait = toolLoopCap
			}
		}
		if maxWait > minWait {
			minWait = time.Duration(rand2.Int63n(int64(maxWait-minWait))) + minWait
		}
		elapsed := time.Since(last)
		if elapsed < minWait {
			time.Sleep(minWait - elapsed)
		}
	}
}
