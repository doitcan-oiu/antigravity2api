# Go 与 demo 反代实现对照

> 此文记录修改前的审计快照，下面的 Go 行号及“尚未修复”描述属于当时版本。后续已实施代码修复，当前状态、验证与保留边界见 [反代修复与运行说明](proxy-hardening.md)。

分析日期：2026-09-07。范围：当前工作区 `server` 的 Go 服务与 `demo/src-tauri/src/proxy` 的 Rust 服务。用户确认两边使用相同账号、模型和代理池配置；主要症状是 Go 容易出现 429，而 demo 日志中看不到。

**结论：Go 实现了主要调用链，但尚未等价移植 demo 的调度、错误恢复和协议兼容层。存在可以本地复现的实现错误；429 问题应优先检查选号、退避、跨请求限流状态和实际出站参数。没有真实 429 响应体及对应请求记录，暂时不能确定线上每次 429 的具体类别。**

本次只分析并添加此报告，没有修改业务代码、运行配置或真实账号数据，也没有调用真实生成接口进行压测。验证用的是临时副本、合成账号和本地 HTTP mock。demo 是行为对照，不假设它的每个实现或源码注释都正确。

## 一、与 429 最直接相关的差异

### 1. Go 的“轮询”存在秒级排序热点——已复现

- [Go MarkUsed](/home/wo/Project/antigravity2api/server/internal/store/store.go:388) 用 `time.Now().Unix()` 写 `last_used`，只精确到秒。
- [Go PickAccount](/home/wo/Project/antigravity2api/server/internal/store/store.go:424) 按 `last_used ASC, created_at ASC` 选第一条，再单独更新使用时间。
- 当多个账号在同一秒内都被选过，它们的 `last_used` 相同。再次选择最早创建的账号后，写回的时间仍是同一秒，排序不会改变。SELECT 与 UPDATE 也不是一次原子选号操作。
- demo 在非粘性选择路径使用候选抽样比较，并先过滤限流账号：[token_manager.rs](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/token_manager.rs:1972)。其策略不等同于平均轮询，但没有这里的秒级排序问题。

**局部验证：**使用真实 Go `Store.PickAccount`，创建 10 个合成账号，在同一秒内连续选号 500 次，耗时约 54ms：最早账号被选 491 次，另外 9 个账号各 1 次。前 10 次走完账号池，此后反复选择第一个账号。

这证明负载可能集中，尤其在请求密集或并发时，会增加单号限流风险。这里的 98.2% 是合成选号分布，**不是实测线上 429 比例**。低频、请求之间跨秒时，不一定出现同样分布。

另一组合验证先将 10 个账号各选一次，使 `last_used` 同秒，再模拟前 5 个账号返回 429、后 5 个可成功，复用当前“最多 5 次 + 当次 exclude”逻辑，在同秒内运行 100 个逻辑请求：全部未能选到后面的可用账号。此结果同样来自无网络仿真，说明两个策略组合可使可用账号得不到机会。

### 2. Go 429 直接换号，demo 会解析等待时间并退避

[Go proxy.go](/home/wo/Project/antigravity2api/server/internal/httpapi/proxy.go:165) 的分支是：读取错误、记录最后状态、排除当前账号、立即下一次。没有读取 `Retry-After`，没有解析 `RetryInfo` / `quotaResetDelay`，也没有同号短等待。

demo 的 [common.rs](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/handlers/common.rs:157) 根据服务端时间选择原账号短等待、固定延迟或从 5 秒开始的线性退避；[OpenAI handler](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/handlers/openai.rs:2456) 读取响应头并执行策略。OpenAI/Gemini 路径中的短等待重试还有独立的预算管理；Claude 使用旧版重试循环，不能将各协议细节概括为完全相同。

例如上游要求短暂等待时，demo 有机会等完后恢复成功；Go 会立刻换号，池小或所有账号都处于短时限制时，会更快耗尽尝试。问题不能简化为“Go 重试次数少”，关键在是否等待、重试哪个账号、是否记住限制。

### 3. Go 下一条请求不记得刚才的限流，也不按目标模型配额选号

- [Go exclude](/home/wo/Project/antigravity2api/server/internal/httpapi/proxy.go:112) 仅存在于一条请求内部；请求结束即丢弃。
- [PickAccount](/home/wo/Project/antigravity2api/server/internal/store/store.go:398) 只接收过期过滤和排除列表，不接收目标模型。SQL 不过滤 `rate_limited_until` 或已耗尽的模型配额。
- [MarkRateLimited](/home/wo/Project/antigravity2api/server/internal/store/store.go:393) 虽已定义，但代理路径没有调用。后台存在配额展示，不等于这些配额已参与调度。
- demo 会将错误分类并保存限流状态：[rate_limit.rs](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/rate_limit.rs:495)。配额耗尽可按账号和模型记忆，RPM/TPM 等限制使用账号维度；相应功能受配置开关控制。

根目录 README 明确规定“429 换号、不冻结”，所以“不保存冷却”包含有意的产品简化，不能全算漏写。但是，它与 demo 的恢复策略确实不等价。若保持“不永久禁用账号”，仍可设计可恢复的短期避让；按模型限制与永久禁用应分开。

### 4. 监控页没有 429，不代表没有中间 429

demo [monitor.rs](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/middleware/monitor.rs:461) 等 handler 返回后才读取最终 HTTP 状态并创建监控记录。所以 `上游 429 → 等待/换号 → 成功 200` 在监控页表现为一条 200。

demo 的运行日志仍会记录中间错误：[openai.rs](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/handlers/openai.rs:2468)，debug payload 还包含 attempt、status 和错误体。需要区分用户看到的是监控页面还是进程运行日志。

Go 也只记录最终结果，其 429 分支并不逐次写请求日志。因此不能说“只有 demo 隐藏中间 429”；demo 的恢复策略更完整，是最终成功率差异的合理解释，实际幅度还需对照真实请求。

### 5. Go 会丢掉最后一次真实错误——HTTP mock 已复现

如果账号全部被当次排除，下一次 [pool.Next 失败](/home/wo/Project/antigravity2api/server/internal/httpapi/proxy.go:126) 会把之前保存的 429 和原始错误覆盖成 503 / `no available accounts`。

**本地 HTTP mock：**仅有一个合成账号，上游返回 `429` 和 `quota exhausted audit marker`，客户端实际得到 `503 {"error":"no available accounts"}`。这会让同一种限流问题有时显示为 429，有时显示为“无可用账号”，妨碍定位。

建议保留最终上游错误和每次尝试信息，至少包括请求 ID、原模型、实际模型、账号内部 ID、端点、attempt、HTTP 状态、错误分类、等待时间。错误体和代理信息应脱敏，不能记录访问令牌。

## 二、已基本移植的部分与仍需对齐的出站行为

| 部分 | 当前对照结论 |
| --- | --- |
| 上游端点顺序 | 两边都是 Sandbox → Daily → Prod。Go [client.go:22](/home/wo/Project/antigravity2api/server/internal/cloudcode/client.go:22)，demo [client.rs:64](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/upstream/client.rs:64)。不能归因为 Go 单独走生产端点。 |
| 端点回退 | 两边都针对 404、408、5xx 回退；HTTP 429 交给外层账号策略。 |
| 普通生成的内部流式化 | Go [Generate](/home/wo/Project/antigravity2api/server/internal/cloudcode/client.go:286) 使用 streamGenerateContent，非流客户端再聚合。demo 的普通生成也有内部流式转换，不能单凭此解释差异。 |
| 身份头与项目头 | Go 已加入客户端、机器、进程会话等头，并在生成请求省略 x-goog-user-project；demo 有相同机制。 |
| HTTP/TLS | Go 使用 req/uTLS Chrome120 配置；demo 使用 rquest Chrome123。两边实现不同，但没有抓包和上游错误证据，不能认定某个指纹就是 429 根因。见 [Go outbound](/home/wo/Project/antigravity2api/server/internal/outbound/outbound.go:132)、[demo client](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/upstream/client.rs:163)。 |
| 代理配置 | Go 一个 Manager 使用一套当前代理配置；demo 还支持账号绑定代理和按代理缓存客户端。相同代理池配置仍应核对实际分配路径。见 [Go Manager](/home/wo/Project/antigravity2api/server/internal/outbound/outbound.go:16)、[demo get_client](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/upstream/client.rs:235)。 |
| 模型与预算 | demo 在 mapper 前有 variant 解析，修改模型、thinking 和 max_tokens。Go 的模型别名及可用模型重写路径不同。客户端填相同名字不足以证明最终请求参数相同。见 [demo OpenAI handler](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/handlers/openai.rs:1851)、[Go 重写](/home/wo/Project/antigravity2api/server/internal/httpapi/proxy.go:139)。 |

模型参数的具体例子：按 OpenAI 兼容入口、首轮、客户端不指定 thinking/max_tokens、demo 默认 Auto 配置且没有账号动态覆盖，源码路径得到以下差异。这是静态推导，不是当前部署抓到的请求。

| 客户端模型名 | Go：模型 / thinkingBudget / maxOutputTokens | demo：模型 / thinkingBudget / maxOutputTokens |
| --- | --- | --- |
| claude-sonnet-4-6 | 同名 / 未启用 / 未设置 | 同名 / 1024 / 64000 |
| claude-opus-4-6 | claude-opus-4-6-thinking / 24576 / 32768 | 同一 thinking 模型 / 24576 / 57344 |
| gemini-3-flash | 同名 / 32768 / 40960 | gemini-3-flash-agent / 10000 / 65536 |
| gemini-3.1-pro | gemini-3.1-pro-preview / 49152 / 57344 | gemini-pro-agent / 10001 / 65535 |

依据：[Go 默认预算](/home/wo/Project/antigravity2api/server/internal/convert/models.go:240)、[Go 输出预算归一化](/home/wo/Project/antigravity2api/server/internal/convert/wrap.go:136)、[demo variant 参数](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/common/variant_mapping.rs:208)、[demo Opus 最终覆盖](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/openai/request.rs:893)。Go 的 maxOutputTokens 在这些例子中反而更小，不能据此断言“Go 要求更多输出额度，因此 429”。

此外，Go 在构建请求之后执行 `RewriteToAvailable`，仅替换 `outer.Model`，没有重新计算 `generationConfig`。因此即便账号转发规则最终使两边模型名相同，也可能继续携带不同模型对应的旧参数。demo OpenAI 在 [按账号解析最终模型](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/handlers/openai.rs:1985) 后才调用 mapper。

请求外层也不完全一致：Go 所有协议的非图像请求都附带 enabledCreditTypes，非 Gmail 邮箱会使用 jetski；demo 原生 Gemini 有类似规则，但 [OpenAI mapper](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/openai/request.rs:1332) 和 [Claude mapper](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/claude/request.rs:714) 使用 antigravity 且未添加该 creditTypes 字段。这些是需要对照的出站变量，源码无法证明它们会触发 429。

## 三、与 429 分开的协议缺口

这些问题足以解释“第一轮正常，工具调用后失败”“客户端无输出”“回复空白”“usage 不对”等现象，但不应直接当成 HTTP 429 的已证实原因。

其中 countTokens 错路由还可能间接增加生成负载：如果客户端执行“先计数、再生成”，Go 会把计数步骤也变成一次生成请求。是否涉及当前使用场景，需要看客户端请求路径。

| 问题 | Go 行为与影响 | demo 对照 |
| --- | --- | --- |
| Responses 响应协议未实现 | [/v1/responses](/home/wo/Project/antigravity2api/server/internal/httpapi/server.go:102) 走 openaiChat；HTTP mock 确认返回 `object:chat.completion` 和 choices，而非 Responses 对象。流式也没有完整 response 生命周期。 | [独立 Responses SSE](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/openai/streaming.rs:575)。 |
| 工具结果名称关联错误 | [OpenAI](/home/wo/Project/antigravity2api/server/internal/convert/openai.go:67)、[Claude](/home/wo/Project/antigravity2api/server/internal/convert/claude.go:164) 没有 name 时填 `tool`，未从调用 ID 恢复真实函数名。工具执行后的下一轮历史可能不匹配。 | [OpenAI 按 ID 恢复](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/openai/request.rs:691)、[Claude](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/claude/request.rs:1412)。 |
| Responses 工具 ID 不匹配 | [转换](/home/wo/Project/antigravity2api/server/internal/convert/openai.go:361) 对 function_call 用 id，对 function_call_output 用 call_id；例如 fc_1 与 call_1 无法配对。custom tools、previous_response_id 也未完整实现。 | [优先使用 call_id](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/handlers/openai.rs:3026)。 |
| Claude thinking 签名丢失 | [入站](/home/wo/Project/antigravity2api/server/internal/convert/claude.go:142) 和 [出站](/home/wo/Project/antigravity2api/server/internal/convert/claude.go:218) 都丢签名；无签名历史还可能被 [补上 Thinking...](/home/wo/Project/antigravity2api/server/internal/convert/wrap.go:259)。真实签名无法完整往返。 | [请求签名处理](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/claude/request.rs:1123)、[signature_delta](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/claude/streaming.rs:370)。 |
| Claude SSE 重复结束、工具终止原因错误 | [stream.go:274](/home/wo/Project/antigravity2api/server/internal/convert/stream.go:274) 只看当前帧是否含工具，[EOF](/home/wo/Project/antigravity2api/server/internal/convert/stream.go:303) 又结束一次。两个工具帧再独立 STOP 的本地输入得到两次 message_stop、两次 end_turn。 | [跨帧 used_tool 状态](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/claude/streaming.rs:488) 和结束去重。 |
| Claude usage 层级错误 | [stream.go:281](/home/wo/Project/antigravity2api/server/internal/convert/stream.go:281) 把 usage 写进 delta.usage，本地复现顶层 usage 不存在。 | [usage 与 delta 平级](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/claude/streaming.rs:524)。 |
| 跨帧生成重复工具 ID | [collectParts](/home/wo/Project/antigravity2api/server/internal/convert/openai.go:445) 每帧从 call_1 开始回填；两个无上游 ID 的工具分帧到达时，OpenAI/Claude 输出都是 call_1、call_1。 | [OpenAI 工具 ID 管理](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/openai/streaming.rs:185)、[Claude 唯一 ID](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/claude/streaming.rs:1201)。 |
| 工具 schema 和 namespace 适配缺失 | [schema 原样使用](/home/wo/Project/antigravity2api/server/internal/convert/openai.go:222)，缺 $ref/$defs 等适配；[namespace](/home/wo/Project/antigravity2api/server/internal/convert/openai.go:194) 只展开，丢前缀，同名工具可能撞名。 | [schema 清理](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/common/json_schema.rs:49)、[namespace 限定名称](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/openai/request.rs:100)。 |
| countTokens 接口行为错误 | Go [Gemini handler](/home/wo/Project/antigravity2api/server/internal/httpapi/proxy.go:73) 不分 countTokens 动作。mock 确认 :countTokens 调成 streamGenerateContent，返回 candidates；/v1/messages/count_tokens 未注册。 | [Gemini 独立 countTokens](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/handlers/gemini.rs:952)、[Claude 路由](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/server.rs:643)。 |
| Gemini 常用鉴权头缺失 | [apiAuth](/home/wo/Project/antigravity2api/server/internal/httpapi/server.go:134) 只取 Authorization/x-api-key；mock 使用正确 x-goog-api-key 仍为 401。 | [读取 x-goog-api-key](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/middleware/auth.rs:129)。 |
| 流内失败变成空成功 | [聚合](/home/wo/Project/antigravity2api/server/internal/convert/stream.go:506) 把 error 对象也算 chunk；mock 的 error-only SSE 被聚合成 parts:null、STOP 且 err=nil。HTTP 层又 [先返回 200](/home/wo/Project/antigravity2api/server/internal/httpapi/proxy.go:202)。 | [首帧错误/空/超时预检查](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/handlers/openai.rs:2182) 与错误事件处理更完整；不保证 demo 能识别所有流错误。 |
| 生成参数、搜索和多模态支持不齐 | [OpenAIRequest](/home/wo/Project/antigravity2api/server/internal/convert/openai.go:9) 不接收 response_format/stop/n/seed 等参数；web_search 未转换为 googleSearch；工具截图及 inlineData-only 输出可能丢失。局部验证图片输出变空字符串+stop。 | [生成参数](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/openai/request.rs:963)、[搜索](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/openai/request.rs:1261)、[图片流输出](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/mappers/openai/streaming.rs:177)。原生 Gemini 路径仍可保留部分多模态数据，不能概括为 Go 全不支持。 |

另一个已验证的独立缺陷：[Go adminAuth 和 apiAuth 共用 validSecret](/home/wo/Project/antigravity2api/server/internal/httpapi/server.go:148)，它接受 ADMIN_TOKEN 或 API_KEY 中任意一个。HTTP mock 中，API_KEY 能以 200 读取 `/api/accounts`。这与 README 宣称的两套密钥分离不一致；demo [管理鉴权](/home/wo/Project/antigravity2api/demo/src-tauri/src/proxy/middleware/auth.rs:145) 在独立管理密码配置后单独校验。应将两个校验器分开修复。

## 四、验证范围与修复顺序

现有 `go test ./...` 全部通过，但 `httpapi`、`pool`、`store` 没有测试文件。这只能证明已有测试通过，不能证明与 demo 的协议行为一致。

临时验证覆盖：真实选号函数的分布、HTTP 鉴权、countTokens 路由、Responses 对象、单账号 429 的最终状态，以及合成 SSE 的工具结束、ID、签名、usage、图片和错误聚合。未运行真实上游 A/B；本地数据库没有可用于归因的请求日志。

建议按以下顺序推进：

1. **修复选号并保留错误证据。** 使用并发安全的调度机制，避免只靠秒级 last_used 排序；新增 attempt 级脱敏记录，并保留最后一次上游错误。
2. **移植 429 分类和恢复策略。** 识别 Retry-After/RetryInfo、短期限流与配额耗尽；同号短重试、退避、按模型避让分别处理，并确保上下文取消和总超时生效。跨请求冷却策略需要与“不冻结账号”的产品要求明确区分。
3. **对齐最终出站模型和参数。** 使用同一输入记录两边最终 model、generationConfig、请求类型、账号和实际代理绑定；再做低负载 A/B。不要仅比较客户端显示名称。
4. **修复工具往返和 SSE 状态机。** 工具 ID/name、真实签名、结束原因、usage、schema、namespace，应有多轮、多帧回归样本。
5. **补齐或明确拒绝未支持的 API 能力。** 独立 Responses 协议、countTokens、结构化输出等不能只注册路由或静默丢字段。管理/API 密钥分离应作为独立小修立即处理。

不建议逐行复制 demo 的所有缓存、提示词注入、压缩和第三方功能。优先恢复用户实际依赖的调度与协议语义，再决定哪些产品功能需要保留。
