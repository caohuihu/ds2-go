# 更新日志

本项目的所有重要变更将记录在此文件中。

## [未发布]

### 新增功能（行为随机化反风控）

- **UA / Version / Locale 随机池**（`deepseek/client.go`）
  - `NewClient` 启动时从池中随机选取：
    - **版本**：`2.5.0` / `2.4.5` / `2.4.0` / `2.3.5` / `2.3.0`
    - **区域**：`zh_CN` / `en_US` / `zh_TW` / `ja_JP` / `ko_KR`
    - **User-Agent**：7 种常见组合，覆盖 Windows / Mac / Linux + Chrome / Firefox / Edge
  - 同一账号每次重启服务后自动选用不同指纹，降低设备指纹相似度。

- **请求前随机抖动**（`deepseek/client.go` + `handlers/handler.go`）
  - 在发送 `/chat/completion` 请求前插入 `REQUEST_JITTER_MIN ~ REQUEST_JITTER_MAX` 的随机等待（默认 200ms ~ 800ms）。
  - 模拟人类思考间隔，避免固定节奏的机械请求模式。

- **POW 求解模拟延迟**（`deepseek/pow.go`）
  - `Solve(minDelay, maxDelay)` 在真实哈希运算前增加 `POW_JITTER_MIN ~ POW_JITTER_MAX` 随机等待（默认 30ms ~ 150ms）。
  - 浏览器解 POW 需约 30–150ms，Go 实现毫秒级完成；此参数让响应时间更贴近真实浏览器行为。

- **新建会话随机延迟**（`handlers/handler.go`）
  - `CreateSession` 调用前增加 `SESSION_CREATE_JITTER` 随机等待（默认 200ms）。
  - 防止批量创建会话时呈现机器批量操作特征。

- **运行时 DeviceID 定期轮换**（`deepseek/client.go` + `handlers/handler.go`）
  - 新增 `DEVICE_ID_ROTATE_HOURS` 环境变量（默认 `0`，关闭）。
  - 启用后，后台 goroutine 每 N 小时遍历所有账号的 Client，生成新 UUID 替换 DeviceID。
  - 有效降低单一设备指纹在长时间运行场景下的可识别度。
  - 启动日志会显示 `device ID rotation enabled: every Xh` 或 `device ID rotation: disabled`。

### 新增环境变量配置（续）

| 变量名 | 默认值 | 说明 |
|---|---|---|
| `ACCOUNT_QPM` | `30` | 每账号每秒滑动窗口内最大请求数 |
| `MAX_CONCURRENT_PER_ACCOUNT` | `5` | 每账号同时进行的最大并发请求数 |
| `RETRY_MAX_ATTEMPTS` | `3` | 遇到 429 最多重试次数 |
| `RETRY_BASE_DELAY` | `2s` | 指数退避基础延迟（实际等待：2s、4s、8s…） |
| `REQUEST_JITTER_MIN` | `200ms` | 每次请求前的最小随机等待（毫秒） |
| `REQUEST_JITTER_MAX` | `800ms` | 每次请求前的最大随机等待（毫秒） |
| `POW_JITTER_MIN` | `30ms` | POW 求解器最小模拟延迟（毫秒） |
| `POW_JITTER_MAX` | `150ms` | POW 求解器最大模拟延迟（毫秒） |
| `SESSION_CREATE_JITTER` | `200ms` | 新建会话前的随机等待（毫秒） |
| `DEVICE_ID_ROTATE_HOURS` | `0` | Device ID 定期轮换间隔（小时，0=不轮换） |

### 启动日志增强

启动时打印所有防风控参数，方便运维确认配置是否生效：

```
  account qpm              : 30
  max concurrent/account   : 5
  retry max attempts       : 3 (base 2s)
  request jitter           : 200ms ~ 800ms
  pow jitter               : 30ms ~ 150ms
  session create jitter    : 200ms
```

### 代码变更

- `deepseek/client.go`：新增 `clientVersions` / `clientLocales` / `userAgents` 随机池；`NewClient` 接受 `powJitterMin/Max` 参数。
- `deepseek/pow.go`：`Solve` 方法增加 `minDelay/maxDelay` 参数，求解前执行随机延迟。
- `handlers/handler.go`：`doCompletionWithRetry` 在 `CreateSession` 和 `Completion` 调用前插入随机抖动；`NewPool` 透传 jitter 参数。
- `config/config.go`：新增 6 个反风控字段及 `parseDurationEnv` 解析函数。
- `main.go`：启动日志增加新参数输出。

### 优化动机

上述改动与上一轮限流/重试功能配合，形成完整的反风控层：

1. **指纹多样性**：UA/Version/Locale 随机化避免所有账号共享同一设备指纹。
2. **节奏拟人**：请求抖动 + POW 延迟让请求间隔和计算耗时接近真实浏览器行为。
3. **会话分散**：新建会话随机延迟防止批量创建会话的模式识别。
4. **账号隔离**：限流器 + 并发信号量 + 429 重试轮换，确保单账号异常不影响其他账号。

### 工具调用支持（Function Calling）

上游网页版没有原生 function-calling 接口，本轮在文本层补齐（思路对齐 webai2api），使思源笔记等 OpenAI 客户端的 agent 工具循环可持续运行：

- **新增文件** `handlers/tools.go`、`handlers/tools_stream.go`：
  - `buildToolPrompt`：把工具 schema + 严格 DSML 调用协议注入 system 块，`tools` 内容做进程内缓存。
  - DSML / 哨兵解析器（`parseToolCalls`）：容忍全角/半角竖线、大小写、缺失 `</invoke>`、缺失 `</calls>`；不声明工具时仍会剥离模型泄漏的标记碎片（零污染原则）。
  - 流式检测器（`streamingToolDetector`）：SCAN/DSML/SENTINEL 状态机，逐 delta 累积、按 OpenAI 规范发 `tool_calls` 增量 chunk。
  - `canonicalToolCalls`：工具调用结构规范化，纳入会话前缀缓存键。
- **`handlers/handler.go`**：
  - `chatRequest` 增加 `tools` / `tool_choice` / `response_format`；`chatMessage` 增加 `tool_calls` / `tool_call_id`。
  - 工具流路径：缓存命中时只重发「最后一条 assistant 之后」的新消息（工具结果、新用户消息），system/工具协议每轮重注入；新建会话时重放完整历史。
  - 响应带 `tool_calls` + `content: null` + `finish_reason: "tool_calls"`；流式按规范发 id/name 与 arguments 两个 chunk。
  - agent 工具循环（消息含 `role:"tool"` 或 assistant `tool_calls`）：免除随机会话重置；读时间冷却上限压到 2s。
  - 未知工具名严格丢弃，不透传给 agent。
- **`handlers/conversations.go`**：`PrefixKey` 纳入规范化的 `tool_calls` 与 `tool_call_id`，agent 每轮命中同一上游会话。
- **`handlers/sse.go`**：`sseWriter` 新增 `toolCalls` 方法；`finish` 支持自定义 `finish_reason`。

### 效果

- 思源笔记 / Open WebUI / Cherry Studio / 任意 OpenAI SDK 的 agent 循环（写日记、调工具、回填结果）可完整跑通，不再把 DeepSeek 网页命令当纯文本显示。

### 账号 token 失效检测（独立检测日志）

DeepSeek 网页 token 会静默失效，此前只能在实际请求中途才发现。本轮加入主动检测 + 自愈 + 独立检测日志：

- **新增文件** `handlers/health.go`：
  - `AccountPool` 增加隔离状态机（`dead` map，`IsDead`/`Quarantine`/`Revive`/`NextLive`/`HasLive`）；`NextLive` 轮询时跳过被隔离账号。
  - `HealthChecker`：用无副作用的鉴权请求（PoW-challenge 拉取）探测账号存活；事件追加写入 `HEALTH_LOG_FILE`（留空默认与主日志同目录，如 `/app/logs/deepseek-health.log`），token 一律只打掩码（`ab12cd…ef90`）。
  - 启动校验（`STARTUP_ACCOUNT_CHECK`，默认开）：监听前探测全部账号，失效的当场隔离。
  - 周期探测：存活账号每 `HEALTH_PROBE_INTERVAL`（默认 30m），被隔离账号每 `HEALTH_PROBE_DEAD_RETRY`（默认 10m）重试，token 换新后免重启自动复活。
- **`handlers/handler.go`**：
  - 被动隔离：真实补全请求出现 401/403 时立即隔离该账号并写检测日志。
  - 会话迁移：绑定在被隔离账号上的会话会被 `Evict` 并重新解析到存活账号、全量重放历史（`migrateOffDeadAccount`）。
  - `coalesceOrCreate`：前缀绑定的账号若已失效，经 `NextLive` 重绑到存活账号；全部失效时返回 `errNoLiveAccount`。
  - `mapUpstreamError`：`errNoLiveAccount` 映射为 503。
- **`config/config.go`**：新增 4 个字段（`HealthProbeInterval` / `HealthProbeDeadRetry` / `HealthLogFile` / `StartupAccountCheck`）与 `parseBoolEnv`。
- **`main.go`**：接线 HealthChecker、启动探测、启动日志输出。
- **文档**：`.env.example` 增加账号健康检测配置块；README（中/英）增加「账号健康检测」章节与配置表行。

> 使用：`tail -f` 检测日志（默认与主日志同目录，如 `logs/deepseek-health.log`），看 `[summary]` 行与 `EXPIRED … quarantined` 行即可定位 `accounts.txt` 里哪一行 token 需要重新抓取替换。

### 工具解析加固 + 思考兜底（会话行为不变）

对齐 webai2api v0.1.20-v0.1.32 的加固项（`handlers/tools.go`、`handlers/tools_stream.go`、`handlers/handler.go`），**会话复用逻辑保持 v1.1.1 原样**：

- **参数名模糊校正**：模型把参数名写错（schema 要 `filePath` 它给 `path`）时，按大小写不敏感唯一子串命中自动改名；歧义或无命中保持原样不瞎猜。
- **宽松 JSON 修复**：模型输出的 JSON 带代码围栏 / 尾逗号 / 全角标点（，：）时，解析前自动修复（哨兵行与 DSML 值解码均生效）。
- **裸 JSON 兜底**（`TOOL_JSON_FALLBACK`，默认关）：无标记的调用 JSON 双判据嗅探（工具名在声明清单 且 带 arguments/args 键），防止把回答里的普通 JSON / 代码示例误判成工具调用；流式检测器同步加 JSON 状态。
- **空正文兜底**（`REASONING_FALLBACK`，默认开）：正文为空而思考块非空时（网页模型把答案写进 THINK 块），非流式直接置换、流式结尾补发 content 增量，客户端不再收到空回复；工具调用轮次不受影响（空正文是正常形态）。

> 注：曾尝试加「agent 请求禁用最近会话兜底（Strategy-3 门控）」防串台，但实测会破坏单用户会话连续性（导致不断新建 DeepSeek 网页会话），已回退，本版本不含该项。



