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

Multiple accounts: create `accounts.txt` in the working directory, one token per
line (blank lines and `#` comments are ignored):

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
| `CONVERSATION_TTL` | — | 旧版单值（兼容），现已弃用，请使用 `CONVERSATION_TTL_OPTIONS` |
| `CONVERSATION_TTL_OPTIONS` | `10m,60m,2h,5h,0` | 会话 TTL 随机池，逗号分隔；`0` 表示永不删除。每次新建会话随机选取一个，让清理时间不可预测 |
| `MAX_CONVERSATIONS` | `1024` | Max sessions kept in memory (LRU eviction) |
| `ACCOUNT_QPM` | `6` | 每账号每秒滑动窗口内最大请求数（正常人约 1-6 QPM，10-60s/条） |
| `MAX_CONCURRENT_PER_ACCOUNT` | `5` | 每账号同时进行的最大并发请求数（= 同时运行的网页会话数上限） |
| `RETRY_MAX_ATTEMPTS` | `3` | Max 429 retry attempts before giving up |
| `RETRY_BASE_DELAY` | `2s` | Base delay for exponential backoff (actual waits: 2s, 4s, 8s…) |
| `REQUEST_JITTER_MIN` | `200ms` | Min random delay (ms) before each completion request |
| `REQUEST_JITTER_MAX` | `800ms` | Max random delay (ms) before each completion request |
| `POW_JITTER_MIN` | `30ms` | Min artificial delay (ms) when solving POW challenge |
| `POW_JITTER_MAX` | `150ms` | Max artificial delay (ms) when solving POW challenge |
| `SESSION_CREATE_JITTER` | `200ms` | Random delay before creating a new DeepSeek session |
| `DEVICE_ID_ROTATE_HOURS` | `0` | Device ID 定期轮换间隔（小时，0=不轮换） |
| `REQUEST_READ_TIME_MIN` | `5s` | 同一账号两次请求之间的最小等待（秒），模拟阅读回复 |
| `REQUEST_READ_TIME_MAX` | `30s` | 同一账号两次请求之间的最大等待（秒） |
| `SESSION_RESET_CHANCE` | `0.15` | 每次请求有 X% 概率开新会话（同时影响 Strategy 2 和 Strategy 3，控制连续问答的"断句"频率） |
| `REQUESTS_PER_SESSION` | `0` | 每个会话最大请求数，到达后强制创建新会话（0=关闭，与 SESSION_RESET_CHANCE 配合使用） |
| `LOG_FILE` | `/app/logs/app.log` | 日志文件路径，留空只输出到 stdout；设置后日志同时写入文件 |
| `STARTUP_ACCOUNT_CHECK` | `true` | 启动时对全部账号做一次轻量鉴权探测，失效账号自动隔离 |
| `HEALTH_PROBE_INTERVAL` | `30m` | 周期探测所有存活账号的间隔（0=关闭） |
| `HEALTH_PROBE_DEAD_RETRY` | `10m` | 对被隔离账号的重试间隔（token 换新后自动复活） |
| `HEALTH_LOG_FILE` | 同 `LOG_FILE` 目录 | 检测日志文件：隔离/复活/摘要事件，token 只显示掩码；留空默认与主日志同目录（如 `/app/logs/deepseek-health.log`） |
| `BAN_QUARANTINE_HOURS` | `168` | Hold window for a BAN-detected account (0 = hold until a probe succeeds). 429 account-level backoff is built in, needs no config |
| `TOOL_JSON_FALLBACK` | `false` | Sniff unmarked bare JSON as tool calls (double criterion: name in whitelist AND has arguments); opt-in |
| `REASONING_FALLBACK` | `true` | Promote thinking text to content when the reply body is empty |
| `CONV_FINGERPRINT_ROUTE` | `false` | Route by conversation-opening fingerprint: first turn of a new task gets a fresh session on the next round-robin account; in-progress tasks re-attach to their own session/account |
| `PERSIST_SESSIONS` | `false` | Snapshot the conversation index to a local data dir so in-progress tasks resume (same session + account) after a restart |
| `DATA_DIR` | `data/` beside the log dir | Directory for the persisted session index (`<DATA_DIR>/conversations.json`); empty = the `data/` folder one level beside the log folder |

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
exposed to callers.

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
- Do **not** commit your tokens. `accounts.txt` is already in `.gitignore`.

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
