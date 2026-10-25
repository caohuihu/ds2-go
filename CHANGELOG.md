# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added

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

- **Agent hardening: session isolation + tool parsing + reasoning fallback**
  (ported from webai2api v0.1.20-v0.1.32)
  - **Strategy-3 gate** (`SESSION_RECENT_FALLBACK`, default on): agent requests
    (declared tools or tool history) never attach to a recent-session fallback; a
    prefix miss becomes a fresh session with a lossless full-history replay, so
    two conversations can no longer borrow each other's context.
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
