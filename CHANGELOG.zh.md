# 更新日志

本项目的所有重要变更将记录在此文件中。

## [Unreleased]

### 工具循环拟人节奏 / 死号即隔离（2026-11-07 事故修复批次）

- **⚠️ 工具循环节奏改为随回复长度增长（默认行为变更）**（`TOOL_LOOP_MIN_GAP`，`handlers/handler.go` + `config/config.go`）：此前 agent 工具循环在同账号上被封顶为固定 2s 间隔——秒级连发是风控眼中的机器特征。现在间隔下限为 `TOOL_LOOP_MIN_GAP`（默认 `8s`，`0` = 关闭工具循环限速），并叠加"阅读时间"（上一条回复每 2000 字 +1s，封顶 30s），再加 0–50%（≤4s）抖动避免恒定节奏。`lastRequestAt` 升级为记录时间戳 + 回复字数；人类节奏路径（5~30s 阅读冷却、`LONG_PAUSE_CHANCE` 长停顿）不变。
- **建会话失败立即隔离账号**（`handlers/handler.go`）：此前建会话撞 40003 / 封禁措辞只打日志、返回 `context.Canceled`，账号要等下一个 30 分钟周期探测才被隔离——期间客户端每次重试都在死账号上重建会话（11/07 事故：3 分钟内 11 连发，每次重发整段 24 条消息的会话）。现在 completion / coalesce 建会话 / 内联建会话三条路径统一走 `quarantineOnUpstreamFailure`：封禁措辞 → 按 `BAN_QUARANTINE_HOURS` 保持隔离；40003/401/403 → 立即隔离直到探测成功，会话马上迁到活账号。
- **池全死 / 日配额耗尽的 503 附带 `Retry-After: 60`**（`handlers/handler.go`）：无 429 冷却进行中时，`errNoLiveAccount` / `errDailyLimit` 的 503 现在带 60s 退避提示，客户端不再秒级重发同一份完整会话（原有基于 429 冷却的 `Retry-After` 行为不变）。
- **`TOOL_LOOP_MIN_GAP` env**（默认 `8s`；双 README + `.env.example` + `.env.risk-hardened.example`（10s）+ 启动 banner 已文档化）。

### 新会话播种 / 429 槽位 / 可观测细粒度 / 流式工具索引（参考 webai2api 批次）

- **⚠️ 新会话播种不再全量重放（默认行为变更）**（`SESSION_REPLAY_TURNS`，`handlers/handler.go` + `handlers/conversations.go` + `config/config.go`）：此前 agent 工具流开新会话（会话重置 / 配额轮换 / 死会话重建）时，会把客户端**全部**历史消息重放进提示词——这是本代理最明显的机器特征之一。现在只播种**最近 N 个完整轮组**（user 指令 + 其产生的 assistant/tool 交换），且只在 user 消息边界切，绝不断开工具链。新 env `SESSION_REPLAY_TURNS`：`2` = 默认（保留最近 2 轮，换会话后前情基本不丢）；`0` = 只发当前指令（痕迹最薄，适合短问答）；`all` = 全量重放（旧版行为，需回退时用它）。每个新会话播种会打日志 `chat: fresh session seeded with X of Y messages (SESSION_REPLAY_TURNS=N)`，启动 banner 一行显示当前策略。**普通聊天路径不受影响**（本来就只发最后一条 user 消息）。升级后若感觉长任务换会话"忘前情"，把值调大即可；`all` 可完整恢复旧行为。
- **429 退避不再占并发槽**（`handlers/limiter.go` + `handlers/handler.go`）：限流器拆为 `WaitQPM`（速率令牌）与 `WaitSlot`（在途许可，支持 ctx 取消——取消时不占槽、释放为 no-op）。`chatWithRetry` 每个 attempt 自取自释槽，429 指数退避的 sleep 期间**不占任何在途预算**：限流风暴不再把一个账号的整个并发池冻在空等里。QPM 令牌语义零变化（一次逻辑请求一个令牌，重试不重复扣）；退避时长与账号级 429 升级不变。
- **风控摘要新增结果分类 + 平均响应时长**（`handlers/stats.go` + `handlers/handler.go`）：每个请求归入唯一结果类——`429` / `auth`（坏 token）/ `ban`（封禁措辞）/ `upstream`（无活账号 / 日配额 / 通用 5xx）/ `empty`（静默空回复触发了自动续写）/ `cancelled`（客户端中途离开），`RISK_STATS_INTERVAL` 摘要多打一行 `risk: outcomes …` + `first_content`（成功请求平均耗时）；429/503 响应附带 `Retry-After` 头（取全池最长 429 冷却），让客户端能自动退避。
- **流式 tool_calls 索引跨批续号**（`handlers/sse.go`）：OpenAI 规范要求 tool_call delta 的 index 按整条消息全局编号，但 SSE 写器此前每个标记批都从 0 重编——一次回复含 2+ 个工具调用时，第二批的 index 与第一批撞车，客户端把所有调用堆进 0 号槽。写器现在跨整个流维护 `toolSeq`，后续批次从上一批末尾继续（回归测试锁定：2+1 调用批 → 0,1 然后 2）。

### 工具调用标记鲁棒性（参考 webai2api v0.1.51 审计批次）

- **降级形态标记**（`handlers/tools.go` + `handlers/tools_stream.go`）：上游网页端偶发把标记开头的 `<` 吃掉，直接下发 `｜｜DSML｜｜ calls>`、`</｜｜DSML｜｜ invoke name="...">` 这类残片；此前所有 DSML 正则均以 `<` 开头锚定，残片会漏进正文。tools 模式下现在：识别无 `<` 的开标签；calls 层残片就地剥除（invoke 层半截块保守不剥，维持既有契约）；闭合形态误写但带 `name=` 的开标签（`</...invoke name="x">`）纠正为开标签；断流时回收 SCAN 态扣住的普通正文（`recoverTextOnCut`，尾部标记碎片气味剪除，≤64 字，不碰 markdown 表格），客户端不再丢断流前最后一段正文。
- **参数值截断**：未闭合 `<parameter>` 的值在下一个标记标签处截断，残片不再污染参数值。
- **`string` 属性三态**：省略 `string` 属性时值先按 JSON 解析、失败回落字符串（`42` → 数字 `42`，`hello` 保持字符串）；显式 `string="true"` 保持字符串。
- 非 tools 模式契约不变：残片保持原文（回归测试锁定）。

### token 失效自动隔离（HTTP-200 envelope 形态）

- `accounts.txt` 中 userToken 失效后，上游网页 API 以 **HTTP 200 + envelope `code: 40003 "Authorization Failed (invalid token)"`** 拒绝（PoW 挑战步骤即被拦下）；此前所有检测点只看 HTTP 401/403，坏账号永远"存活"，绑定在它上面的会话（前缀/指纹路由）每次请求都撞死账号、客户端反复拿到空回复。
- 新增 `deepseek.IsAuthFailure`（`deepseek/client.go`）：HTTP 401/403 **或** code 40003 / "invalid token" 措辞，统一判定鉴权失败。四处接入（`handlers/handler.go` + `handlers/health.go`）：
  - **请求中被动隔离**：命中即隔离该账号，下个请求 `migrateOffDeadAccount` 把会话迁到活账号全量重放历史；
  - **启动校验 + 周期探测**：判 EXPIRED 隔离，探测成功即自动复活；
  - **self-heal 拒绝**：坏 token 不再在同一账号上白重建会话；
  - **客户端错误码**：鉴权失败返回 401（此前 502）。

### 正确性修复批次（7 项，零风控行为变化）

- **空 messages 不再打崩进程**（`handlers/handler.go`）：`"messages": []` 或字段缺省会触发会话查找处 `req.Messages[:len(req.Messages)-1]` 切片下溢，在 HTTP 协程里 panic **整个服务进程**（所有进行中任务断掉）。现解码后直接 400，不接触上游。
- **合并建会话广播给所有等待者**（`handlers/handler.go`）：`pendingEntry` 原是 buffer=1 结果 channel + 永不取消的 `context.Background()`——并发同前缀请求（并行 tool calls 的典型场景）合并进同一建会话流程后，**除第一个等待者外全部永久挂起**。改 mutex + 共享结果 + `close(done)` 广播，所有等待者拿到同一会话（或同一错误）。
- **日配额跨天后 `Count` 也重置**（`handlers/usage.go`）：跨天归零此前只在 `Use` 里做——所有账号昨日达标时，午夜后首个**新会话**请求走 `Count` 读到昨日残留计数 → 503，且 503 发生在会触发重置的 `Use` 之前，全池 503 直到有人重启。现提取 `rolloverLocked()` 供 Use/Count 共用。
- **sweep 删除失败不再丢查找键**（`handlers/conversations.go`）：`sweepOnce` 的 `DeleteSession` 失败时此前只把会话插回 `bySession`，`byPrefix`/`byFingerprint` 键丢失——该任务下个请求查不到旧会话 → 白建新上游会话 + 全量重放历史（风控敏感动作），旧会话成孤儿。现记录过期会话的全部键，失败重试路径三表重链，带"不覆盖并发已重绑到别的会话的键"守卫。
- **429 首次重试真正等待**（`handlers/handler.go`）：退避 `1<<uint(attempt-1)` 在 `attempt=0` 时 uint 下溢为 0——首次 429 立即重试（实际 0s/2s/4s，注释与文档是 2s/4s/8s）。现 `1<<uint(attempt)`。
- **`Turn`/`LastUsed` 数据竞争修复**（`handlers/conversations.go`）：两字段写侧持会话锁、读侧（日志 / 存储扫描 / 持久化）无锁。改 `atomic.Int32` / `atomic.Int64`（unix 秒）+ `Turn()` / `LastUsed()` getter。
- **limiter 信号量扩容不再死锁**（`handlers/limiter.go`）：扩大账号在途上限时旧实现在双锁内阻塞 drain 旧 channel；在途槽位的延迟 release 读的是**新** channel，旧 channel 永远回不满 → 有在途请求时该账号全死锁。现直接换新 channel 并按"每个在途请求留一个空槽"预填（在途释放回灌新 channel），不阻塞、在途计数精确。

### 设备指纹稳定化（每账号设备身份固定到 DATA_DIR）

- **修复"重启 = 换设备"**（`deepseek/fingerprint.go` + `handlers/fingerprint.go` + `main.go`）：此前每次代理启动都会重新随机所有账号的「UA / 版本 / 区域」，且设备 ID 每次都是全新 UUID、从不落盘——NAS/CI 每重启一次，每个账号都像"新设备重新入池"（设备池大特征；正常人只有一台固定设备，顶多月后浏览器自动升级）。现在每账号的「设备ID / 版本 / 区域 / UA / 时区偏移」首次启动从拟人池选定一次，固定到 `<DATA_DIR>/device-fingerprints.json`（按 token 前缀作 key，`accounts.txt` 换序不受影响；账号删掉再加回恢复旧指纹——删号 ≠ 换设备），重启沿用同一设备。损坏文件隔离（quarantine）不崩溃，与项目其它状态文件同契约。
- **随机池拟人化**：版本池向当前版倾斜（2.5.0 占 3/6 槽位——人的 web 客户端版本只向前不回头）；区域池 zh_CN 占 5/7，**去掉 `ja_JP`/`ko_KR`**（大陆账号挂日韩 locale 是卖号特征）；UA 池在 Windows Chrome/Edge 主体上加少量 Mac（Chrome/Safari）权重。
- **时区同为指纹一部分**：时区偏移一并固定——账号在中国而容器跑 UTC 时，请在**首次启动前**给容器设 `TZ=Asia/Shanghai`（固定值一经生成不随重启改变，compose 文件里已有注释示例）。
- **`DEVICE_ID_ROTATE_HOURS` 兼容说明**：轮换产生的新设备 ID 仅在本次运行内有效，重启回落固定值（启动 banner 已注明）；该功能默认关闭，绝大多数部署无感知。
- **启动日志可监控**：每次启动，`ApplyFingerprints` 为主日志（`LOG_FILE`）打一行/账号：`fingerprint: #i/n <mask> [new|pinned] ua=… ver=… locale=… tz=+08:00 device=…`——`[new]` 本次新生成并落盘，`[pinned]` 从文件恢复（自首次生成起未变）。grep `fingerprint:` 即可核对每账号正在使用的 UA/版本/区域/时区。

### 静默自动续写 + 风控可观测（参考 webai2api）

- **静默自动续写**（`AUTO_CONTINUATION` + `AUTO_CONTINUATION_MAX`，默认 `false` / `2`，`handlers/handler.go` + `config/config.go`）：上游偶尔返回"成功但静默"的空回复（正文、思考全空——长任务静默卡死的典型形态，如 56 轮 stall）。开启后在**同一网页会话**内自动发送 "Continue" 促动（挂接在空回复的 parent 消息上，`ModelType` 置空表示非首轮），最多连续 `AUTO_CONTINUATION_MAX` 次；续写增量流入同一条回复流，客户端看到无缝衔接的答案。仅在回复**完全为空**且会话**已有 parent 消息**时触发，agent 工具流与任何非空回复都不触发；促动失败只记日志并保留已有部分回复，不会让整个请求失败。续写轮**同样计入** `REQUESTS_PER_SESSION` 与日配额（不豁免）。
- **风控可观测**（`RISK_STATS_INTERVAL`，默认 `0` = 关，`handlers/stats.go` + `handlers/health.go`）：按该间隔（duration）向主日志输出多行风控摘要——全局 请求数 / 新建会话数 / 缓存命中数，以及每账号 成功完成数 / 429 连撞次数（`AccountPool.RateHits` 暴露升级计数）/ 剩余 429 冷却 / 剩余 BAN 保持 / 日配额用量 / 在途数。新建会话 vs 缓存命中的比例是首要风险信号（新建是昂贵且易被指纹识别的动作）。计时器在 `main` 中随关停序列一起取消。

### 日配额 + 拟人长停顿 + 负载均衡选号（参考 webai2api）

- **日配额**（`DAILY_REQUEST_LIMIT`，默认 `0` = 不限，`handlers/usage.go` + `config/config.go`）：每账号按本地自然日计数请求数，计数落盘到 `<DATA_DIR>/daily-usage.json`（重启不清零、跨天归零、损坏文件隔离不崩溃）。达到配额的账号在新建会话选号时被跳过；全部账号都达标时，新会话请求 503（`errDailyLimit`，午夜后自动恢复），已缓存的会话不受影响。
- **拟人长停顿**（`LONG_PAUSE_CHANCE`，默认 `0` = 关，`handlers/handler.go`）：每个非 agent 请求以该概率额外随机等待 20–60s（"去倒杯水"长尾），叠加在 5–30s 阅读间隔之上，让单账号请求节奏更难被识别为机器节奏。agent 工具循环豁免（保持响应性）。
- **负载均衡选号**（`handlers/handler.go` + `handlers/limiter.go`）：新增 `AccountLimiter.InFlight`（各账号在途完成数）。前缀需重绑账号（未绑定 / 账号失效 / 账号达日配额）时，选**在途请求最少**的存活账号，轮询序仅在打平时生效——新流量不再堆到最忙账号的队列里。

### 账号级 429 退避 + 全池冷却宽限（参考 webai2api）

- **429 账号级退避**（`handlers/health.go` + `handlers/handler.go`）：每撞一次 429，该账号进入**跨请求**冷却 `60s × 2^(连撞次数-1)`（60s→120s→240s…封顶 900s）；新会话通过 `NextLive` 自动漂移到健康账号，一次成功完成即清零。此前 429 退避只在单个请求内（`chatWithRetry`），请求失败后**下一个请求立刻又撞同一账号**，现已根治。
- **全池冷却宽限**：所有账号都在冷却时，剩余冷却 ≤45s 的最快恢复账号仍会放行（避免全池 503）；冷却超出的账号绝不提前放行。
- **封禁熔断**（`BAN_QUARANTINE_HOURS`，默认 `168` 小时 = 7 天）：上游错误含封禁措辞（"封禁/被封/封号/封停/账号已被限制/账号异常/banned/suspended"，**仅错误路径匹配**，回复正文不算）→ 该账号隔离并**保持**：保持期内不探测、不轮询、不使用（反复戳已封账号只会加重风控）；到期后自动恢复探测，探测成功即复活。`0` = 长期保持直到探测成功（旧行为）。
- 启动横幅新增一行：封禁保持时长 + 429 退避策略。

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

### 任务级账号分发（会话起点指纹路由）+ 轮询修复

- **任务级账号分发**（`CONV_FINGERPRINT_ROUTE`，默认关，`handlers/handler.go` + `handlers/conversations.go`）：开启后，任务进行中的请求按「系统 + 首条指令 + 首轮回复 + 模型」指纹回到本任务的会话/账号（滚动前缀缓存 miss 也不受影响）；新任务第一回合（消息里无 assistant）跳过最近会话兜底，必然新建网页会话并轮询到下一个账号——任务 a→账号 A、任务 b→账号 B，风控负载按任务数摊薄，任务内保持单会话零重放。
- **轮询 bug 修复**（`handlers/handler.go` `resolveBoundAccount`）：此前前缀→账号绑定用 Go 单值 map 读取，未绑定前缀静默落到账号 0，导致**所有新会话都钉在第一个账号**（轮询实际失效，只在账号被隔离后才换号）；修复后未绑定前缀真正在所有存活账号间轮询。

### 会话持久化（任务跨重启继续）+ 会话死亡自愈

- **会话持久化**（`PERSIST_SESSIONS` + `DATA_DIR`，默认关，`handlers/persist.go` + `handlers/handler.go` + `handlers/conversations.go`）：开启后把「前缀键 / 指纹键 / 会话ID → 会话记录（含所属账号）」索引快照到 `<DATA_DIR>/conversations.json`（默认日志同层 `data/` 目录，如 `/app/data`）。
  - 写入 5 秒节流 + 触发合并 + 原子替换（tmp + rename）；关机前强制 flush；文件损坏时改名隔离（`*.corrupt-<时间戳>`）而非崩溃。
  - 重启启动时自动恢复：任务 a 继续命中同一网页会话、同一账号 A（前缀→账号绑定一并恢复），无需从头重放上下文；`turn_count` 等 bookkeeping 字段也恢复。
  - 恢复时自动丢弃：已超空闲 TTL 的会话（其上游会话已被 sweep 删除）、账号已删除/被隔离的会话（下次请求自动轮询重绑）。
  - 账号用 token 掩码做稳定标识，`accounts.txt` 重排/增删行后仍能正确 remap（新增 `AccountPool.FindByMask`）。
- **会话死亡自愈**（跟随 `PERSIST_SESSIONS` 开关，`handlers/handler.go` `shouldSelfHeal`）：缓存/恢复的会话若以**非鉴权错误**（404/5xx/网络——即「上游会话大概率已死」）失败，在同账号重建会话 + 全量历史重放 + 本请求内重试一次（仅非流式请求；401/403 仍走隔离+跨账号迁移，429 仍走退避）。
- 补全执行路径重构为 `buildAndComplete` 闭包，使自愈可复用同一套 prompt/图片/限流/重试逻辑（`errRequestAborted` 哨兵排除请求本身的错误，不触发自愈）。

### 多客户端密钥（PROXY_KEYS_FILE）+ 跨客户端串台根治

- **多客户端代理密钥**（`PROXY_KEYS_FILE`，默认 `proxy-keys.txt`，`config/config.go` + `handlers/handler.go`）：代理现在可接受任意多个代理 key——`PROXY_API_KEY`（文件有 key 时可为空）+ 文件里每行一个。给每个客户端（思源 1 号 key、opencode 2 号 key…）不同 key，各自的 DeepSeek 网页会话彻底隔离，互不串台。
- **跨客户端串台根治**（"客户端 B 的指令命中客户端 A 会话"的根因与修复）：
  - 每个 key 的稳定 ID（sha256 前 8 字节，**与文件行序无关、可持久化**）并入**前缀键**与**指纹键**——不同 key 在内容层就不可能共享会话。
  - `CONV_FINGERPRINT_ROUTE` 开启时，**所有轮次**都跳过"最近会话兜底"（此前只跳过首轮）：未知指纹请求自建新会话，不再被吸进"别的客户端最近用的会话"。
  - `RegisterFingerprint` 仅在"本请求新建了会话 / 内容级命中（指纹·前缀·conversation_id）"时执行，**兜底命中绝不登记**——防止错误关联被锁死并落盘（此前正是这一步把 opencode 的指纹登记进了思源的会话）。
- **运维注意**：升级后旧的持久化前缀/指纹键全部失配（哈希加入了 keyID），所有任务**冷启动一次**（新会话+全量重放历史）；被污染过的 `conversations.json` 建议直接删除重建。




