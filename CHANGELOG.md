# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Fixed

- **Robust tool-marker parsing** (port of webai2api v0.1.51 audit, `handlers/tools.go`, `handlers/tools_stream.go`): the upstream web UI occasionally drops the leading `<` of a marker, shipping residue like `｜｜DSML｜｜ calls>` or `</｜｜DSML｜｜ invoke name="...">`. Every DSML regex was `<`-anchored, so the residue leaked into reply content. In tools mode the scanner now recognizes `<`-less openers; calls-layer fragments are stripped in place (half invoke blocks stay conservatively dropped, per the existing contract); a close-shaped opener carrying a `name=` attribute is corrected to an opener; and on stream cuts the withheld SCAN-state tail is recovered (`recoverTextOnCut`) with a marker-fragment trim (≤64 chars, markdown tables untouched), so the client keeps the last plain-text chunk before a cut.
- **Unterminated `<parameter>` values are truncated at the next marker tag**, so residue can no longer pollute a tool argument value.
- **Tri-state `string` attribute**: when the attribute is omitted the value is parsed as JSON first and falls back to a string (`42` → number, `hello` stays a string); an explicit `string="true"` keeps the raw string.
- The no-tools contract is unchanged: marker fragments stay ordinary text (regression-tested).
- **Expired-token detection covers the HTTP-200 envelope form** (`deepseek.IsAuthFailure`, `handlers/handler.go`, `handlers/health.go`): when a `userToken` in `accounts.txt` expires, the web API rejects it with **HTTP 200 + envelope `code: 40003 "Authorization Failed (invalid token)"`** — every detection point checked only HTTP 401/403, so the dead account stayed "live" forever and every session bound to it kept failing. Auth failure is now recognized via `deepseek.IsAuthFailure` (401/403, or code 40003 / "invalid token" wording) in all four places: the passive mid-request quarantine (the session then migrates to a live account with a full history replay), the startup check and periodic probes (EXPIRED → quarantined, revived on first success), the self-heal refusal (no pointless same-account recreate), and the client-facing error code (401 instead of 502).

### Added

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
