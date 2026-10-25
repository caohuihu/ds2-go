<div align="center">
  <img src="assets/logo.png" alt="deepseek2api" width="180" />

  <h1>deepseek2api</h1>

  <p><strong>把 <a href="https://chat.deepseek.com">chat.deepseek.com</a> 变成 OpenAI 兼容 API。</strong><br/>
  一个轻量、零依赖的 Go 代理，完整实现 OpenAI Chat Completions 协议。</p>

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

## 这是什么？

`deepseek2api` 把 **chat.deepseek.com 网页版**包装成干净、OpenAI 兼容的
HTTP API。使用纯 Go 标准库编写，编译为单个静态二进制文件，无任何运行时依赖。

把任意 OpenAI 客户端指向它 —— Cherry Studio、LobeChat、Open WebUI、官方
OpenAI SDK、`curl` —— 即可在没有官方 API Key 的情况下使用 DeepSeek。

> **自带账号。** 你提供自己已登录浏览器会话中的网页版 `userToken`。代理不会
> 向任何第三方发送数据。

## 功能特性

- **OpenAI Chat Completions 兼容** —— `POST /v1/chat/completions`，支持流式与非流式。
- **模型列表** —— `GET /v1/models`。
- **图片输入** —— `image_url` 内容块支持 base64 data URL 与 http(s) 链接，自动上传为 DeepSeek 文件并以 `ref_file_ids` 发送。
- **服务端自动完成 proof-of-work** —— 并行求解 DeepSeekHashV1（23 轮 Keccak 变体），通常 <100ms。
- **推理支持** —— 思考过程映射为 `reasoning_content`，与官方 API 一致。
- **多账号令牌池** —— 轮询分发，令牌仅保存在服务端。
- **多轮对话上下文** —— 自动前缀缓存复用 DeepSeek 会话，也支持显式 `conversation_id` 直通。
- **面向未来的模型名** —— 未知 `deepseek-*` 名称按启发式映射（reasoner/think/search 关键词）。
- **自动清理** —— 空闲会话自动从上游删除，保持网页版会话列表整洁。
- **Docker / Docker Compose 部署。**

## 支持的模型

| 模型 ID | 网页端行为 | 别名 |
| --- | --- | --- |
| `deepseek-chat` | 快速模式 | `deepseek-v3` |
| `deepseek-reasoner` | 快速模式 + 深度思考 | `deepseek-r1` |
| `deepseek-search` | 快速模式 + 联网搜索 | |
| `deepseek-reasoner-search` | 深度思考 + 联网搜索 | |

## 获取 Token

1. 浏览器登录 [chat.deepseek.com](https://chat.deepseek.com)；
2. F12 打开控制台，执行：

   ```js
   JSON.parse(localStorage.userToken).value
   ```

3. 复制输出的 token（退出登录前一直有效；换号重复即可）。

## 快速开始

### 构建

```bash
go build -o deepseek2api .
```

### 运行

单账号：

```bash
PROXY_API_KEY='你的代理密钥' DEEPSEEK_TOKEN='你的token' PORT=8080 ./deepseek2api
```

多账号：在工作目录创建 `accounts.txt`，每行一个 token（`#` 与空行忽略）：

```text
eyJhbGciOi...token1
eyJhbGciOi...token2
```

```bash
PROXY_API_KEY='你的代理密钥' ./deepseek2api
```

### 测试

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Authorization: Bearer 你的代理密钥' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "deepseek-reasoner",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

## Docker

```bash
docker build -t deepseek2api .

docker run --rm -p 8080:8080 \
  -e PROXY_API_KEY='你的代理密钥' \
  -e DEEPSEEK_TOKEN='你的token' \
  deepseek2api
```

或使用 Compose：

```bash
PROXY_API_KEY='你的代理密钥' DEEPSEEK_TOKEN='你的token' docker compose up --build
```

## 配置项

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | `8080` | 本地 HTTP 服务端口 |
| `PROXY_API_KEY` | 无，必填 | 客户端调用代理用的密钥 |
| `DEEPSEEK_TOKEN` | 空 | DeepSeek 网页版 `userToken` |
| `DEEPSEEK_ACCOUNTS_FILE` | `accounts.txt` | 多账号文件，每行一个 token |
| `DEEPSEEK_BASE_URL` | `https://chat.deepseek.com` | 上游地址 |
| `DEFAULT_MODEL` | `deepseek-chat` | 请求未指定模型时使用 |
| `CONVERSATION_TTL` | `30m` | 会话空闲多久后从上游删除 |
| `MAX_CONVERSATIONS` | `1024` | 内存中保留的最大会话数（LRU 淘汰） |
| `STARTUP_ACCOUNT_CHECK` | `true` | 启动时对全部账号做一次轻量鉴权探测，失效账号自动隔离 |
| `HEALTH_PROBE_INTERVAL` | `30m` | 周期探测所有存活账号的间隔（0=关闭） |
| `HEALTH_PROBE_DEAD_RETRY` | `10m` | 对被隔离账号的重试间隔（token 换新后自动复活） |
| `HEALTH_LOG_FILE` | 同 `LOG_FILE` 目录 | 检测日志文件：隔离/复活/摘要事件，token 只显示掩码；留空默认与主日志同目录 |
| `TOOL_JSON_FALLBACK` | `false` | 裸 JSON 工具调用嗅探（双判据：工具名在白名单 且 带 arguments 键），opt-in |
| `REASONING_FALLBACK` | `true` | 正文为空而思考块有内容时，把思考内容补进正文 |
| `CONV_FINGERPRINT_ROUTE` | `false` | 按任务指纹路由：新任务第一回合必建网页会话并轮询下一账号；进行中的任务按「系统+首条指令+首轮回复」指纹回到本任务会话/账号 |

## 多轮对话

- **自动模式（推荐）**：客户端照常发送完整 `messages`，代理对历史做指纹缓存——命中时复用同一会话只发最新一句，未命中时新建会话。主流客户端无需改动。
- **直通模式**：响应带非标准字段 `conversation_id`（DeepSeek 会话 ID）；后续请求带上它即可复用该会话（仅取最后一条 user 消息作为 prompt）。
- **任务级账号分发**（`CONV_FINGERPRINT_ROUTE`，默认关）：多任务并发时每个任务用自己的网页会话，新任务轮询到下一个账号，风控负载按任务数摊薄；开启后「无 assistant 历史」的请求不再走最近会话兜底（每次新建会话）。
- **账号轮询修复**：此前未绑定的新会话静默钉在第一个账号（轮询实际失效，仅在被隔离账号后才换号），现已在所有存活账号间真正轮询。

> 冷启动说明：服务重启后，若历史超过一轮，只会带上 system 提示词与最后一条用户消息。

## 工具调用（Function Calling）

上游网页版没有原生 function-calling 接口，代理在文本层模拟（与 webai2api 相同思路）：

- 请求体带 OpenAI 风格的 `tools` / `tool_choice`。代理每轮都把工具 schema 和严格的 DSML 调用格式协议注入 system 块，保证 agent 循环（思源笔记、Open WebUI、Cherry Studio、任意 OpenAI SDK）在多轮工具往返中持续可用。
- 模型回复若含工具调用（DSML `<｜｜DSML｜｜ calls>` 块或 `<|tool_calls|>` 哨兵，容忍全角/半角竖线、大小写、缺失闭合标签），解析后按 OpenAI 规范返回：`message.tool_calls` 填充、`content: null`、`finish_reason: "tool_calls"`。标记碎片会被从 content 中剥离，不外泄。
- 回传的 `role:"tool"` 结果与 assistant `tool_calls` 会重新渲染进 prompt（哨兵形式 / `[Tool Result id=...]` 行）；会话前缀缓存键纳入规范化的 `tool_calls` 结构，使 agent 每一轮都复用同一上游会话。
- `tool_choice`：`auto`（默认）、`none`（忽略工具）、`required`、或 `{"type":"function","function":{"name":"..."}}`。
- 模型输出的未知工具名会被丢弃（严格模式），不透传给 agent。
- 模型写错的参数名按 schema 自动校正（大小写不敏感唯一子串命中才改名，如 `path` → `filePath`）；带小毛病的 JSON（代码围栏、尾逗号、全角标点）解析前自动修复。`TOOL_JSON_FALLBACK=true` 可开启无标记裸 JSON 嗅探（双判据：工具名在声明清单 且 带 arguments 键）。
- agent 工具循环免除随机会话重置，读时间冷却上限压到 2s，保持循环响应。
- `response_format: {"type":"json_object"}` 注入「仅输出 JSON」指令。
- **空正文兜底**（`REASONING_FALLBACK`，默认开）：网页模型偶尔把最终答案只写进思考块、正文为空，此时把思考文本补进 `content`，客户端不会收到空回复。

## 账号健康检测（token 失效探测）

DeepSeek 网页 token 会静默失效。本代理主动检测，而不是等到请求中途才发现：

- **启动校验**：服务启动、开始监听前，对每个账号做一次无副作用的鉴权探测（PoW-challenge 拉取）。失效账号当场隔离。
- **周期探测**：每 `HEALTH_PROBE_INTERVAL`（默认 30m）重探全部存活账号；被隔离账号每 `HEALTH_PROBE_DEAD_RETRY`（默认 10m）重试一次，token 换新后无需重启即自动复活。
- **使用中被动隔离**：真实补全请求里一旦看到 401/403，立刻隔离该账号；绑定在它上面的会话会被迁移到存活账号并全量重放历史；若所有账号都失效，请求快速失败并返回清晰的 503。
- **检测日志**：每次隔离/复活以及一条滚动摘要都追加到 `HEALTH_LOG_FILE`。留空时默认与主日志同目录（`LOG_FILE=/app/logs/app.log` → `/app/logs/deepseek-health.log`），一个文件夹里 `tail -f` 两个文件即可。token 一律只打印掩码（`ab12cd…ef90`），文件可安全保留：

  ```
  2026-10-25T01:00:03+08:00 [probe] #1/5 ab12cd…ef90 EXPIRED (upstream http 401) → quarantined
  2026-10-25T01:00:03+08:00 [summary] total=5 live=4 quarantined=1 [#1 upstream http 401]
  ```

  直接 `tail -f` 该文件（与主日志同目录）即可知道要替换 `accounts.txt` 的哪一行。

## 认证方式

所有 `/v1/*` 请求需要：

```http
Authorization: Bearer <PROXY_API_KEY>
```

`DEEPSEEK_TOKEN` / `accounts.txt` 只在服务端使用，不会暴露给调用方。

## 注意事项

- **图片**：每张 ≤20MB，单次最多 8 张，支持 png/jpg/webp/gif/bmp；仅处理最后一条用户消息中的图片。
- **`usage` 是估算值**：总量来自上游会话累计 token 的轮间差，分项按字符数近似。
- 上游 `429` 原样映射为 `429`；令牌失效映射为 `401`。
- 请勿将 token 提交到公开仓库（`accounts.txt` 已在 `.gitignore` 中）。

## 项目结构

```
main.go                 HTTP 服务与路由
config/                 环境配置
handlers/               OpenAI 兼容处理器、SSE、图片上传
deepseek/               上游客户端、proof-of-work、流式处理
docs/api-analysis.md    上游 API 逆向分析笔记
assets/                 Logo 与横幅
```

## 许可证

基于 [MIT 许可证](LICENSE) 发布。

<div align="center"><sub>与 DeepSeek 无隶属关系。请合理使用并遵守上游条款。</sub></div>
