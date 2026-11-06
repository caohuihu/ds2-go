<div align="center">
  <img src="assets/logo.png" alt="deepseek2api" width="180" />

  <h1>deepseek2api</h1>

  <p><strong>Turn <a href="https://chat.deepseek.com">chat.deepseek.com</a> into an OpenAI-compatible API.</strong><br/>
  A lightweight, dependency-free Go proxy that speaks the OpenAI Chat Completions protocol.</p>

  <p>
    <a href="https://github.com/0xgetz/deepseek2api/stargazers"><img alt="GitHub stars" src="https://img.shields.io/github/stars/0xgetz/deepseek2api?style=for-the-badge&logo=github&color=7c5cff"></a>
    <a href="https://github.com/0xgetz/deepseek2api/network/members"><img alt="GitHub forks" src="https://img.shields.io/github/forks/0xgetz/deepseek2api?style=for-the-badge&logo=github&color=26e0c8"></a>
    <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/github/license/0xgetz/deepseek2api?style=for-the-badge&color=4f8bff"></a>
  </p>
  <p>
    <img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/0xgetz/deepseek2api?style=for-the-badge&logo=go&color=00add8">
    <img alt="Zero dependencies" src="https://img.shields.io/badge/dependencies-0-brightgreen?style=for-the-badge">
    <img alt="Docker ready" src="https://img.shields.io/badge/docker-ready-2496ed?style=for-the-badge&logo=docker&logoColor=white">
  </p>
  <p>
    <a href="README.md"><img alt="English" src="https://img.shields.io/badge/lang-English-4f8bff?style=flat-square"></a>
    <a href="README.id.md"><img alt="Bahasa Indonesia" src="https://img.shields.io/badge/lang-Indonesia-26e0c8?style=flat-square"></a>
    <a href="README.zh.md"><img alt="中文" src="https://img.shields.io/badge/lang-中文-ff6b6b?style=flat-square"></a>
    <a href="README.ja.md"><img alt="日本語" src="https://img.shields.io/badge/lang-日本語-ffb454?style=flat-square"></a>
    <a href="README.es.md"><img alt="Español" src="https://img.shields.io/badge/lang-Español-7c5cff?style=flat-square"></a>
  </p>
</div>

---

## What is this?

`deepseek2api` wraps the **chat.deepseek.com web app** behind a clean,
OpenAI-compatible HTTP API. It is written in pure Go using only the standard
library, so it ships as a single static binary with no runtime dependencies.

Point any OpenAI client at it — Cherry Studio, LobeChat, Open WebUI, the
official OpenAI SDKs, `curl` — and talk to DeepSeek without an official API key.

> **Bring your own account.** You supply the web `userToken` from your own
> logged-in browser session. The proxy never phones home.

## Features

- **OpenAI Chat Completions compatible** — `POST /v1/chat/completions`, streaming and non-streaming.
- **Model listing** — `GET /v1/models`.
- **Health endpoint** — `GET /health` (no auth) returns `200 {"status":"ok"}` for container / uptime checks.
- **Image input** — `image_url` content blocks accept base64 data URLs and http(s) links; images are uploaded to DeepSeek and sent as `ref_file_ids`.
- **Automatic proof-of-work** — the server solves the web client's DeepSeekHashV1 challenge (a 23-round Keccak variant) in parallel, typically under 100 ms.
- **Reasoning support** — thinking output is mapped to `reasoning_content`, matching the official DeepSeek API.
- **Multi-account token pool** — round-robin across accounts; tokens stay server-side.
- **Multi-turn context** — automatic prefix caching reuses a DeepSeek session; an explicit `conversation_id` passthrough is also supported.
- **Future-proof model names** — unknown `deepseek-*` ids are mapped heuristically (reasoner/think/search keywords), so clients that hardcode newer names keep working.
- **Self-cleaning** — idle sessions are deleted upstream so your web chat list stays tidy.
- **Anti-fraud rate limiting** — per-account QPM token bucket + in-flight concurrency semaphore prevent burst flooding.
- **429 exponential-backoff retry** — on rate-limit, waits with exponential backoff and rotates to a different account.
- **Behavioral randomization** — random UA/version/locale per client, request jitter, simulated POW solving delay, and optional device ID rotation to reduce fingerprint similarity.
- **Docker / Docker Compose** ready.

## Supported models

| Model ID | Web behavior | Aliases |
| --- | --- | --- |
| `deepseek-chat` | Fast mode | `deepseek-v3` |
| `deepseek-reasoner` | Fast mode + deep thinking | `deepseek-r1` |
| `deepseek-search` | Fast mode + web search | |
| `deepseek-reasoner-search` | Deep thinking + web search | |

## Getting a token

1. Log in to [chat.deepseek.com](https://chat.deepseek.com) in your browser.
2. Open DevTools (F12) → Console and run:

   ```js
   JSON.parse(localStorage.userToken).value
   ```

3. Copy the output. It stays valid until you sign out; repeat for extra accounts.

## Quick start

### Build

```bash
go build -o deepseek2api .
```

### Run

Single account:

```bash
PROXY_API_KEY='your-proxy-key' DEEPSEEK_TOKEN='your-token' PORT=8080 ./deepseek2api
```

Multiple accounts: fill in the `accounts.txt` file in the working directory,
one token per line (blank lines and `#` comments are ignored). The repo ships
an empty placeholder `accounts.txt` on purpose — it reminds you where the
token file goes; keep the committed copy empty and never commit real tokens:

```text
eyJhbGciOi...token1
eyJhbGciOi...token2
```

```bash
PROXY_API_KEY='your-proxy-key' ./deepseek2api
```

### Test

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Authorization: Bearer your-proxy-key' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "deepseek-reasoner",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

## Docker

```bash
docker build -t deepseek2api .

docker run --rm -p 8080:8080 \
  -e PROXY_API_KEY='your-proxy-key' \
  -e DEEPSEEK_TOKEN='your-token' \
  deepseek2api
```

Or with Compose:

```bash
PROXY_API_KEY='your-proxy-key' DEEPSEEK_TOKEN='your-token' docker compose up --build
```

Or pull the prebuilt multi-arch image that CI (`.github/workflows/docker.yml`)
pushes to GHCR — `docker-compose-ghcr.yml` points at `ghcr.io/caohuihu/ds2-go:latest`,
no local build needed:

```bash
docker compose -f docker-compose-ghcr.yml up
```

> **Risk-hardened preset**: for long-running, multi-account deployments, copy `.env.risk-hardened.example` to `.env` — three layers of fraud protection (human-like behavior / per-account quota + breaker / task binding with zero replay) plus a 30m risk log; values and rationale are commented in the file.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `PORT` | `8080` | Local HTTP port |
| `PROXY_API_KEY` | none | Key clients use to call the proxy; can be empty when `PROXY_KEYS_FILE` supplies keys. At least one key (env or file) is required |
| `PROXY_KEYS_FILE` | `proxy-keys.txt` | Multi-client keys, one per line. Give each client (SiYuan / opencode / …) a distinct key to fully isolate its DeepSeek sessions |
| `DEEPSEEK_TOKEN` | empty | DeepSeek web `userToken` |
| `DEEPSEEK_ACCOUNTS_FILE` | `accounts.txt` | Multi-account file, one token per line |
| `DEEPSEEK_BASE_URL` | `https://chat.deepseek.com` | Upstream base URL |
| `DEFAULT_MODEL` | `deepseek-chat` | Model used when a request omits one |
| `CONVERSATION_TTL` | — | Legacy single-value form, deprecated — use `CONVERSATION_TTL_OPTIONS` |
| `CONVERSATION_TTL_OPTIONS` | `10m,60m,2h,5h,0` | Random session-TTL pool, comma-separated; `0` = never delete. Each new session picks one at random, making cleanup timing unpredictable |
| `MAX_CONVERSATIONS` | `1024` | Max sessions kept in memory (LRU eviction) |
| `ACCOUNT_QPM` | `6` | Max requests per account within a sliding one-minute window (a normal human runs about 1–6 QPM, one every 10–60 s) |
| `MAX_CONCURRENT_PER_ACCOUNT` | `5` | Max in-flight concurrent requests per account (= cap on simultaneously open web sessions) |
| `RETRY_MAX_ATTEMPTS` | `3` | Max 429 retry attempts before giving up |
| `RETRY_BASE_DELAY` | `2s` | Base delay for exponential backoff (actual waits: 2s, 4s, 8s…) |
| `REQUEST_JITTER_MIN` | `200ms` | Min random delay (ms) before each completion request |
| `REQUEST_JITTER_MAX` | `800ms` | Max random delay (ms) before each completion request |
| `POW_JITTER_MIN` | `30ms` | Min artificial delay (ms) when solving POW challenge |
| `POW_JITTER_MAX` | `150ms` | Max artificial delay (ms) when solving POW challenge |
| `SESSION_CREATE_JITTER` | `200ms` | Random delay before creating a new DeepSeek session |
| `DEVICE_ID_ROTATE_HOURS` | `0` | Device-ID rotation interval in hours (0 = no rotation) |
| `REQUEST_READ_TIME_MIN` | `5s` | Min wait between two requests to the same account (simulates reading the reply) |
| `REQUEST_READ_TIME_MAX` | `30s` | Max wait between two requests to the same account |
| `SESSION_RESET_CHANCE` | `0.15` | Per-request probability (X%) of opening a new session; affects both Strategy 2 and Strategy 3, controlling how often a task is "cut" into a new conversation |
| `REQUESTS_PER_SESSION` | `0` | Max requests per session; a new session is forced after this (0 = off, use together with `SESSION_RESET_CHANCE`) |
| `SESSION_REPLAY_TURNS` | `2` | Seeding turns when a new session opens: in agent tool flows, how many complete turn groups (user instruction + its assistant/tool exchange) are replayed before the current instruction. `2` = default (last 2 turns); `0` = current instruction only (thinnest trace); `all` = full history (legacy). Plain chat is unaffected |
| `LOG_FILE` | empty | Log file path; empty = stdout only. When set, logs go to both the file and stdout |
| `STARTUP_ACCOUNT_CHECK` | `true` | Lightweight auth probe of all accounts at startup; expired accounts are quarantined automatically |
| `HEALTH_PROBE_INTERVAL` | `30m` | Interval for probing all live accounts (0 = off) |
| `HEALTH_PROBE_DEAD_RETRY` | `10m` | Retry interval for quarantined accounts (revives automatically once the token is refreshed) |
| `HEALTH_LOG_FILE` | next to `LOG_FILE` | Detection log file: quarantine / revival / summary events; tokens shown masked only. Empty = same folder as the main log (e.g. `/app/logs/deepseek-health.log`) |
| `BAN_QUARANTINE_HOURS` | `168` | Hold window for a BAN-detected account (0 = hold until a probe succeeds). 429 account-level backoff is built in, needs no config |
| `DAILY_REQUEST_LIMIT` | `0` | Per-account daily request budget (resets by local date, counters persisted across restarts); 0 = unlimited. When every account is spent, new sessions 503 until midnight; cached sessions are unaffected |
| `LONG_PAUSE_CHANCE` | `0` | Per-request probability of an extra 20–60s human-like pause (agent tool loops are exempt); 0 = off |
| `AUTO_CONTINUATION` | `false` | Nudge a silent empty upstream reply (no body, no thinking) with a "Continue" prompt on the same web session, up to `AUTO_CONTINUATION_MAX` consecutive nudges; continuation turns count toward the session turn cap and the daily quota |
| `AUTO_CONTINUATION_MAX` | `2` | Max "Continue" nudges per request (only when `AUTO_CONTINUATION=true`) |
| `RISK_STATS_INTERVAL` | `0` | Log a per-account risk summary at this interval (global requests / sessions created / cache hits, plus per-account completions, consecutive 429s, cooldown & BAN remaining, daily quota, in-flight); 0 = off |
| `TOOL_JSON_FALLBACK` | `false` | Sniff unmarked bare JSON as tool calls (double criterion: name in whitelist AND has arguments); opt-in |
| `REASONING_FALLBACK` | `true` | Promote thinking text to content when the reply body is empty |
| `CONV_FINGERPRINT_ROUTE` | `false` | Route by conversation-opening fingerprint: first turn of a new task gets a fresh session on the next round-robin account; in-progress tasks re-attach to their own session/account |
| `PERSIST_SESSIONS` | `false` | Snapshot the conversation index to a local data dir so in-progress tasks resume (same session + account) after a restart |
| `DATA_DIR` | `data/` beside the log dir | Directory for persisted state: session index (`conversations.json`, with PERSIST_SESSIONS), daily quota (`daily-usage.json`) and per-account device fingerprints (`device-fingerprints.json`, always on); empty = the `data/` folder one level beside the log folder |

## Multi-turn conversations

- **Automatic mode (recommended).** Clients send the full `messages` array as
  usual. The proxy fingerprints the history (everything except the last
  message); on a hit it reuses the same DeepSeek session and sends only the new
  turn, otherwise it creates a fresh session. Mainstream clients work with no
  changes.
- **Passthrough mode.** Responses include a non-standard `conversation_id`
  field (the DeepSeek session id). Send it in a later request body to reuse that
  session; only the last user message is used as the prompt.
- **Task-level account distribution** (`CONV_FINGERPRINT_ROUTE`, default off).
  With several concurrent tasks, each task keeps its own web session and new
  tasks are dealt the next account in round-robin order, spreading load across
  accounts. When on, requests without assistant history no longer use the
  recent-session fallback (each becomes a fresh session).
- **Round-robin fix.** Unbound prefixes previously resolved to account 0
  (Go's map zero value), pinning every new session to the first account; new
  sessions now genuinely rotate across all live accounts.
- **Load-aware selection.** When a prefix needs re-binding (unbound, its
  account died, or its daily quota is spent) the pool picks the *least
  busy* live account — fewest in-flight completions — with round-robin
  breaking ties, so new traffic no longer queues behind the busiest account.
- **Daily quota** (`DAILY_REQUEST_LIMIT`, default off). Each account's
  requests are counted per local day and persisted to
  `<DATA_DIR>/daily-usage.json` (restart-safe, resets at midnight). An
  account at its budget is skipped by new-session selection; when *all* are
  spent, new sessions fail fast with 503 until midnight while cached
  sessions keep working.
- **Stable per-account device fingerprint** (`<DATA_DIR>/device-fingerprints.json`, always on).
  Before this, *every* process start re-randomized all accounts' user
  agents, web-client versions, locales and device ids — and the device id
  was never persisted, so each restart made every account look like a brand
  new device enrolling again (a device-pool tell). Each account's identity is
  now picked once from a human-weighted pool (current web-client version
  preferred, `zh_CN` locale preferred, Windows Chrome/Edge dominant) and
  pinned in the file: restarts present the same device, and an account
  removed from and re-added to `accounts.txt` resumes its old identity.
  The timezone offset is pinned too — set the container `TZ` (e.g.
  `Asia/Shanghai`) before the first start, since a China-based account
  reporting UTC+0 is a tell in its own right.
- **Human-like long pause** (`LONG_PAUSE_CHANCE`, default off). With that
  probability any request (agent tool loops exempt) waits an extra random
  20–60 s before hitting upstream — a "stepped away" long tail on top of the
  5–30 s read-time cooldown, making the per-account request rhythm harder
  to fingerprint as machine-paced.
- **Silent-reply auto-continuation** (`AUTO_CONTINUATION`, default off).
  The upstream occasionally returns a *silent* successful completion
  (empty body, empty thinking) — the classic long-task stall mode. When
  enabled, the proxy nudges the **same web session** with a "Continue"
  prompt chained off the empty reply, up to `AUTO_CONTINUATION_MAX`
  consecutive nudges; the continuation's deltas flow into the same reply
  stream, so the client sees one seamless answer. Continuation turns count
  toward `REQUESTS_PER_SESSION` and the daily quota; tool-flow turns and
  replies with any content never trigger it.
- **Fresh-session seeding** (`SESSION_REPLAY_TURNS`, default `2`, agent
  tool flows only). The DeepSeek web page holds no state, so when a new
  upstream session is opened the proxy must "paste" relevant history into
  the prompt. N = how many complete turn groups (a user instruction plus
  the assistant/tool exchange it produced) are replayed ahead of the
  current instruction: `2` = default (keeps the last two turns, so long
  tasks survive a session swap without the paste volume looking machine-
  generated); `0` = the current instruction only (thinnest trace, fine for
  short Q&A); `all` = full history (the pre-1.10.3 behavior: richest
  context, most conspicuous trace). Applies on session resets
  (`SESSION_RESET_CHANCE` / `REQUESTS_PER_SESSION`) and when a dead cached
  session is recreated; the plain chat path is untouched.
- **Risk observability** (`RISK_STATS_INTERVAL`, default off). At that
  interval the main log gets a risk summary: global request / session-created
  / cache-hit counters, a per-request outcome breakdown
  (429 / auth / ban / upstream / empty-reply / client-cancelled) and the mean
  request latency, plus per-account successful completions,
  consecutive-429 count, cooldown & BAN-hold remaining, daily-quota usage
  and in-flight count. The created-vs-hits ratio is the primary risk signal —
  new sessions are the expensive, fingerprintable upstream action. 429/503
  responses also carry a `Retry-After` hint derived from the pool's longest
  active 429 cooldown.
- **Multi-client isolation** (`PROXY_KEYS_FILE`, active with ≥2 keys). Give each
  client a distinct proxy key (e.g. SiYuan uses key 1, opencode uses key 2). The
  key's stable id is mixed into the **prefix and fingerprint keys**, so distinct
  clients can never share a DeepSeek web session — this eliminates the "client B's
  instruction lands in client A's session" cross-talk. Different tasks under the
  same key still route to different accounts by fingerprint.
- **Session persistence** (`PERSIST_SESSIONS`, default off). When on, the
  "prefix / fingerprint / session-id → session record (incl. owning account)"
  index is snapshotted to `<DATA_DIR>/conversations.json` (default: the `data/`
  folder one level beside the log dir). On restart it is restored so task a
  keeps hitting the same web session and the same account A — no context
  replay. Entries whose idle TTL has lapsed, or whose account is gone or
  quarantined, are dropped at load. Writes are throttled (5 s) and atomic;
  a final flush happens on shutdown. Additionally, a cached session that
  fails with a non-auth error (404/5xx/network — "the upstream session is
  probably dead") is self-healed by recreating the session on the same
  account and retrying once (non-streaming requests only).

> Cold start note: after a restart, if the history has more than one turn, only
> the system prompt and the latest user message are replayed. This is a
> deliberate simplification.

## Tool calling (function calling)

The upstream web backend has no native function-calling API, so the proxy
simulates it at the text layer (the same approach webai2api uses):

- Send OpenAI-style `tools` / `tool_choice` in the request body. The proxy
  injects the tool schema plus a strict DSML call-format protocol into the
  system block on **every** turn, so agent loops (Siyuan, Open WebUI, Cherry
  Studio, any OpenAI SDK) keep working across many tool round-trips.
- Replies that carry a tool call are parsed out of the model text (DSML
  `<｜｜DSML｜｜ calls>` blocks and `<|tool_calls|>` sentinels, tolerant to
  half/full-width pipes, case, missing close tags). The response then follows
  the OpenAI spec: `message.tool_calls` populated, `content: null`,
  `finish_reason: "tool_calls"`. Marker fragments are stripped from the
  content so no artifact leaks to the client.
- Echoed `role: "tool"` results and assistant `tool_calls` are re-rendered
  back into the prompt (sentinel form / `[Tool Result id=...]` lines), and
  the conversation-prefix cache key includes the canonicalized
  `tool_calls` structure so each agent turn reuses the same upstream
  session.
- `tool_choice`: `auto` (default), `none` (tools ignored), `required`, or
  `{"type":"function","function":{"name":"..."}}`.
- The parser tolerates the upstream's *degraded* marker forms: when the web
  UI drops the leading `<` of a marker (shipping `｜｜DSML｜｜ calls>` or
  `</｜｜DSML｜｜ invoke name="...">` as-is), tools mode strips the
  calls-layer residue, corrects close-shaped openers that carry a `name=`
  attribute, and cuts unterminated parameter values at the next marker — so
  residue never leaks into content or argument values; plain chat (no tools)
  keeps such text intact. On a stream cut the detector recovers the last
  plain-text tail it was withholding (≤64 bytes, marker fragments trimmed).
- Unknown tool names emitted by the model are dropped (strict mode), not
  forwarded to the agent.
- Argument keys the model gets wrong are corrected against the schema when a
  case-insensitive substring match is unique (`path` → `filePath`); sloppy
  JSON (fences, trailing commas, full-width punctuation) is repaired before
  parsing. Bare-JSON calls without markers can be sniffed with
  `TOOL_JSON_FALLBACK=true` (double criterion: declared name AND arguments).
- Agent tool loops are exempted from the random session-reset chance and the
  read-time cooldown is capped at 2s so the loop stays responsive.
- `response_format: {"type":"json_object"}` injects a "JSON only" directive.

## Account health (token expiry detection)

DeepSeek web tokens expire silently; this proxy detects it instead of only
finding out mid-request:

- **Startup check** — before serving, every account is probed with a
  side-effect-free auth request (PoW-challenge fetch). Expired accounts are
  quarantined up front.
- **Periodic probes** — every `HEALTH_PROBE_INTERVAL` (default 30 min) all
  live accounts are re-probed; quarantined accounts are retried every
  `HEALTH_PROBE_DEAD_RETRY` (default 10 min) so a refreshed token revives
  without a restart.
- **Passive quarantine** — a 401/403 observed during a real completion
  quarantines that account immediately. Conversations bound to it are
  migrated to a live account with a full history replay; if *all* accounts
  are dead, requests fail fast with a clear 503.
- **Expired-token envelope form** — when the web API rejects a dead
  `userToken` with its HTTP-200 envelope form (`code: 40003 "Authorization
  Failed (invalid token)"`), the startup check, the periodic probes and the
  passive path all recognize it via `deepseek.IsAuthFailure` (401/403, or
  code 40003 / "invalid token" wording) and quarantine the account — the
  bound session then migrates to a live account automatically instead of
  failing forever on the dead one.
- **Account-level 429 backoff** — every 429 seen on an account escalates a
  cross-request cooldown: `60s × 2^(consecutive hits − 1)` (60s → 120s →
  240s … capped at 900s). New sessions route around the cooling account
  until one completion succeeds and clears it. When *every* account is
  cooling, the one recovering soonest is still offered, but only with ≤45s
  left, so a full backoff window is respected instead of 503ing.
- **BAN circuit breaker** (`BAN_QUARANTINE_HOURS`, default 7 days) — if an
  upstream error carries ban wording (“封禁”, “账号异常”, “banned”,
  “suspended” — matched on error paths only, never on reply content), the
  account is quarantined and *held*: no probes, no traffic, until the hold
  expires (hammering a banned account only makes the ban worse). After the
  hold, probes resume and a successful one revives it. `0` = hold forever
  until a probe succeeds.
- **Detection log** — every quarantine/revival and a running summary are
  appended to `HEALTH_LOG_FILE`. Left empty, it defaults to the main log's
  folder (`LOG_FILE=/app/logs/app.log` → `/app/logs/deepseek-health.log`), so
  both live side by side. Tokens are only ever printed masked
  (`ab12cd…ef90`), so the file is safe to keep:

  ```
  2026-10-25T01:00:03+08:00 [probe] #1/5 ab12cd…ef90 EXPIRED (upstream http 401) → quarantined
  2026-10-25T01:00:03+08:00 [summary] total=5 live=4 quarantined=1 [#1 upstream http 401]
  ```

  `tail -f` that file (next to your main log) tells you exactly which line of
  `accounts.txt` to replace.

## Authentication

Every `/v1/*` request needs:

```http
Authorization: Bearer <PROXY_API_KEY>
```

`DEEPSEEK_TOKEN` / `accounts.txt` are used server-side only and are never
exposed to callers. `GET /health` is the only endpoint that needs no key.

## Notes & limits

- **Images:** ≤ 20 MB each, up to 8 per request, png/jpg/webp/gif/bmp. Only the
  last user message's images are processed; older ones already live in the
  upstream session. Vision requests add a few seconds of first-token latency.
- **Usage** is estimated: totals come from the upstream session's cumulative
  token delta, and the split is approximated by character count.
- **Reasoning fallback**: when the model writes the final answer only into the
  thinking block (empty body), the thinking text is promoted to `content` so
  the client never gets a blank reply (`REASONING_FALLBACK`, default on).
- Upstream `429` maps to `429`; an invalid token maps to `401`.
- Do **not** commit your tokens. The repo intentionally keeps an empty
  `accounts.txt` placeholder as a reminder of where the token file goes:
  fill it in your local working copy, keep the committed copy empty.

## Project layout

```
main.go                 HTTP server and routing
config/                 environment configuration
handlers/               OpenAI-compatible handlers, SSE, image upload
deepseek/               upstream client, proof-of-work, streaming
docs/api-analysis.md    reverse-engineering notes on the upstream API
assets/                 logo and banner
```

## License

Released under the [MIT License](LICENSE).

<div align="center"><sub>Not affiliated with DeepSeek. Use responsibly and respect upstream terms.</sub></div>
