# AI Router 设计与实现审查报告

- 审查对象：`https://github.com/wbn-2020/ai-router`（本地仓库 `C:\AI\project\ai-router`）
- 审查基线：`86bc0dd feat: complete capture UI, Codex manifest/E2E and packaging`
- 审查时间：2026-10-31（本机）
- 审查范围：需求/计划一致性、Go 后端、React 管理端、数据面调度与流式、安全与隐私、存储/备份/迁移、测试/打包/文档
- 审查方式：逐文件阅读 + 调用链核对 + 全量测试/类型检查 + 针对可疑行为做最小复现；**未修改任何业务代码**

---

## 1. 结论摘要

这个项目已经具备一个可运行的个人 AI API 网关 MVP 的核心能力：单管理员/单客户端 Key、AES-GCM 加密上游 Key、原生 Responses/Chat 直通、Codex 0.164 模型 manifest、流式首内容门控与失败切换、请求日志/统计、按需 body 捕获、SQLite 迁移/备份/恢复、非 root Dockerfile 都已落地；真实 Codex 0.164 CLI 的 E2E 在本次审查中实际通过（46 秒）。

**没有发现会阻断正常 Codex 请求的 P0 级缺陷**，但发现了几类需要尽快处理的问题：

1. **P1：渠道删除与在途请求存在竞态，会导致请求日志/attempt 写入失败并被静默丢弃**（已用 SQLite 语义复现）。
2. **P1：请求日志保留清理代码已实现但从未启动**，`request_logs`/`request_attempts` 会无限增长。
3. **P1：三个对外配置项没有真正接线**：`GATEWAY_MAX_ATTEMPTS`、`GATEWAY_CAPTURE_MAX_BYTES` 完全不生效，`GATEWAY_CAPACITY_WAIT=0` 也不能实现文档所说的“不等待”。
4. **P1：`restore` 在替换数据库前先删除旧库的 `-wal`/`-shm`**，替换失败时可能丢失 WAL 中已提交的数据；且没有强制“服务已停止”的锁检查。
5. **P1（安全卫生）：`.gitignore` 没有忽略 `.env`/`gateway.env`/`bin/`/`*.db`**，而部署文档正好指导用户在仓库根目录创建 `gateway.env`，存在把主密钥提交到 Git 的现实风险。

此外还有若干 P2/P3：客户端看到的 `X-Request-ID` 与数据库中的 `request_id` 不是同一个值、鉴权/模型不存在等“代理前失败”完全不写请求日志、Codex manifest 会列出没有 Responses 渠道的模型、熔断打开时返回 404 `model_not_available` 而不是可重试的 503、流式上游返回 200 非 SSE body 时被丢弃并重试、非流式成功响应没有强制 `no-store`、E2E 自己复制了一套数据面装配而不是调用生产装配等。

**总体判断**：作为个人本机 MVP 可以继续使用，Codex 主路径质量较好；但在把它当作长期无人值守服务之前，建议先修完第 7 节的 P1，再处理 P2 中的观测/兼容性问题。

---

## 2. 审查范围与验证结果

### 2.1 实际运行并通过的检查

| 检查 | 命令 | 结果 |
| --- | --- | --- |
| Go 静态检查 | `go vet ./...` | 通过，无输出 |
| 全量 Go 测试（非缓存） | `go test -count=1 ./...` | 全部通过；e2e 47.902s，adminapi 29.206s，gateway 29.064s |
| 核心包重复运行 | `go test -count=3 ./internal/routing ./internal/gateway ./internal/adminauth` | 全部通过 |
| 真实 Codex E2E | `go test -count=1 ./e2e` | 通过，46.077s（codex-cli 0.164.0） |
| 前端类型检查 | `npm.cmd --prefix web run typecheck` | 通过 |
| 前端测试 | `npm.cmd --prefix web run test` | 8 个测试文件、62 个测试全部通过；jsdom 有 `window.scrollTo` 噪声输出 |

### 2.2 本次没有执行的检查

- `docker build` / 容器启动：环境未执行；文档也明确把它列为发布前人工验收。
- `go test -race`：当前环境无 C 工具链（`CGO_ENABLED=0`），未运行。
- `staticcheck` / `govulncheck` / `golangci-lint`：环境中未安装，未运行。
- 真实中转站上游：E2E 使用本地假上游；未验证真实 Responses 中转站的字段/错误行为。
- 浏览器手工验收：前端只做了 Vitest/jsdom 组件测试，没有 Playwright/真实浏览器回归。

因此，本报告的“已确认”指代码路径或本地复现可证实；“推测/待验证”会明确标注。

---

## 3. 设计层面的问题

### D1. 计划缺少“生产数据面装配”任务，最终靠计划外手工代码完成

**位置**：`docs/plans/api-gateway-plan.md` 的任务清单（T17–T29）、`cmd/gateway/dataplane.go:16-20`、`cmd/gateway/main.go:101-113`

计划从 T17 到 T24 分别实现 router、proxy、stream、usage、capture，但没有一个任务负责把这些组件装配进 `cmd/gateway`/`internal/server`。实际代码中 `cmd/gateway/dataplane.go` 是执行阶段额外补上的，注释也写着“这是 T17/T20/T21/T23/T24 的最终接线点”。

**影响**：

- 严格 task-run 的“文件范围”契约无法覆盖这一步，执行时只能越界修改并手动提交；
- 后续任何人按计划重跑，仍会在“所有组件都通过单元测试，但 `/v1` 没有挂载”处卡住；
- 装配逻辑没有进入计划的可验收范围。

**建议**：在计划中增加一个明确的集成任务（例如 T30），文件范围包含 `cmd/gateway/dataplane.go`、`cmd/gateway/dataplane_test.go`，行为验收为“通过 `server.New` + 生产装配后 `/v1/models` 与 `/v1/responses` 可达”，并把 `mountDataPlane` 作为唯一装配入口。

### D2. T24/T29 的文件范围与自己的步骤矛盾，导致执行时必然越界

**位置**：`docs/plans/api-gateway-plan.md:772`、`:776`（T24）；`:873`、`:876`（T29）

- T24 的文件范围只有 `internal/observability/capture.go`、`capture_test.go`、`internal/adminapi/capture.go`、`capture_test.go`，但步骤 2 要求在“请求结束时把 request/response 字节 tee 到诊断环”——这必须修改 `internal/gateway`，甚至 `internal/server`。
- T29 的文件范围只有 Dockerfile、`.dockerignore`、docs、两个 server 测试，但步骤 1 要求先执行 `npm run build` 生成 `internal/webassets/dist`；这会产生范围外文件，且 dist 必须提交后才能通过 `go:embed` 测试。

**影响**：task-run 执行时先后在 T24、T29 因范围检查失败，最后靠手工提交/重跑绕过；计划与真实工作不一致。

**建议**：修订计划文件范围，或把“生成 dist”拆成独立的前端构建任务；T24 显式允许 `internal/gateway/observability.go`、`internal/server/server.go`（或抽出 capture 中间件包）。

### D3. 管理端的秘密值来源绕过了 `config.Config`，形成第二套配置入口

**位置**：`internal/config/config.go:95-107`、`cmd/gateway/main.go:101-105`、`internal/server/server.go:211-246`、`internal/adminapi/channels.go:292-297`、`internal/adminapi/capture.go:265-287`

`config.Load` 已经解析了 `AdminPassword`、`CookieSecure`、`MasterKey`，但：

- `main.runServe` 构造 `server.Options` 时只传了 `ListenAddr`、`Store`、`MasterKeyLoaded`；
- `server.newAdminAPI` 在 `opts.AdminPassword` 为空时回退读 `GATEWAY_ADMIN_PASSWORD`，在 `opts.CookieSecure` 为 false 时回退读 `GATEWAY_COOKIE_SECURE`；
- `adminapi` 的渠道/捕获端点每次请求都从 `os.Getenv(GATEWAY_MASTER_KEY)` 重新解析主密钥。

**影响**：

- 配置不再是单一来源；如果未来支持配置文件、密钥管理服务、测试注入或 `LoadFrom` 的可编程配置，数据面会使用新配置，管理端仍使用进程环境变量，出现“数据面能跑、管理端 503”的分裂；
- E2E harness 已经展示了这个缝：它用 `config.LoadFrom` 注入配置、给 `server.Options` 传 `AdminPassword`，但没有设置真实进程环境变量，因此不能在同一 harness 里测试渠道创建/Key 揭示/捕获保存；
- 目前生产路径恰好都从环境变量启动，所以没有立即的功能故障，但这是明显的设计债。

**建议**：把 `masterKey`/`envelope`、`AdminPassword`、`CookieSecure` 显式放进 `server.Options`，由 `main` 从 `cfg` 传入；`adminapi.Options` 接收 `*secret.Envelope`，不再读环境变量。环境变量解析只保留在 `internal/config`。

### D4. 单 SQLite 连接 + 同步日志/统计带来队头阻塞

**位置**：`internal/store/db.go:85-88`、`internal/observability/record.go:151-223`、`internal/observability/query.go:210-243`、`internal/adminapi/observability.go:286-347`

`SetMaxOpenConns(1)` 是计划明确的选择，避免了写锁竞争，但也意味着**所有读和写共享同一个连接**：

- 仪表盘的 4 条统计查询（totals/hourly/by_model/by_channel，其中 P95 使用窗口函数）会独占连接；
- 每个请求的 `TouchLastUsed`、`WriteAttempt`、`WriteRequest` 都是同步写；
- `/readyz` 的 `Ping` + `SchemaVersion` 也要排队；
- 如果统计查询在几万/几十万行上变慢，数据面日志写入和 readiness 会被一起拖慢。

**影响**：个人低流量下可接受；但“长期可维护、稳定优先”的目标下，这是一处明确的扩展性瓶颈。尤其在失败切换时，`writeAttempt` 是同步的，数据库繁忙会直接增加客户端等待时间。

**建议**：短期给统计查询加缓存/时间窗限制，或把只读查询放到第二个只读连接（WAL 允许并发读）；中期把 attempt/request 日志改为有界异步队列（失败时降级丢弃而不是阻塞请求），并保留单写连接保证写序列化。

### D5. “模型可发现”与“模型可服务”不是同一个集合

**位置**：`internal/store/routes.go:217-242`（`ListModels`）、`internal/gateway/models.go:118-127`（manifest）、`internal/gateway/codex_models.go:141-159`

`ListModels` 只过滤 `model_routes.enabled = 1`，不检查渠道是否启用、渠道协议是否匹配；而 `ListByModelAndProtocol` 会检查。

结果：

- 一个只映射到 `openai_chat` 渠道的模型会出现在 Codex 的 `/backend-api/codex/models` manifest 中；
- 一个只映射到禁用渠道的模型也会出现在 `/v1/models`；
- Codex/客户端选中后请求 `/v1/responses`，得到 404 `model_not_available`。

这不是数据面路由错误（数据面确实正确返回 404），但模型发现给出了错误承诺，用户会认为是网关坏了。R-004 的字面验收只要求“启用映射”，R-015 又要求协议匹配，需求本身需要澄清。

**建议**：为 Codex manifest 增加协议过滤（`openai_responses`），普通 `/v1/models` 可按查询参数或默认“至少一个可服务渠道”过滤；至少不要列出渠道全部禁用的模型。

### D6. 熔断/健康状态只存在于内存，且“临时不可用”被映射成“模型不存在”

**位置**：`internal/routing/scheduler.go:205`、`:266-268`、`:327-343`（`CircuitSnapshot` 未被生产代码使用）、`internal/gateway/attempt.go:251-259`、`internal/gateway/stream.go:793-809`、`:836-847`

- 所有候选都因熔断被过滤时，`Candidates` 返回 `ErrNoCandidates`，最终映射为 **404 `model_not_available`**；
- 但熔断通常只持续 20 秒（5xx）或 10 秒（429），是临时状态；Codex 很可能把 404 当作“模型不存在”而不再重试；
- `CircuitSnapshot` 有 `LastError`，但没有任何管理 API/UI 暴露；渠道列表里的 `last_error` 只来自探针，不来自数据面失败；
- 传输错误、Key 解密失败、2xx 响应体读取失败、流式提交后失败都没有真正触发熔断（`RecordFailure(..., 0, ...)` 不匹配任何状态码；post-commit 路径根本没有调用 `RecordFailure`）。

**影响**：临时故障没有被隔离，故障渠道会被反复尝试；管理端看不到熔断原因；客户端可能把临时故障当成永久错误。

**建议**：

- 当“无候选”的原因是熔断/容量而不是映射不存在时，返回 503 + `Retry-After`，而不是 404；
- 在管理 API/渠道页暴露 `CircuitSnapshot`；
- 对传输错误、解密失败、post-commit 失败引入失败计数/打开熔断的明确策略；
- 至少对“配置错误/解密失败”做持久化健康标记，避免每次请求都重试坏渠道。

### D7. 观测链路存在断点：请求 ID 不统一、代理前失败不落日志、诊断环对所有请求生效

**位置**：`internal/server/server.go:371-373`、`internal/gateway/observability.go:228-256`、`internal/gateway/router.go:168-201`、`internal/gateway/proxy.go:540-548`

- 管理端/数据面响应头里的 `X-Request-ID` 由 `server` 中间件生成；
- 数据面日志和诊断环里的 `request_id` 由 `ObservedGateway` 另外生成；
- 两者没有任何传递关系（`server.RequestIDFromContext` 只在 server 自己的错误体里使用）；
- 鉴权失败、缺 model、JSON 非法、model_not_available 在 router 层直接返回，不经过 Proxy/StreamProxy，因此**完全没有 request_logs**；
- 但 `ObservedGateway` 在进入 router 前就创建了诊断环 session，这些“没有日志的请求”会占用 ring 的 50 个槽位。

**影响**：用户拿响应头 ID 去请求页查不到；模型映射/鉴权问题在管理端完全不可见；诊断环容易被 `/v1/models`、401 等非推理流量挤掉。

**建议**：

- `ObservedGateway` 从 `server` 上下文取同一个 request ID，或由 server 把 ID 注入 context 并让 data plane 使用；
- 在 router 层对“已识别 endpoint/model”的失败写一条 request_log（status=error，attempts 为空），或统一由 ObservedGateway 在返回后补写；
- 诊断环只为推理端点创建 session，或对 models/401 设更低优先级。

### D8. 备份/恢复的安全边界只写在文档里，没有运行时强制

**位置**：`internal/store/backup.go:91-170`、`docs/deploy.md:244-269`

`Restore` 的注释和文档都要求“进程应已停止”，但代码没有获取任何锁、没有检测同一数据库是否被其他进程打开，也没有检查当前进程是否正在 serve。恢复时还会在 `os.Rename` **之前**删除旧库的 `-wal`/`-shm`。

**影响**：

- 在 Linux 上，运行中的进程持有旧 inode，rename 成功后旧进程继续写旧库，形成“新旧两个数据库”；
- 如果 rename 因权限/文件占用失败，旧库的 WAL 已经被删除，可能丢失已提交事务；
- 用户误在服务运行时执行 restore 时，最坏情况是数据分叉/丢失，虽然有 pre-restore 备份可人工恢复。

**建议**：使用文件锁/`BEGIN EXCLUSIVE` 或显式的“服务未运行”检查；恢复顺序改为先把 WAL checkpoint 到主库，再原子替换，最后清理旧 WAL/SHM；失败时保证旧库仍可打开。

---

## 4. 代码缺陷（按严重程度）

> 严重度定义：
> - **P1**：在常见运维/使用场景下会造成数据丢失、无界增长、配置失效或明显安全风险。
> - **P2**：特定条件下行为错误、兼容性/可靠性风险，或明显的可观测性缺口。
> - **P3**：健壮性、性能、可维护性建议，不改变主路径正确性。

### C1 [P1] 删除渠道与在途请求竞态会导致 request_logs/attempts 写入失败

**位置**：

- `internal/store/migrations/0001_init.sql:70`：`request_logs.channel_id INTEGER REFERENCES channels(id) ON DELETE SET NULL`
- `internal/store/migrations/0001_init.sql:97`：`request_attempts.channel_id` 同样的 FK
- `internal/gateway/attempt.go:304-307`：attempt 记录总是写入 `candidate.Channel.ID`
- `internal/gateway/proxy.go:245`、`:285`、`:272`、`:464`：attempt/最终日志同步写
- `internal/gateway/stream.go:637`、`:1046`：流式路径同样
- `internal/store/channels.go:308-321`：删除渠道

**证据/复现**：

```text
SQLite（foreign_keys=ON）：
DELETE FROM channels WHERE id=1;
INSERT INTO request_logs(request_id, channel_id) VALUES ('r1', 1);
=> FOREIGN KEY constraint failed
```

本次用 Python `sqlite3` 在内存库中验证了同样的行为（项目代码本身未改变）。

**触发条件**：管理员在一个长请求/长流式请求进行中删除该请求正在使用的渠道。请求已经拿到 `candidate.Channel` 快照；结束时 `WriteAttempt`/`WriteRequest` 用旧 channel_id 插入。

**影响**：

- `WriteAttempt` 失败只记一条日志（`proxy.go:471-479` / `stream.go:1011-1019`），请求继续；
- 最终 `WriteRequest` 失败只记日志（`proxy.go:462-467` / `stream.go:1044-1048`），**请求日志永久丢失**；
- 数据库留下没有 request_log 的孤儿 attempts（清理任务也按 request_logs 查，永远不会清）；
- 仪表盘/请求页少一次请求，统计失真；客户端完全无感。

**置信度**：已确认（代码路径 + SQLite FK 语义）。

**建议**：`WriteAttempt`/`WriteRequest` 捕获 FK 错误后以 `channel_id = NULL` 重试（保留 channel_name 快照）；或从日志表移除 `channel_id` FK，改为软删除渠道；或删除渠道时先等待在途请求。建议加一个“删除渠道时正在流式请求”的集成测试。

### C2 [P1] 请求日志保留清理从未运行，数据库会无限增长

**位置**：

- `internal/observability/record.go:34-41`：`DefaultRetentionDays = 90`、`DefaultRetentionInterval = 24h`
- `internal/observability/record.go:231-314`：`PruneRequestLogs` / `RunRetention` 完整实现
- 生产调用点：**不存在**。`grep` 只在 `record.go` 和测试中出现；`adminapi/capture.go:48-61` 只启动了捕获清理，没有启动日志清理。

**影响**：

- `request_logs`、`request_attempts` 永不删除；个人使用一年也可能积累数万到数十万行；
- 备份文件、统计查询、迁移前备份都会随之变大；
- `RunRetention` 的 90 天常量成为死代码，文档/实现不一致；
- `admin_sessions` 也没有清理（见 C16）。

**置信度**：已确认（`grep` 无生产调用点）。

**建议**：在 `mountDataPlane` 或 `main.runServe` 中用和 `Server` 生命周期绑定的 context 启动 `journal.RunRetention`；增加 `GATEWAY_LOG_RETENTION_DAYS`/`GATEWAY_LOG_RETENTION_INTERVAL`（或明确固定 90 天）；在 README/deploy 中说明。顺手加 `admin_sessions` 过期清理和孤儿 attempts 清理。

### C3 [P1] 三个对外配置项没有接线，文档与实际行为不一致

**位置**：

| 配置 | 解析 | 实际使用 | 结论 |
| --- | --- | --- | --- |
| `GATEWAY_MAX_ATTEMPTS` | `internal/config/config.go:206`（允许 1–3） | `internal/gateway/attempt.go:21` 常量 `maxUpstreamAttempts = 3`；`Proxy`/`StreamProxy` 没有任何字段接收 | **完全不生效**，设 1 仍会尝试 3 个渠道 |
| `GATEWAY_CAPTURE_MAX_BYTES` | `config.go:212` | `internal/observability/capture.go:30-33` 固定 8 MiB/32 MiB；`:634-635` 固定 `DefaultCaptureBodyLimit` | **完全不生效** |
| `GATEWAY_CAPACITY_WAIT=0` | `config.go:209` 允许 0 | `internal/routing/scheduler.go:137-140`：`<= 0` 一律回退 `DefaultCapacityWait`（2s） | **无法实现文档所说“0 表示不等待”** |

`docs/deploy.md:82-84` 仍然把这三项写成有效配置。R-013（`docs/requirements/api-gateway.md:88`）明确要求“上游超时/重试上限可配置且有默认值”，因此 `GATEWAY_MAX_ATTEMPTS` 同时是需求缺口。

另外 `config.Config.AdminPassword` / `CookieSecure` 两个字段也没有被 `main` 传给 server（见 D3），虽然 server 通过环境变量回退能工作，但字段本身是死配置。

**置信度**：已确认（逐字段 grep 调用点）。

**建议**：二选一——要么把值接进 `ProxyOptions`/`StreamProxyOptions`/`CaptureRingOptions`/`Scheduler`，要么删除配置项和文档行。`CapacityWait=0` 需要区分“未设置”和“显式 0”（例如用 `*time.Duration` 或单独的 `DisableCapacityWait`）。

### C4 [P2] 大请求体被多次读取/重编码，非流式上游响应无大小上限

**位置**：

- `internal/gateway/router.go:176-183` + `:223-242`：第一次 `io.ReadAll` 读取完整 body
- `internal/gateway/proxy.go:176-198` + `:514-527`：第二次读取（非流式）；流式请求还会在 `internal/gateway/stream.go:565-574` 第三次读取
- `internal/upstream/client.go:288` + `:467-488`：`rewriteModel` 把 body 解码成 `map[string]json.RawMessage` 再重新 Marshal，产生额外副本
- `internal/gateway/proxy.go:375-401`：非流式 2xx 响应 `io.ReadAll(response.Body)` 无上限

**影响（按代码路径估算，不是实测峰值）**：

- 一个 32 MiB 的请求（例如大图片 base64）在路由、代理、流式代理、上游重写、捕获 tee 之间可能同时存在 3–4 份，峰值约 100 MiB 以上；
- 并发几个大图片请求就可能触发 Go 堆压力/OOM；
- 上游返回异常大的 2xx body（错误页、压缩炸弹已由 Transport 处理但解压后仍可能很大）会被完整读入内存；`usageBufferBody` 的 32 MiB 上限只限制 usage 解析副本，不限制 proxy 自己保留的 body。

**置信度**：路径已确认；峰值是按数据流估算，未做 pprof 实测。

**建议**：

- 路由层只解析 `model` 和 `stream`，把原始 body 通过 context/结构体传给代理，避免重复 `ReadAll`；
- 非流式响应加显式上限（例如 64 MiB，超出返回 502 `upstream_response_too_large`）；
- 有条件时避免 `rewriteModel` 的全量重编码（例如只在原始 JSON 中替换顶层 model 字符串，或接受 `json.RawMessage` 映射后直接流式写出）；
- 增加一个 32 MiB body + 64 并发的内存/基准测试。

### C5 [P2] 客户端看到的 X-Request-ID 与数据库/诊断环中的 request_id 不一致

**位置**：

- `internal/server/server.go:371-373`：server 中间件生成并写入响应头
- `internal/gateway/observability.go:228-234`：`ObservedGateway` 另外生成一个 request ID
- `internal/gateway/proxy.go:540-548` / `stream.go:1000-1008`：代理使用 `ObservedGateway` 注入的 ID
- `internal/gateway/proxy.go:498-501` + `internal/upstream/client.go:353`：上游的 `X-Request-Id` 还会被 `Add` 到同一个响应头

**影响**：

- 客户端/Codex 日志里看到的是 server 的 ID；管理端请求页/诊断环用的是另一个 ID，无法关联；
- 如果上游返回 `X-Request-Id`，响应头会有两个值，排障更混乱。

**置信度**：已确认（没有任何代码把 server 的 ID 传给 data plane）。

**建议**：`ObservedGateway` 优先使用 `server.RequestIDFromContext`（需要避免 gateway 反向依赖 server，可把 request ID 的 context key 抽到独立小包，或由 server 在 `Handle` 前注入）；数据面成功响应统一覆盖 `X-Request-ID`，不要 `Add` 上游同名头。

### C6 [P2] 代理前失败（401/400/404/503）完全不写请求日志

**位置**：`internal/gateway/router.go:168-201`

`serveData` 在以下路径直接返回，不经过 `Proxy`/`StreamProxy`：

- 客户端 Key 无效 → 401；
- 方法不对 → 405；
- body 非法/缺 model → 400；
- 没有启用映射/协议不匹配 → 404 `model_not_available`；
- route catalog/forwarder 不可用 → 503。

`ObserveGateway` 虽然为这些请求创建了诊断环（`observability.go:244-252`），但没有 request_logs 行。

**影响**：

- 管理端请求页看不到“模型映射写错/Key 错/客户端发了错误 endpoint”的请求；
- 统计错误率不包含这些失败；
- 诊断环里的 body 没有入口可查（请求页没有对应 ID）。

**置信度**：已确认。

**建议**：在 router 层对已识别 endpoint 的失败写一条最小 request_log（request_id、protocol、endpoint、model、status=error、http_status、error_code；attempt_count=0），或在 `ObservedGateway` 的统一返回路径补写。注意 401 不应记录 body/prompt。

### C7 [P2] 流式代理把任何 2xx body 都当 SSE；非 SSE 的 200 错误会被丢弃并重试

**位置**：`internal/gateway/stream.go:752-877`

`runStreamAttempt` 在 `response.StatusCode` 为 2xx 后直接 `NewSSEReader`，没有检查 `Content-Type`。如果上游/中转站返回：

```http
HTTP/1.1 200 OK
Content-Type: application/json

{"error":{"message":"upstream overloaded"}}
```

SSE reader 读不到 `data:` 事件，EOF 时 `gate.completed` 为 false，代码把 `interruptedStreamFailure`（502，Retry=true）返回并切换渠道；最终客户端得到 502/最后一个渠道的通用错误，而不是上游的真实错误。若所有渠道都这样，客户端完全看不到原因。

**影响**：部分中转站在流式请求出错时返回 200 + JSON error，这会被误判为“上游断流”并重复消耗额度；错误信息丢失。

**置信度**：代码路径已确认；没有现成测试覆盖该场景。

**建议**：在 2xx 后检查 `Content-Type`；如果不是 `text/event-stream`，读取有限 body 并按上游错误分类（或直接把 JSON 错误以对应 HTTP 状态返回），提交前不要重试。

### C8 [P2] 熔断/失败隔离不完整：临时故障 404、post-commit 失败不计入、配置错误永远重试

**位置**：

- `internal/routing/scheduler.go:205`、`:266-268`、`:389-508`
- `internal/gateway/attempt.go:251-259`
- `internal/gateway/proxy.go:321-329`（Key 解密失败不 `RecordFailure`）
- `internal/gateway/stream.go:713-717`（同样）、`:793-809`、`:836-847`（post-commit 失败不 `RecordFailure`）

**影响**：

- 熔断打开时，所有候选被过滤 → `ErrNoCandidates` → 404 `model_not_available`；Codex 可能不会重试；
- 一个 Key 解密失败/协议配置错误的渠道每次请求都会被尝试，永远不被隔离；
- 一个持续在提交后断流的上游不会被熔断，只会让每个流式请求都失败。

**置信度**：已确认代码行为；404 对 Codex 的具体影响取决于客户端重试策略。

**建议**：区分“无映射”（404）和“临时无可用渠道”（503 + Retry-After）；对配置/解密/传输/流式失败增加失败计数和熔断；在管理端展示熔断状态。

### C9 [P2] `restore` 在 rename 前删除 WAL/SHM，失败时可能丢数据

**位置**：`internal/store/backup.go:159-169`

```go
for _, suffix := range []string{"-wal", "-shm"} {
    os.Remove(paths.DBPath + suffix)
}
os.Rename(tempPath, paths.DBPath)
```

如果 `os.Rename` 失败（Windows 文件占用、权限、磁盘错误），旧数据库仍在使用，但它的 WAL 已被删除；旧库中已提交但尚未 checkpoint 的事务可能丢失。当前代码没有锁/运行中检测，文档只写了“先停止服务”。

**置信度**：代码顺序已确认；“WAL 未 checkpoint 时丢数据”取决于 SQLite 状态，属于高风险路径。

**建议**：先 checkpoint/使用 SQLite backup API；在替换前获取独占锁或检查服务未运行；rename 成功后再清理旧 WAL/SHM（并确认新库不会误读旧 WAL）；失败时保留可打开的旧库。补充“运行中 restore”的测试。

### C10 [P2] 非流式 2xx 响应没有强制 no-store，会透传上游 Cache-Control

**位置**：`internal/gateway/proxy.go:494-512`、`internal/upstream/client.go:353`

`writeUpstreamResponse` 把上游白名单头原样 `Add` 给客户端，白名单包含 `Cache-Control`；它自己没有设置 `Cache-Control: no-store`。对比：

- 错误/模型列表 `writeJSON` 设置了 no-store；
- 流式提交 `stream.go:165-172` 设置了 no-store；
- 只有非流式成功响应缺失。

如果上游返回 `Cache-Control: public, max-age=3600`，浏览器/中间代理可能缓存包含用户 prompt/模型输出的响应。Codex 是 CLI 不受影响，但其他客户端/浏览器场景存在隐私风险。

**置信度**：代码已确认；实际是否被缓存取决于客户端和中间代理。

**建议**：数据面成功响应统一覆盖 `Cache-Control: no-store`/`Pragma: no-cache`，不要无条件透传上游缓存头；保留上游 `X-Request-Id`/`Retry-After` 等非缓存头。

### C11 [P2] 管理端秘密值读取/配置入口不统一（同 D3 的具体代码表现）

**位置**：`internal/server/server.go:211-246`、`internal/adminapi/channels.go:320-329`、`internal/adminapi/capture.go:265-287`、`cmd/gateway/main.go:101-105`

**影响**：见 D3；此外 `opts.CookieSecure=false` 无法显式覆盖环境变量 true，`opts.AdminPassword=""` 无法显式覆盖环境变量；对嵌入/测试不友好。

**建议**：同 D3。

### C12 [P2] E2E harness 复制了生产装配，生产装配回归测不到

**位置**：`e2e/harness_test.go:214-276` 与 `cmd/gateway/dataplane.go:21-89`

E2E 没有调用 `mountDataPlane`，而是自己重新构造 `upstream.NewClient`、`routing.New`、`NewStreamProxy`、`NewProxy`、`gateway.New`、`ObserveGateway`，再 `srv.Handle`。`cmd/gateway/dataplane_test.go` 只验证 `/v1/models` 与 `/backend-api/codex/models`，不验证真实推理请求。

**影响**：

- 如果生产装配漏传配置（例如本次发现的 `MaxAttempts`/`CaptureMaxBytes`）、顺序错误或漏挂路由，E2E 仍可能通过；
- 两套装配以后会漂移，维护者需要改两处。

**建议**：把装配抽到 `internal/app`（可被 `cmd/gateway` 和 `e2e` 同时调用），E2E 直接使用生产装配；再加一个 `cmd/gateway` 级别的真实 `/v1/responses` 集成测试。

### C13 [P2] `.gitignore` 未覆盖 `.env`/`gateway.env`/`bin`/本地数据库，存在秘密入库风险

**位置**：`.gitignore:1-4`、`docs/deploy.md:131-140`

当前 `.gitignore` 只有：

```text
/reference/
/.zcode/
/web/node_modules/
/data/
```

但部署文档指导用户：

```bash
cat > gateway.env <<'EOF'
GATEWAY_MASTER_KEY=...
GATEWAY_CLIENT_KEY=...
GATEWAY_ADMIN_PASSWORD=...
EOF
```

`gateway.env` 在仓库根目录，默认不会被忽略；`bin/`、根目录下的 `*.db`、`.env` 也不会。用户执行 `git add .` 时可能把主密钥提交。

**置信度**：已确认 `.gitignore` 内容；是否泄露取决于操作。

**建议**：加入 `.env`、`.env.*`、`gateway.env`、`bin/`、`*.db`、`*.db-wal`、`*.db-shm`、`web/coverage/`；`.dockerignore` 也加入 `gateway.env`。可在文档中把 `gateway.env` 示例改到仓库外（如 `~/.config/ai-router/gateway.env`）。

### C14 [P3] 诊断环对每个请求克隆 body，`status` 查询也会复制最多 16 MiB

**位置**：`internal/observability/capture.go:172-210`、`:214-235`、`:349-365`、`internal/gateway/observability.go:244-252`

- `Capture` 在持有 ring 互斥锁时 `cloneCaptureSnapshot`（最多 16 MiB）；
- `ObservedGateway` 丢弃 `Capture` 返回值（`defer session.Commit()`），但克隆已经发生；
- `CaptureRing.Get` 也为 status 查询克隆完整 body，而 status 只需要 truncated/bytes；
- 每个 `/v1/models`/401 请求也会创建并提交 session，50 个槽位可能被诊断价值低的流量占满。

**影响**：大 body 时捕获提交会阻塞其他捕获操作，增加 GC 压力；诊断页可能找不到真正需要的推理请求。

**建议**：增加 metadata-only 的 `SnapshotMeta`/`GetMeta`；`Commit` 不返回克隆（或返回轻量元数据）；只为推理端点创建 session；考虑按 endpoint 设置不同保留权重。

### C15 [P3] `admin_sessions` 和孤儿 attempts 没有清理策略

**位置**：`internal/observability/record.go:231-314`、`internal/adminauth/session.go:354-377`

- `admin_sessions` 只增不删；`ListSessions` 在 Go 层过滤过期，但表会长期保留所有过期 session；
- `request_attempts` 的清理依赖 `request_logs` 中的 request_id；C1 的失败路径会留下永久孤儿 attempts；
- 没有 `VACUUM` 策略，删除日志后 SQLite 文件不会自动缩小。

**影响**：长期运行后小表也会积累；备份体积偏大。个人场景下不紧急。

**建议**：在 retention 任务中顺带删除过期 session 和无对应 request_log 的 attempts；文档说明 SQLite 空间回收（`VACUUM` 需停机或定期维护）。

### C16 [P3] 备份/数据库文件权限没有主动收紧

**位置**：`internal/store/backup.go:56`、`:65`、`internal/store/db.go:72`

代码用 `0750` 创建目录，但 SQLite 创建数据库文件、`VACUUM INTO` 创建备份文件时使用进程 umask；没有 `chmod 0600`。在 Linux 多用户机器上，其他本地用户可能读取数据库/备份（上游 Key 是密文，但请求元数据、管理员 session 哈希、捕获密文仍属敏感数据）。

**置信度**：推测/需在 Linux 上验证实际 mode；Windows 不适用。

**建议**：创建后 `os.Chmod(dbPath, 0600)`、备份后 `os.Chmod(dest, 0600)`；文档写明目录/文件权限要求。

### C17 [P3] 反向代理后面登录限流退化为全局限流，可被用来锁死管理员

**位置**：`internal/adminapi/middleware.go:299-315`、`internal/adminauth/limiter.go:24-29`

`clientSource` 只使用 `RemoteAddr`，不信任 `X-Forwarded-For`（安全上合理），但反向代理部署时所有请求来源都是代理地址，5 次失败会在 15 分钟内锁住管理员登录。若管理端通过公网反向代理暴露，未认证攻击者可以持续触发这个锁。

**影响**：可用性 DoS；单管理员场景下影响明显。

**建议**：明确部署时限制 `/admin` 来源；或增加“可信代理”配置后按真实 IP 限流；或对单管理员使用更宽松的全局阈值 + 账号级退避，并记录审计。

### C18 [P3] `sameOrigin` 只比较 host:port，不比较 scheme

**位置**：`internal/adminapi/middleware.go:127-137`

代码有意忽略 scheme，以兼容 TLS 反向代理；但这意味着 `http://host` 与 `https://host`（默认端口）会被视为同源。生产环境如果 `GATEWAY_COOKIE_SECURE=true`，cookie 不会在 HTTP 下发送，风险较低；如果用户忘了设置，HTTP 页面可能对 HTTPS 管理端发起写请求。

**建议**：在可信代理场景下结合 `X-Forwarded-Proto`/`r.TLS` 比较 scheme；或至少要求 `Sec-Fetch-Site: same-origin`；文档强调 `GATEWAY_COOKIE_SECURE=true`。

### C19 [P3] 前端存在两套 API 客户端/ApiError，登出路径也不一致

**位置**：`web/src/app/App.tsx:29-77`（本地 `requestJSON`/`ApiError`）与 `web/src/lib/api.ts:11-142`（全局 `apiRequest`/`ApiError`）；`App.tsx:640-650` 与 `web/src/features/settings/SettingsPage.tsx:267-282`

**影响**：

- 两套 `ApiError` 的 `instanceof` 不互通，未来混用会漏掉错误分支；
- `App.tsx` 的 `requestJSON` 没有 `cache: 'no-store'`，依赖服务端头；
- AdminLayout 用客户端 `navigate` 登出，Settings 用整页 `window.location.assign`；
- 登出请求失败时 `AuthController.logout` 在 `finally` 中把本地状态置为 anonymous，随后仍然跳登录页，但服务端 session 可能仍有效，UI 会短暂“假装已登出”。

**建议**：统一到 `lib/api.ts`；`AuthController` 只在服务端登出成功后置 anonymous，失败时保留状态并提示重试；统一登出导航方式。

### C20 [P3] 模型名没有长度限制

**位置**：`internal/gateway/router.go:245-264`

`decodeModel` 只 Trim，不限制长度；模型名会进入 `request_logs.model`、SQL 查询参数和诊断环。调试 API 有 256 字符限制（`internal/adminapi/debug.go:37-38`），数据面没有。

**影响**：已持有客户端 Key 的调用方可以写入超长 model 名，放大数据库/日志/UI；恶意上游模型名也可能很长。

**建议**：与调试 API 对齐 256 字符上限，超长返回 400 `invalid_request`。

### C21 [P3] 统计错误率把 cancelled 计入失败

**位置**：`internal/observability/query.go:164`、`:533-546`

`errorCount = requestCount - successCount`，因此客户端主动断开（`cancelled`）也被算作错误。仪表盘显示“失败”次数和错误率会偏高。

**建议**：如果产品语义要求区分，增加 `cancelled_count`，错误率只按 `status='error'` 计算；或在 UI 明确注明包含取消。

### C22 [P3] 计划文档本身存在状态不一致

**位置**：`docs/plans/api-gateway-plan.md:5` 与 `:957`

第 5 行仍写着“执行状态：未批准。请审阅方案后将合同中的 approved 改为 true”，但合同 JSON 中 `"approved": true`，且任务已全部执行。另见 D1/D2 的 T24/T29 范围问题。

**影响**：后来者读计划会以为尚未批准；与 git 历史/执行合同矛盾。

**建议**：修订计划头部状态；把 T24/T29 范围修正；把 `cmd/gateway/dataplane.go` 纳入任务清单。

---

## 5. 未完善与运维缺口

### G1. 缺少 README 和 Codex 接入说明

仓库根目录没有 README；`docs/` 只有需求、计划和部署文档。新用户需要自己推断：

- 如何 `keygen`、启动、创建渠道、创建模型映射；
- Codex `config.toml` 如何指向网关（`base_url` 是 `/v1` 还是根地址、`wire_api=responses`、`requires_openai_auth=false` 等）；
- 管理端 6 个页面的基本操作顺序；
- 日志保留/备份/恢复/升级的实际操作。

E2E 的 `e2e/testdata/codex-0.164.0/config.toml.tmpl` 是一份可用参考，但没有被文档引用。建议补一个 10 分钟 quick start，至少覆盖“本机启动 + 第一个 Responses 渠道 + Codex 配置 + 第一条请求”。

### G2. 没有 CI，且 `go test ./...` 强制依赖 codex 0.164

`e2e/codex_test.go:129-137` 明确没有 skip 分支：找不到 `codex` 或版本不是 0.164.x 就失败。这在计划里是刻意选择（防止 E2E 假通过），但意味着：

- 新机器/CI 不装 codex 就无法跑 `go test ./...`；
- GitHub 上没有 `.github/workflows`，本次审查也没有看到自动构建/测试；
- 需要在 CI 中固定安装 codex 0.164，或把 E2E 拆成显式 `go test ./e2e` 任务（同时保留“发布前必须跑”的检查）。

建议增加一个最小的 GitHub Actions：`go vet`、`go test`（含 e2e 的 codex 安装）、`npm typecheck/test`、`go build`；Docker build 可作为手动/定时任务。

### G3. 真实 Docker 构建和容器运行未验证

`Dockerfile` 结构合理（多阶段、`CGO_ENABLED=0`、非 root、`/data`、HEALTHCHECK），`internal/server/dockerfile_test.go` 做了静态契约检查；但：

- 没有实际 `docker build`；
- 没有验证 Alpine 的 `wget` healthcheck、`npm ci` 阶段、`COPY --from=web` 的 dist 路径；
- 没有验证命名卷首次挂载时 UID 10001 对 `/data` 的写权限；
- 没有验证 `docker stop -t 35` 与 SIGTERM 的行为。

`docs/deploy.md:303-319` 已明确列为人工验收；建议在发布前至少执行一次并记录结果。

### G4. 真实上游兼容性未验证

E2E 使用本地假上游，覆盖了 Codex 0.164 的请求形状、图片、tool calling、断流；但没有验证真实中转站的：

- Responses 事件字段/顺序差异；
- 200 + JSON error 的非 SSE 行为（见 C7）；
- 429/5xx 的 Retry-After；
- 大响应/大 tool output；
- 上游返回 `Content-Encoding`/压缩 SSE 的行为。

`Q-UPSTREAM-NATIVE` 仍然是非阻塞待确认项。建议准备一个真实上游的冒烟脚本（不放进默认 CI），至少验证文本、图片、tool calling 三条。

### G5. 没有 race/静态分析/漏洞扫描

- `go test -race` 未运行；
- `staticcheck`/`govulncheck`/`golangci-lint` 未安装；
- 依赖只有 `modernc.org/sqlite` 及间接依赖，但未做漏洞数据库扫描。

建议在 CI 或本地定期运行 `go vet`、`staticcheck`、`govulncheck`；对 `modernc.org/sqlite` 升级保持关注。

### G6. 前端依赖许可证未记录

`LICENSES.md` 只列了 Go 直接/间接依赖和四个参考项目；`internal/compliance` 的 `TestDirectDependenciesHaveLicenses` 只解析 `go.mod`，不检查 `web/package.json`。React、TanStack Router/Query、Vite、TypeScript、Vitest、Testing Library 等前端依赖的许可证没有记录。

项目当前按 Q-PROJECT-LICENSE 保持私有，问题不紧急；如果未来公开，需要补前端依赖许可证清单并人工复核。

### G7. 管理端没有真实浏览器回归

62 个 Vitest/jsdom 测试覆盖了主要页面和交互，但：

- 没有 Playwright/Cypress；
- 没有验证 CSP 在真实浏览器下不拦截 Vite 产物；
- 没有验证深链接刷新、SSE 流式渲染、剪贴板/下载在真实浏览器中的行为；
- `window.scrollTo` 在 jsdom 中未实现，测试输出有噪声。

建议发布前做一次手工/浏览器验收，尤其是 Codex 配置页、渠道 Key 揭示、捕获下载。

### G8. 没有明确的日志/捕获数据生命周期文档

- 请求日志保留期代码是 90 天，但没有启动清理，也没有文档；
- 捕获 TTL 24h、清理 10 分钟有实现，但文档没有说明；
- 备份包含加密 Key、元数据、session 哈希，文档只写“按敏感数据保管”；
- 没有说明如何安全删除旧备份/旧数据库。

建议在 `docs/deploy.md` 增加“数据生命周期”小节。

---

## 6. 需求覆盖简表

| 需求 | 状态 | 依据/备注 |
| --- | --- | --- |
| R-001 单管理员/单 Key/无多租户 | 通过 | `admin_credentials` 单行、单客户端 Key；无支付/注册表 |
| R-002 密码/session/CSRF | 通过 | PBKDF2 600k、token 只存 SHA-256、Origin 校验、HttpOnly/SameSite |
| R-003 渠道/Key 加密/揭示 | 通过 | AES-GCM v1，列表只返回 mask，揭示端点需 session + no-store |
| R-004 模型映射 | 部分 | CRUD/唯一约束通过；`/v1/models` 未按渠道启用/协议过滤（D5） |
| R-005 多渠道路由/失败切换 | 通过（核心） | 排序/3 次尝试/attempt 记录；熔断打开时 404 语义有问题（C8） |
| R-006 并发上限/满载 429 | 通过 | 信号量/2s 等待/`channel_capacity_exhausted`；`GATEWAY_CAPACITY_WAIT=0` 不生效（C3） |
| R-007 流式首内容门控/提交后终止 | 通过 | 真实 Codex E2E；非 SSE 200 场景未覆盖（C7） |
| R-008 Codex 0.164 | 通过 | 真实 CLI E2E 46s；manifest 完整字段 |
| R-009 ZCode 延后 | 按批准延后 | T28 保持 needs-info，未实现 adapter |
| R-010 文本/SSE/tool/图片，不扩端点 | 通过 | 路由白名单；E2E 覆盖图片/tool |
| R-011 元数据日志/统计 | 部分 | 统计/usage 通过；无保留清理、代理前失败不记录（C2/C6） |
| R-012 按需捕获 | 部分 | 环/加密/TTL/删除/no-store 通过；`GATEWAY_CAPTURE_MAX_BYTES` 不生效；环对所有请求生效（C3/C14） |
| R-013 配置/迁移/备份/退出/超时 | 部分 | 迁移/备份/超时/优雅退出通过；重试上限不可配、真实 Docker 未跑（C3/G3） |
| R-014 React 管理端 7 页 | 通过 | 7 页/62 个前端测试；未做真实浏览器回归 |
| R-015 原生协议直通 | 通过 | 数据面按协议过滤；模型发现未按协议过滤（D5） |
| R-016 Go+React+SQLite/单进程/Docker | 部分 | embed/非 root/Dockerfile 通过；Docker 未实跑；前端许可证未记录 |
| R-017 错误归一化/不泄露 | 通过 | 固定错误 envelope；上游 URL/Key 不出现在响应/日志（除少数未 redact 的 DB 错误日志，低风险） |

---

## 7. 建议修复顺序

### P1（建议先修，作为长期运行前提）

1. **C1**：渠道删除与在途请求的 FK 竞态——日志写入失败时以 NULL channel_id 重试/去 FK/软删除，并加测试。
2. **C2**：启动请求日志保留清理，和 Server 生命周期绑定；顺带清理过期 session/孤儿 attempts。
3. **C3**：接线 `GATEWAY_MAX_ATTEMPTS`、`GATEWAY_CAPTURE_MAX_BYTES`，修正 `GATEWAY_CAPACITY_WAIT=0`；删除文档中的无效项或改代码。
4. **C9**：restore 的 WAL/SHM 顺序与独占锁。
5. **C13**：`.gitignore` 加 `.env`/`gateway.env`/`bin`/`*.db`；部署文档把 env 文件放到仓库外。

### P2（下一轮）

6. **C5 + C6**：统一 request ID；为代理前失败补 request_log。
7. **C7**：流式 2xx 非 SSE 的 Content-Type 检测与错误保真。
8. **C8**：熔断打开返回 503 + Retry-After；暴露熔断状态；补失败计数。
9. **D5**：Codex manifest 按 `openai_responses` 过滤；`/v1/models` 至少过滤禁用渠道。
10. **C4**：单次读取 body、非流式响应上限、避免全量重编码。
11. **C10**：数据面成功响应强制 no-store。
12. **D3/C11**：把 master key/admin password/cookie secure 显式传入 server/adminapi。
13. **C12**：抽出生产装配，E2E 复用。

### P3（持续改进）

14. **C14**：诊断环 metadata-only 查询、避免克隆、限制抓取端点。
15. **C15/C16**：session/孤儿清理、文件权限。
16. **C17/C18**：代理后限流、scheme 校验。
17. **C19/C20/C21/C22**：前端 API 客户端统一、模型名长度、cancelled 统计、计划文档状态。
18. **G1/G2/G3/G4/G5/G6/G7**：README/CI/Docker 实跑/真实上游/静态分析/前端许可证/浏览器验收。

---

## 8. 已检查且认为没有问题的要点

以下项目经过代码阅读/测试，未发现需要报告的问题：

- **客户端鉴权**：`Authorization: Bearer`/`x-api-key` 双入口，SHA-256 + `subtle.ConstantTimeCompare`；无效 Key 统一 401。
- **管理端认证**：PBKDF2-HMAC-SHA256 600k、随机 salt、常量时间比较；session token 只存 SHA-256；cookie HttpOnly/SameSite=Lax/Secure 可配；登录限流在密码校验前占位，并发不可绕过。
- **密钥信封**：AES-256-GCM v1，12 字节随机 nonce，AAD 绑定版本+用途；错误不泄露明文/密文；渠道 Key 和捕获 body 分用途。
- **上游边界**：只替换顶层 `model`，保留 tools/input_image/未知字段；过滤客户端 Authorization/Cookie/x-api-key/hop-by-hop；响应头白名单；拒绝跨 host/协议重定向；超时分类稳定。
- **流式门控**：提交前缓冲/不写客户端；提交后只发一次 `response.failed` 或 `error + [DONE]`；客户端断开取消上游并记录 499；slot 在 `defer` 中释放。
- **调度**：route/channel priority + last_used + id 固定排序；`AcquireExcluding` 保证同一渠道不重试；容量等待/429；信号量支持缩容。
- **存储**：SQLite WAL、busy_timeout、foreign_keys、synchronous=NORMAL 均通过 DSN 设置；迁移 checksum、迁移前备份、单迁移事务；恢复前 integrity_check/核心表/版本校验 + pre-restore 备份。
- **管理端安全**：所有管理响应 no-store；写请求 Origin 校验；Key 揭示需 session；捕获保存要求 `confirm_risk`；捕获读取 no-store；前端无 `dangerouslySetInnerHTML`，body 以 `<pre>` 纯文本渲染。
- **CSP**：`script-src 'self'`，Vite 产物无内联脚本；`style-src 'unsafe-inline'` 兼容 React 内联样式；`frame-ancestors 'none'`。
- **打包**：`internal/webassets` 是唯一 embed 包；Dockerfile 多阶段、非 root、`/data` 卷、`CGO_ENABLED=0`；`.dockerignore` 排除了本机 dist/node_modules/reference/data。
- **测试**：Go 全量/重复运行通过；真实 Codex E2E 通过；前端 typecheck + 62 个测试通过。

---

## 9. 复核方法与限制

- 本次审查以 `86bc0dd` 为基线，工作区干净（`git status --short --branch` 输出 `## main...origin/main`）。
- 每个代码级结论都尽量给出 `file:line`；C1 额外用 SQLite 语义做了最小复现；C3/C2 用 `grep` 确认无生产调用点；C5/C6/C7 通过调用链确认。
- 涉及性能/内存/文件权限的条目（C4、C16）属于代码路径估算或平台相关，已在条目中标注置信度，未把它们写成“实测结论”。
- 本次没有修改、提交或推送任何文件；报告仅新增到 `docs/reviews/`。
- 后续如果修复上述问题，建议同步更新 `docs/plans/api-gateway-plan.md` 的任务范围/状态，避免计划与实现继续漂移。

---

## 10. 附：本次实际执行的关键命令

```bash
go version
go vet ./...
go test -count=1 ./...
go test -count=3 ./internal/routing ./internal/gateway ./internal/adminauth
go test -count=1 ./e2e

npm.cmd --prefix web run typecheck
npm.cmd --prefix web run test
```

结果摘要：

```text
go vet ./...                      -> 通过
go test -count=1 ./...            -> 全部包通过；e2e 47.902s
go test -count=3 (routing/gateway/adminauth) -> 全部通过
go test -count=1 ./e2e            -> ok 46.077s（codex-cli 0.164.0）
npm --prefix web run typecheck    -> 通过
npm --prefix web run test         -> 8 files / 62 tests passed
```

未执行：`docker build`、`go test -race`、`staticcheck`、`govulncheck`、真实上游冒烟、真实浏览器验收。

---

## 11. 修复记录（后续会话）

本节记录针对上述审查结论完成的代码修复；原报告正文保留为审查时点的结论。

### 已修复

| 条目 | 修复内容 |
| --- | --- |
| C1 | `WriteRequest`/`WriteAttempt` 遇到 `FOREIGN KEY constraint failed` 时保留 `channel_name` 快照，以 `channel_id=NULL` 重试；新增 SQLite 回归测试覆盖请求与 attempt。 |
| C2 | `serve` 启动后在独立 goroutine 中运行 `RunRetention`（启动即清理一次，之后每 24h），随 SIGINT/SIGTERM 退出。 |
| C3 | `MaxAttempts` 接入 `Proxy`/`StreamProxy`（1-3，默认 3）；`CaptureMaxBytes` 接入进程级诊断环与捕获落库；`GATEWAY_CAPACITY_WAIT=0` 通过 `DisableCapacityWait` 立即返回 429，不再回落到 2s。 |
| C4 | 路由阶段读取的 body 通过 context 复用，代理层不再二次全量读取/复制；非流式成功响应增加 32 MiB 上限，超限返回不可重试的 502。 |
| C5 | 新增 `internal/requestid`；server 中间件写入的 request ID 贯穿 `ObservedGateway`、代理、诊断环与 `request_logs`；非流式响应不再透传上游 `X-Request-ID`。 |
| C6 | `ObservedGateway` 在代理层未写日志且响应 >=400 时补写一条失败 `request_log`，使用 server request ID 与稳定错误码。 |
| C7 | 流式 2xx 响应检查 `Content-Type`；非 `text/event-stream` 时最多读取 64 KiB 后丢弃，按可重试的 502 上游错误切换渠道。 |
| C8 | 全部候选熔断返回 503 + `Retry-After: 1`，与“无模型/无映射”的 404 区分；`RecordFailure(status<=0)` 按 5xx 窗口熔断；解密/配置、流读取中断、提交后失败、缓冲超限均计入失败。 |
| C9 | `restore` 在复制/替换前对旧库执行 `PRAGMA wal_checkpoint(TRUNCATE)`；checkpoint 失败或 WAL 仍被占用时中止，旧库保持可打开。 |
| C10 | 非流式成功响应强制 `Cache-Control: no-store` + `Pragma: no-cache`，忽略上游缓存头。 |
| C11 | `server.Options` 显式接收 `MasterKey`/`AdminPassword`/`CookieSecure`/`CaptureMaxBytes`；管理 API 优先使用注入的信封，环境变量只作为直接构造时的兼容回退；`CookieSecure=false` 仍保留环境变量回退（生产入口传入 config 值）。 |
| C12 | 数据面装配抽到 `internal/dataplane`；`cmd/gateway` 与 E2E harness 共用同一套依赖图。 |
| C13 | `.gitignore` 覆盖 `.env`、`.env.*`、`gateway.env`、`bin/`、`*.db*`、`web/coverage/`；部署文档改为把 env 文件放在仓库外；`.dockerignore` 同步排除。 |
| C20 | 客户端 `model` 字段上限 256 字符，超限返回 400 `invalid_request`。 |

### 测试补充

- 非流式成功响应的 no-store / 上游 request ID 隔离。
- 非 SSE 200 的流式失败切换。
- 熔断打开返回 503 与 `Retry-After`。
- 路由层 401 使用 server request ID 写失败日志。
- 渠道删除后的 FK 回退（请求与 attempt）。
- `DisableCapacityWait` 立即失败、`status=0` 熔断窗口。
- `ListModelsByProtocol` 与 Codex manifest 的协议过滤。

### 本次验证

```text
gofmt -w cmd internal e2e   -> 通过
go vet ./...                -> 通过
go test -count=1 ./...      -> 全部包通过（e2e 使用 codex 0.164.0）
go test -count=3 ./internal/routing ./internal/gateway ./internal/adminauth ./internal/observability ./internal/store -> 通过
npm --prefix web run typecheck -> 通过
npm --prefix web run test      -> 8 files / 62 tests passed
```

`go test -race` 未能执行：本机没有 gcc，`CGO_ENABLED=1` 时 `runtime/cgo` 构建失败。

### 仍未处理

- C14（诊断环克隆/元数据查询性能）、C15（`admin_sessions` 与孤儿 attempts 清理）、C16（文件权限）、C17/C18（代理后限流、scheme 校验）、C19（前端 API 客户端统一）、C21（cancelled 统计）、C22（计划文档状态）。
- G1-G8（README/CI/Docker 实跑/真实上游/静态分析/许可证/浏览器验收/生命周期文档）。
- 本次修复未提交、未推送。
