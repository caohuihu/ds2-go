# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Changed

- **Agent tool-loop pacing now scales with the previous reply's length** (`TOOL_LOOP_MIN_GAP`, `handlers/handler.go`, `config/config.go`) — **default behavior change in this release**: tool-loop turns on the same account used to be capped at a flat 2 s inter-request gap — a machine rhythm that upstream risk control reads as automation. The gap is now the `TOOL_LOOP_MIN_GAP` floor (default `8s`, `0` disables tool-loop pacing) plus 1 s of "reading" per 2000 reply characters (capped at 30 s), plus 0–50 % (≤4 s) jitter so the rhythm is not constant. `lastRequestAt` now records the reply char count alongside the timestamp; the human-paced paths (5–30 s read time, `LONG_PAUSE_CHANCE` pauses) are untouched.
- **Fresh-session seeding is now bounded, not full-history** (`SESSION_REPLAY_TURNS`, `handlers/handler.go`, `handlers/conversations.go`, `config/config.go`) — **default behavior change in this release**: when a *new* upstream session is opened for an agent tool flow (session reset, quota rotation, dead-session recreate), the proxy used to replay the client's **entire** message history into the prompt — the single most conspicuous machine tell of this proxy. It now seeds only the **last N complete turn groups** (a user instruction plus the assistant/tool exchange it produced) ahead of the current instruction, cut only on user-message boundaries so a tool chain is never split. New env `SESSION_REPLAY_TURNS`: `2` = default, `0` = current instruction only (thinnest trace), `all` = the legacy full replay. The startup banner reports the active policy, and every fresh session logs how many of the messages were seeded. Plain (non-tool) chat paths are untouched — they already sent only the last user message. If an upgrade makes long tasks "forget" more context after a session swap, raise the value; `all` restores the pre-release behavior exactly.
- **429 backoff sleeps no longer park a concurrency slot** (`handlers/limiter.go`, `handlers/handler.go`): the limiter is split into `WaitQPM` (rate token) and `WaitSlot` (in-flight permit, ctx-aware — a cancelled waiter leaves with a no-op release, never a parked slot). `chatWithRetry` now takes and releases its own slot per attempt, so the exponential-backoff waits between 429 retries hold **no** in-flight budget: a storm of rate-limited requests can no longer freeze an account's whole concurrency pool behind idle sleeps. QPM token semantics are unchanged (one token per logical request, re-tries do not re-consume tokens); backoff durations and the account-level 429 escalation are untouched.

- **Stable, persisted per-account device fingerprint** (`deepseek/fingerprint.go`, `handlers/fingerprint.go`, `main.go`): every process start used to re-randomize all accounts' user agents, web-client versions and locales, and the device id was a fresh UUID each time, never persisted — so every restart made each account look like a brand-new device enrolling (a device-pool tell; a normal human runs one fixed device, at most a browser upgrade months later). Each account's identity (device id / version / locale / user agent / timezone offset) is now generated once from a human-weighted pool and pinned to `<DATA_DIR>/device-fingerprints.json` (keyed by the masked token, so reordering `accounts.txt` is safe; an account removed and re-added resumes its old identity). The pools were narrowed to the real user base: the version pool weights the current release (3 of 6 slots), the locale pool weights `zh_CN` (5 of 7) and drops `ja_JP`/`ko_KR`, and the UA pool adds a small Mac minority to the Windows Chrome/Edge majority. The timezone offset is pinned too — set the container `TZ` (e.g. `Asia/Shanghai`) before the first start. Caveat: `DEVICE_ID_ROTATE_HOURS` rotates the in-memory device id only; a restart falls back to the pinned id (noted in the startup banner). On every start, each account's identity is logged to the main log (`LOG_FILE`) as one greppable line — `fingerprint: #i/n <mask> [new|pinned] ua=… ver=… locale=… tz=+08:00 device=…` — so an operator can watch which user agent every account is presenting.

### Fixed

- **Session-create failures now quarantine the account on the spot** (`handlers/handler.go`): a dead token or BAN wording seen while *creating* an upstream session only logged and returned `context.Canceled`; the account was held "live" until the next periodic probe, and every client retry re-created sessions into the dead account (the 2026-11-07 incident: 11 create attempts in 3 minutes, each re-sending the full 24-message conversation). The completion, coalesced-create and inline-create paths now all run the shared `quarantineOnUpstreamFailure` helper — BAN wording holds the account under `BAN_QUARANTINE_HOURS`, an auth failure (401/403 or envelope 40003) quarantines it until a probe succeeds, and the conversation migrates off immediately.
- **Streaming `tool_calls` indices no longer restart at 0 per delivery batch** (`handlers/sse.go`): the OpenAI spec numbers tool_call deltas globally per message, but the SSE writer re-numbered every marker batch from 0 — with two or more tool calls in one reply, the second block's indices collided with the first and clients stacked every call into slot 0. The writer now keeps a running `toolSeq` across the whole stream, so batches continue where the previous one ended (regression-tested: two blocks of 2+1 calls index 0,1 then 2).
- **Robust tool-marker parsing** (port of webai2api v0.1.51 audit, `handlers/tools.go`, `handlers/tools_stream.go`): the upstream web UI occasionally drops the leading `<` of a marker, shipping residue like `｜｜DSML｜｜ calls>` or `</｜｜DSML｜｜ invoke name="...">`. Every DSML regex was `<`-anchored, so the residue leaked into reply content. In tools mode the scanner now recognizes `<`-less openers; calls-layer fragments are stripped in place (half invoke blocks stay conservatively dropped, per the existing contract); a close-shaped opener carrying a `name=` attribute is corrected to an opener; and on stream cuts the withheld SCAN-state tail is recovered (`recoverTextOnCut`) with a marker-fragment trim (≤64 chars, markdown tables untouched), so the client keeps the last plain-text chunk before a cut.
- **Unterminated `<parameter>` values are truncated at the next marker tag**, so residue can no longer pollute a tool argument value.
- **Tri-state `string` attribute**: when the attribute is omitted the value is parsed as JSON first and falls back to a string (`42` → number, `hello` stays a string); an explicit `string="true"` keeps the raw string.
- The no-tools contract is unchanged: marker fragments stay ordinary text (regression-tested).
- **Expired-token detection covers the HTTP-200 envelope form** (`deepseek.IsAuthFailure`, `handlers/handler.go`, `handlers/health.go`): when a `userToken` in `accounts.txt` expires, the web API rejects it with **HTTP 200 + envelope `code: 40003 "Authorization Failed (invalid token)"`** — every detection point checked only HTTP 401/403, so the dead account stayed "live" forever and every session bound to it kept failing. Auth failure is now recognized via `deepseek.IsAuthFailure` (401/403, or code 40003 / "invalid token" wording) in all four places: the passive mid-request quarantine (the session then migrates to a live account with a full history replay), the startup check and periodic probes (EXPIRED → quarantined, revived on first success), the self-heal refusal (no pointless same-account recreate), and the client-facing error code (401 instead of 502).
- **Empty `messages` no longer crashes the proxy** (`handlers/handler.go`): `"messages": []` (or the field omitted) used to underflow the `req.Messages[:len(req.Messages)-1]` slice in the conversation lookup and panic the whole process from an HTTP goroutine — every in-flight task with the service. The request is now rejected with a 400 before anything touches upstream.
- **Coalesced session creation now wakes every waiter** (`handlers/handler.go`): `pendingEntry` carried a buffer=1 result channel plus a `context.Background()` that never cancels, so when several concurrent requests shared one conversation prefix (the parallel-tool-calls case), the first waiter consumed the single result and **all remaining waiters blocked forever**. The entry now publishes the shared result/error under a mutex and closes `done` exactly once, so every coalesced waiter gets the same conversation.
- **Daily quota counters reset for `Count` at midnight** (`handlers/usage.go`): rollover only ran inside `Use`, so when every account had spent yesterday's quota, the first *new-session* request after midnight read yesterday's totals via `Count` → 503 — and since the 503 happens before any `Use` that would reset, the pool stayed 503 until a restart. The rollover is now a shared `rolloverLocked()` called by both.
- **Idle-session sweep no longer loses lookup keys when the upstream delete fails** (`handlers/conversations.go`): `sweepOnce` re-queued the session in `bySession` only, dropping its `byPrefix`/`byFingerprint` keys — the task's next request would miss the cache, create a brand-new upstream session and replay the full history, and orphan the old one. The sweep now records every key of the expiring session and re-links all three maps on the failure path, with a guard that never overwrites a key already re-bound to a different session.
- **First 429 retry now actually waits** (`handlers/handler.go`): the backoff computed `1 << uint(attempt-1)`; at `attempt=0` the shift underflows to 0, so the first retry fired instantly (real waits 0s/2s/4s vs. the documented 2s/4s/8s). Now `1 << uint(attempt)`.
- **`Conversation.Turn` / `LastUsed` data race** (`handlers/conversations.go`): written under the conversation lock, read lock-free from log lines, store scans and the persister. Replaced with `atomic.Int32` / `atomic.Int64` (unix seconds) plus `Turn()` / `LastUsed()` getters.
- **Limiter semaphore growth no longer deadlocks** (`handlers/limiter.go`): growing an account's in-flight cap used to block-drain the old channel while holding both locks; in-flight slots release into the bucket's *current* channel (re-read at release time), so the old one never refilled and any in-flight request deadlocked the whole account. The new channel is now prefilled with one free slot per in-flight request instead — non-blocking, and in-flight accounting stays exact.

### Added

- **`TOOL_LOOP_MIN_GAP` env** (`config/config.go`, default `8s`): the tool-loop pacing floor documented above; `0` disables tool-loop pacing entirely. The startup banner reports the active value.
- **503s for a dead pool / spent daily quota carry `Retry-After: 60`** (`handlers/handler.go`): when no 429 cooldown is in flight, `errNoLiveAccount` / `errDailyLimit` responses now tell clients to back off a minute instead of re-sending the same conversation immediately (the existing 429-derived `Retry-After` hint is unchanged).
- **Per-request outcome classes + first-content latency in the risk summary** (`handlers/stats.go`, `handlers/handler.go`): every request now buckets into exactly one outcome class — `429` / `auth` / `ban` / `upstream` (dead pool, daily limit, generic 5xx) / `empty` (silent reply that needed auto-continuation) / `cancelled` (client gone) — so the `RISK_STATS_INTERVAL` summary can say *which* failure mode is showing up instead of a lumped error count; it also reports the mean latency of successful requests. 429/503 client responses additionally carry a `Retry-After` header derived from the pool's longest active 429 cooldown.
- **`SESSION_REPLAY_TURNS` env** (`config/config.go`): the seeding knob documented above — `2` (default), `0`, or `all`; invalid values fall back to the default with a logged warning.
- **Fresh-session seeding log line** (`handlers/handler.go`): every fresh agent-session seed logs `chat: fresh session seeded with X of Y messages (SESSION_REPLAY_TURNS=N)` so the operator can watch exactly what a new session gets.

- **Silent-reply auto-continuation** (`handlers/handler.go`, `config/config.go`)
  - New env `AUTO_CONTINUATION` (default `false`) + `AUTO_CONTINUATION_MAX`
    (default `2`): when the upstream returns a *silent* successful completion
    (empty body, empty thinking — the long-task stall mode), the proxy nudges
    the **same web session** with a "Continue" prompt chained off the empty
    reply's parent message, up to `AUTO_CONTINUATION_MAX` consecutive nudges.
    Continuation deltas flow into the same reply stream, so the client sees
    one seamless answer; a failed nudge logs and returns the partial reply.
    Triggers only when the reply is fully empty AND the session has a parent
    message; tool-flow turns and any non-empty reply never trigger it.
    Continuation turns count toward `REQUESTS_PER_SESSION` and the daily
    quota (no exemption).
- **Risk observability** (`handlers/stats.go`, `handlers/health.go`)
  - New env `RISK_STATS_INTERVAL` (duration, default `0` = off): a ticker in
    `main` logs a multi-line risk summary to the main log at that interval —
    global request / session-created / cache-hit counters plus, per account,
    successful completions, consecutive-429 count, remaining 429 cooldown,
    remaining BAN hold, daily-quota usage and in-flight count
    (`AccountPool.RateHits` exposes the escalation counter; the created-vs-
    hits ratio is the primary risk signal).

- **Daily request quota** (`handlers/usage.go`, `config/config.go`)
  - New env `DAILY_REQUEST_LIMIT` (default `0` = unlimited): per-account
    requests are counted per local day and persisted to
    `<DATA_DIR>/daily-usage.json` (restart-safe; counters reset at midnight,
    corrupt files are quarantined instead of crashing).
  - An account at its budget is skipped when picking accounts for new
    sessions; when *all* live accounts are spent, new sessions fail fast
    with 503 (`errDailyLimit`) until midnight while cached sessions keep
    working.
- **Human-like long pause** (`handlers/handler.go`)
  - New env `LONG_PAUSE_CHANCE` (default `0` = off): with that probability
    any non-agent request waits an extra random 20–60 s before hitting
    upstream ("stepped away" tail on top of the read-time cooldown).
- **Load-aware account selection** (`handlers/handler.go`, `handlers/limiter.go`)
  - New `AccountLimiter.InFlight` reports in-flight completions per account.
  - When a prefix must re-bind its account (unbound / dead / quota-spent),
    the pool now picks the *least busy* live account instead of bare
    round-robin; round-robin only breaks ties.

- **Account-level 429 backoff + all-pool grace window** (`handlers/health.go`, `handlers/handler.go`)
  - Every 429 seen on an account escalates its cross-request cooldown:
    `60s × 2^(consecutive hits − 1)`, capped at 900s. New sessions route
    around cooling accounts (`NextLive` skips them); one successful
    completion clears the escalation. This complements the in-request
    backoff — without it, the next request after a failed one rammed the
    same throttled account again.
  - Grace window: when *every* account is cooling, the one recovering
    soonest (≤45s left) is still offered, so the pool never 503s
    prematurely.
- **BAN circuit breaker** (`handlers/health.go`, `handlers/handler.go`)
  - Upstream errors carrying ban wording (“封禁”, “被封”, “封号”, “封停”,
    “账号已被限制”, “账号异常”, “banned”, “suspended” — matched on the error
    path only, never on reply content) quarantine the account and *hold*
    it out of rotation and out of probes.
  - New env `BAN_QUARANTINE_HOURS` (default `168` = 7 days): the hold
    duration; `0` = hold until a probe succeeds (legacy behaviour). After
    the hold expires, probes resume and a success revives the account.
- New startup banner line reporting the ban-hold window.

- **Per-account rate limiting** (`handlers/limiter.go`)
  - Token-bucket QPM (requests per minute) limiter per DeepSeek account, keyed by `deviceID`.
  - In-flight concurrency semaphore per account to prevent burst flooding.
  - Sliding-minute window: tokens auto-refill when the minute rolls over.
  - Semaphore capacity grows dynamically if `MAX_CONCURRENT_PER_ACCOUNT` is raised at runtime.

- **429 exponential-backoff retry with account rotation** (`handlers/handler.go`)
  - On a `429 Too Many Requests` response, the handler waits with exponential backoff (`baseDelay * 2^(attempt-1)`, capped at 5 min).
  - Each retry switches to the next account in the round-robin pool and starts a fresh DeepSeek session.
  - Non-429 errors are surfaced immediately without retry.

- **New environment variables** (`config/config.go`)
  | Variable | Default | Description |
  |---|---|---|
  | `ACCOUNT_QPM` | `30` | Max requests per account per sliding minute |
  | `MAX_CONCURRENT_PER_ACCOUNT` | `5` | Max in-flight completions per account simultaneously |
  | `RETRY_MAX_ATTEMPTS` | `3` | Maximum 429 retry attempts before giving up |
  | `RETRY_BASE_DELAY` | `2s` | Base delay for exponential backoff (actual waits: 2s, 4s, 8s…) |

- **Startup log lines** (`main.go`)
  - Prints `account qpm`, `max concurrent/account`, `retry max attempts`, and `retry base delay` on launch.

- **OpenAI-compatible tool calling** (`handlers/tools.go`, `handlers/tools_stream.go`)
  - Accepts `tools` / `tool_choice` / `response_format` on `/v1/chat/completions`; the tool schema and a
    strict DSML call-format protocol are injected into the system block on every turn.
  - Model output is scanned for DSML `<｜｜DSML｜｜ calls>` blocks and `<|tool_calls|>` sentinels
    (tolerant parser: half/full-width pipes, case, missing close tags); replies with calls return
    `message.tool_calls` + `content: null` + `finish_reason: "tool_calls"` per the OpenAI spec.
  - Streaming responses emit `tool_calls` delta chunks via a stateful detector; marker fragments are
    stripped from `content` so no artifact reaches the client.
  - Echoed `role: "tool"` results and assistant `tool_calls` are re-rendered into the prompt, and the
    conversation-prefix cache key now includes the canonicalized `tool_calls` structure, so agent
    loops (e.g. SiYuan Note) keep reusing one upstream session across round-trips.
  - Agent tool loops are exempted from the random session-reset chance; their read-time cooldown is
    capped at 2 s so the loop stays responsive.
  - Strict tool-name mode: calls whose name is not in the declared tool set are dropped, not forwarded.
  - `response_format: {"type":"json_object"}` injects a JSON-only directive.

- **Account token-expiry detection** (`handlers/health.go`)
  - A dedicated detection log file (`HEALTH_LOG_FILE`, defaults to the main log's directory,
    e.g. `/app/logs/deepseek-health.log`) records
    quarantines, revivals and a running one-line summary; tokens are only ever printed masked
    (`ab12cd…ef90`).
  - **Startup check** (`STARTUP_ACCOUNT_CHECK`, default on): every account is probed before the
    server starts, using a side-effect-free auth request (PoW-challenge fetch).
  - **Periodic probes**: live accounts every `HEALTH_PROBE_INTERVAL` (default 30 m), quarantined
    accounts retried every `HEALTH_PROBE_DEAD_RETRY` (default 10 m) so refreshed tokens revive.
  - **Passive quarantine**: a 401/403 seen during a real completion quarantines the account
    immediately; conversations bound to it migrate to a live account with full history replay.
    If every account is dead, requests fail fast with 503.
  - Quarantine happens only on auth failures (401/403), never on 429 / 5xx / network errors.

- **Tool-parsing robustness + reasoning fallback**
  (ported from webai2api v0.1.20-v0.1.32, session behavior unchanged)
  - **Argument-name correction**: a wrong argument key is renamed to the schema
    parameter when a case-insensitive substring match is unique (`path` →
    `filePath`); ambiguous or unknown keys are kept as-is.
  - **Loose JSON repair**: model JSON with code fences, trailing commas or
    full-width punctuation is repaired before parsing (sentinel lines and DSML
    values).
  - **Bare-JSON fallback** (`TOOL_JSON_FALLBACK`, default off): unmarked call
    JSON is sniffed with a double criterion (name in the declared whitelist AND
    an arguments/args key), so ordinary JSON and code samples stay text; the
    streaming detector gains a matching JSON state.
  - **Reasoning fallback** (`REASONING_FALLBACK`, default on): when the reply
    body is empty but the thinking block carries the answer, the thinking text
    is promoted to content (non-streaming swap / trailing content delta), so
    clients never get a blank reply.
   - Note: an earlier strategy-3 gate (SESSION_RECENT_FALLBACK) was tried and
     reverted — it broke single-user session continuity, so session behavior
     stays exactly as v1.1.1.

- **Task-level account distribution via conversation-start fingerprint**
  (`handlers/handler.go`, `handlers/conversations.go`)
  - New `CONV_FINGERPRINT_ROUTE` (default off): when enabled, in-progress
    requests re-attach to their own session by the fingerprint of the
    conversation opening (system + first user message + first assistant reply
    + model), which stays robust even when the rolling-prefix cache misses.
  - When enabled, the first turn of a new task (no assistant message yet)
    skips the recent-session fallback and gets a fresh web session on the next
    account in round-robin order, so distinct tasks spread across accounts.
   - **Round-robin fix**: the prefix→account binding used Go's single-value map
     read, so an unbound prefix silently resolved to account 0 and every new
     session pinned to the first account; unbound prefixes now genuinely
     rotate through all live accounts.

- **Session persistence across restarts** (`handlers/persist.go`,
  `handlers/handler.go`, `handlers/conversations.go`)
  - New `PERSIST_SESSIONS` (default off) + `DATA_DIR` (default: the `data/`
    folder one level beside the log dir). The conversation index
    (prefix / fingerprint / session-id keys → session record incl. owning
    account) is snapshotted to `<DATA_DIR>/conversations.json`.
  - Writes are throttled (5 s, coalesced triggers) and atomic (tmp + rename);
    a final flush runs on shutdown. A corrupt file is quarantined
    (`*.corrupt-<ts>`) instead of crashing.
  - On startup the index is restored: sessions keep their prefix/fingerprint
    lookups, their prefix→account binding (so task a stays on account A), and
    their turn counts. Entries past their idle TTL, or whose account no longer
    exists / is quarantined, are dropped.
  - **Session self-heal**: a cached session that fails with a non-auth error
    (404/5xx/network — "the upstream session is probably dead") is recreated
    on the same account with a full history replay and retried once
    (non-streaming requests only). 401/403 still go to quarantine+migration;
    429 to backoff.
  - New pool helper `FindByMask` re-maps persisted account ids (masked token
    prefixes) to current pool indices after an `accounts.txt` reorder.

- **Multi-client proxy keys + cross-client isolation**
  (`config/config.go`, `handlers/handler.go`)
  - The proxy now accepts any number of proxy API keys: `PROXY_API_KEY`
    (optional when the file supplies keys) plus one-per-line entries in
    `PROXY_KEYS_FILE` (default `proxy-keys.txt`). `PROXY_API_KEY` is no longer
    strictly required — at least one key overall is.
  - Each key gets a stable, order-independent id (first 8 bytes of sha256)
    that is mixed into **both** the prefix cache key and the
    conversation-opening fingerprint. Distinct clients (distinct keys) can
    therefore never share a DeepSeek web session.
  - **Cross-client crosstalk fix**: with `CONV_FINGERPRINT_ROUTE` on, the
    most-recent-session fallback is now skipped for *all* turns (not just
    first turns), so an unknown-fingerprint request starts its own session
    instead of leaking into another client's most-recent one. And
    `RegisterFingerprint` only runs when the session is content-verified
    (created / fingerprint / prefix / conversation_id) — never on a bare
    recent fallback, which previously could lock a foreign client's
    fingerprint into another client's session and persist it.
  - Note: upgrading changes the persisted prefix/fingerprint keys (keyID was
    added to the hash), so every task cold-starts once; delete a contaminated
    `conversations.json` to reset.


### Changed

- `config.Config` struct extended with the four new fields above.
- `handlers.Handler` now holds an `*AccountLimiter` initialised from `cfg.AccountQPM`.
- `ChatCompletion` acquires a rate-limit token and a concurrency slot before invoking the upstream completion; both are released via `defer`.
- `doCompletionWithRetry` centralises session creation so that a retry on a new account can start a clean session.

### Why

These changes reduce the risk of DeepSeek web anti-fraud detection:
- Human-like request pacing (token bucket ≈ 30 req/min per account).
- Bounded concurrency prevents all accounts from hammering the upstream simultaneously.
- Graceful 429 recovery rotates load across accounts instead of failing hard.
