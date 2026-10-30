# 实施方案：个人 AI API 网关 v2 — strict 实施计划（修订版）

> 模式：strict
> 独立评审：approve。增量评审通过：结构化 diff 仅列 T01 的 steps 与 behaviorAcceptance，files/modules/dependencies 均为 false，勘察闸门未触发；其余任务、需求、技术决策、Q-ZCODE 记录与 T28 的 needs-info 未见变化。新 T01 step 5 将禁用标识/路径扫描限定为 cmd/、internal/、web/src/、e2e/ 下的 *.go/*.ts/*.tsx/*.js，排除所有 *_test.go/*.test.ts/*.test.tsx/*.test.js 与 internal/compliance/licenses_test.go 自身，显式排除且不遍历 reference/、.zcode/、web/node_modules/、internal/webassets/dist/、LICENSES.md、docs/，并明确 LICENSES.md 中列出 sub2api/new-api/juhe-ai/claude-code-hub 不触发扫描；behaviorAcceptance 第 5 条逐项复述同一范围与排除项，保留“自动扫描不替代人工审计”，第 4 条把 step 5 已要求的 LICENSES.md/go.mod/go.sum 检查写入验收，未新增依赖或文件。只读核验：命令 `find reference -type f \( -name '*.go' -o -name '*.ts' -o -name '*.tsx' -o -name '*.js' \) | wc -l` 输出 12437；命令 `grep -o "sub2api\|new-api\|juhe-ai\|claude-code-hub" .zcode/workflow-drafts/task-plan.dwf.ts | sort | uniq -c` 显示四个名字各 1 次；根目录只有 .zcode、docs、reference（ls -la），无 go.mod/业务源码，故未运行 T01 的 `go test ./internal/config ./internal/compliance`；`go version`、`node --version`、`npm --version`、`codex --version` 分别输出 go1.26.5 windows/amd64、v24.18.0、11.16.0、codex-cli 0.164.0，与计划引用一致。Q-ZCODE 记录与旧合同一致（docs/plans/api-gateway-plan.md:920、:977-980）；本次 changes 的逐字授权句“其他你帮我做决定。你继续吧。”包含在既有 evidence 中，未编造新决定；Q-ZCODE blocking=false，T28 保持 needs-info，未发现新的 blocking question 关联 ready 任务。T01 执行时 web/src/、e2e/ 尚不存在，扫描器实现应将缺失根按空处理并用 filepath 归一化排除路径（Windows）；这是实现细节，不影响本次修订批准。未修改任何文件；docs/plans/api-gateway-plan.md:289/:294 仍是修订前 T01 文本，批准后由工作流重写。
> 执行状态：未批准。请审阅方案后将合同中的 approved 改为 true；先解决所有阻塞问题。

已按独立评审修订。推荐路线仍为 clean-room 重写：sub2api 是 LGPL + Vue + PostgreSQL/Redis，new-api 是 AGPLv3 + 多租户/支付，juhe-ai 是 Apache-2.0 但 Vue + PG/Redis + 两个常驻进程 + maintenance CLI，claude-code-hub 是 MIT 但 Next.js + PG/Redis。目标技术栈固定为 Go 1.26 + React 19/TypeScript + SQLite 单文件，单进程、单管理员、单客户端 Key。计划文件已内联完整任务合同、独立部署章节；Go embed 改为 internal/webassets/dist；Codex manifest 改为完整 ModelInfo；response.failed 补齐严格字段；增加 channels.last_used_at 与删除渠道 FK 策略；T22 用 npm run test:debug 同时执行前后端测试。Codex 按本机 0.164.0 的 Responses HTTP SSE 实现并做真实 CLI E2E；ZCode 经用户批准延后至 MVP+1，第一版只保留 adapter 边界并等待协议证据，T28 保持 needs-info 但不再阻塞第一版。

## 目标

在不复制参考仓库代码、不引入 Redis/多租户的前提下，交付一个可本机运行、可迁移 Linux/Docker 的个人 AI API 网关：单管理员 + 单客户端 Key，支持 Codex 的 Responses 文本/图片/工具/SSE、OpenAI Chat、手工模型映射、渠道优先级与并发、失败切换、观测统计、按需 body 捕获；ZCode 经用户批准延后至 MVP+1，第一版只保留 adapter 边界并等待协议证据。

## 规划约束与范围

- 用户约束：绿地项目，工作区无业务源码；只读调研 reference/ 与 docs/，不得修改业务源码；计划只写 docs/plans/api-gateway-plan.md；strict 计划，禁止 shell 与 python -c；Codex/ZCode 入站协议必须以参考仓库证据为准，不得猜测；参考项目需核对许可证，不复制不兼容代码；证据不足或产品决策未定列入 assumptions/blocking questions。
- 用户 scope：reference/sub2api,reference/new-api,reference/juhe-ai,reference/claude-code-hub

## 独立评审详情

- verdict：approve
- 摘要：增量评审通过：结构化 diff 仅列 T01 的 steps 与 behaviorAcceptance，files/modules/dependencies 均为 false，勘察闸门未触发；其余任务、需求、技术决策、Q-ZCODE 记录与 T28 的 needs-info 未见变化。新 T01 step 5 将禁用标识/路径扫描限定为 cmd/、internal/、web/src/、e2e/ 下的 *.go/*.ts/*.tsx/*.js，排除所有 *_test.go/*.test.ts/*.test.tsx/*.test.js 与 internal/compliance/licenses_test.go 自身，显式排除且不遍历 reference/、.zcode/、web/node_modules/、internal/webassets/dist/、LICENSES.md、docs/，并明确 LICENSES.md 中列出 sub2api/new-api/juhe-ai/claude-code-hub 不触发扫描；behaviorAcceptance 第 5 条逐项复述同一范围与排除项，保留“自动扫描不替代人工审计”，第 4 条把 step 5 已要求的 LICENSES.md/go.mod/go.sum 检查写入验收，未新增依赖或文件。只读核验：命令 `find reference -type f \( -name '*.go' -o -name '*.ts' -o -name '*.tsx' -o -name '*.js' \) | wc -l` 输出 12437；命令 `grep -o "sub2api\|new-api\|juhe-ai\|claude-code-hub" .zcode/workflow-drafts/task-plan.dwf.ts | sort | uniq -c` 显示四个名字各 1 次；根目录只有 .zcode、docs、reference（ls -la），无 go.mod/业务源码，故未运行 T01 的 `go test ./internal/config ./internal/compliance`；`go version`、`node --version`、`npm --version`、`codex --version` 分别输出 go1.26.5 windows/amd64、v24.18.0、11.16.0、codex-cli 0.164.0，与计划引用一致。Q-ZCODE 记录与旧合同一致（docs/plans/api-gateway-plan.md:920、:977-980）；本次 changes 的逐字授权句“其他你帮我做决定。你继续吧。”包含在既有 evidence 中，未编造新决定；Q-ZCODE blocking=false，T28 保持 needs-info，未发现新的 blocking question 关联 ready 任务。T01 执行时 web/src/、e2e/ 尚不存在，扫描器实现应将缺失根按空处理并用 filepath 归一化排除路径（Windows）；这是实现细节，不影响本次修订批准。未修改任何文件；docs/plans/api-gateway-plan.md:289/:294 仍是修订前 T01 文本，批准后由工作流重写。

## 需求拆解

### R-001 网关只有一个管理员、一个统一客户端 API key；不提供注册、多租户、支付、充值和复杂权限。
- 验收：客户端只凭单一 key 的 Authorization: Bearer 或 x-api-key 通过 /v1 鉴权；错误 key 返回 401，且不影响管理端 session。
- 验收：数据库没有 users/roles/groups/billing 表；管理端只有 admin_credentials 单行与 admin_sessions。
- 验收：管理前端不出现注册、支付、充值、返利入口。

### R-002 管理端使用单管理员密码和可撤销 session；支持登录、登出、改密和 CSRF 防护。
- 验收：密码只以 PBKDF2-HMAC-SHA256 哈希存储；数据库与日志无明文。
- 验收：session cookie 为 HttpOnly/SameSite=Lax；登出、改密或显式撤销后旧 token 失效。
- 验收：跨源写请求被拒绝；同源管理写请求成功。

### R-003 渠道以 base URL、上游 key、协议、优先级、并发上限和启停状态管理；上游 key 加密存储，列表脱敏，点击显示明文且不要求二次密码。
- 验收：channels.key_ciphertext 只含 AES-256-GCM 密文；列表 API 不返回 key。
- 验收：揭示接口返回完整明文并带 no-store；错误或篡改密文读取失败。
- 验收：base URL 只允许 http/https；并发上限 1..64；name 唯一。

### R-004 模型到渠道的映射由管理员手工配置，包含模型名、渠道、上游模型、优先级、启停；不做自动发现。
- 验收：model_routes 可 CRUD；同一客户端模型+渠道唯一。
- 验收：禁用映射/渠道不进入候选；/v1/models 只列有启用映射的模型。
- 验收：映射不存在或只有不匹配协议渠道时返回 model_not_available，不自动创建、不调用上游。

### R-005 同一模型可对应多个渠道，按优先级、健康和容量调度；首 token 前失败切换到下一个渠道，重试有上限。
- 验收：按 route.priority、channel.priority、last_used、id 排序；禁用/熔断/满载渠道被过滤。
- 验收：上游 503/429/网络错误会尝试下一个渠道；400 不重试；最多 3 个渠道，同一渠道不重试。
- 验收：每次尝试写入 request_attempts；最终错误按入站协议返回。

### R-006 渠道有进程内并发上限；满载时优先切换其他渠道，全部满载时短暂排队后返回 429。
- 验收：并发计数不超过 max_concurrency，流式/非流式/取消路径都释放槽位。
- 验收：全部候选满载时最多等待 2s，然后返回 429 + code=channel_capacity_exhausted + Retry-After。
- 验收：不存在 Redis 依赖。

### R-007 流式响应在首 token 前可切换；已输出语义内容后不得拼接第二个上游，必须按协议终止并让客户端重试。
- 验收：首内容前上游断流/错误可换渠道；客户端未收到语义字节。
- 验收：首内容后错误只产生一次 response.failed（Responses）或 error+DONE（Chat），不发起第二个上游；response.failed 含 type、sequence_number、response.id/object/created_at/model/status/output/error。
- 验收：客户端断开取消上游并记录 499，不重试。

### R-008 Codex 0.164.0 作为第一版核心客户端，使用 Responses HTTP SSE，支持文本、图片输入、tool calling、流式输出和完整 ModelInfo 模型发现，并通过真实 CLI E2E。
- 验收：codex exec 配置 wire_api=responses、supports_websockets=false 后可完成文本、图片、工具调用。
- 验收：请求命中 /v1/responses；模型发现返回包含 supported_reasoning_levels/context_window/input_modalities 等完整字段的 Codex manifest；不出现 WebSocket 升级。
- 验收：真实 Codex E2E 在 codex-cli 0.164.x 下通过；版本不匹配时测试失败。

### R-009 ZCode 延后至 MVP+1；第一版只保留 adapter 边界并等待协议证据（用户已批准延后）。
- 验收：第一版不注册 ZCode 入站 endpoint、不实现 ZCode adapter，只在 gateway/upstream 预留协议边界。
- 验收：MVP+1 启动前记录 ZCode 版本、endpoint、method、headers、body、SSE 事件、图片与工具字段，并以取证结果驱动实现。
- 验收：无证据时 T28 保持 needs-info，不以 Chat/Responses/Anthropic 猜测替代；ZCode 不阻塞第一版验收。

### R-010 第一版只提供文本、SSE、tool calling、图片输入；不支持 embeddings、图片生成、音频、视频、rerank。
- 验收：Responses/Chat 请求的 tools、input_image、reasoning 等字段原样到达上游。
- 验收：未实现端点返回 404；不注册 embeddings/images/audio/video 路由。
- 验收：流式事件与终止事件按协议保留。

### R-011 请求日志默认只保存元数据、token、延迟和错误；统计包含请求数、输入/输出 token、延迟、错误率和渠道维度。
- 验收：request_logs 不含 header/body/prompt；默认无 request_captures 行。
- 验收：能从 Responses completed 与 Chat usage 提取 token；缺失时为空，不估算。
- 验收：统计 API/页面支持时间、模型、渠道筛选，展示请求数、token、平均/P95 延迟和错误率。

### R-012 完整请求/响应 body 仅在管理员按需保存；默认不落库，保存前提示风险。
- 验收：默认请求只写元数据；内存诊断环有 5 分钟/50 请求/32MB 上限。
- 验收：管理员确认后保存的 body 为 AES-GCM 密文，TTL 24h，可删除；不保存 header；读取响应带 no-store。
- 验收：超过 8MB 的单侧 body 标记 truncated，不截断流式转发。

### R-013 配置校验、数据库迁移、备份/恢复、优雅退出、健康检查、上游超时和重试上限必须可用。
- 验收：缺主密钥/客户端 key/非法监听地址启动失败；/readyz 在 DB 或迁移未就绪时 503。
- 验收：迁移有版本/checksum 与迁移前备份；迁移失败不改变原库；删除渠道不破坏历史日志。
- 验收：SIGTERM 后 readiness 503，30s 内完成或取消；上游超时/重试上限可配置且有默认值。

### R-014 管理端使用 React/TypeScript，包含登录 + 6 个受保护页（共 7 页），布局完整但不堆砌多租户/支付。
- 验收：未登录访问重定向登录；登录后侧边栏显示 6 个受保护页；登出清除 session。
- 验收：渠道页初始只显示脱敏 key，点击后才显示完整 key；复制可用。
- 验收：请求页默认只显示元数据；抓取按钮仅在内存诊断环可用时出现。
- 验收：前端测试覆盖登录、渠道、映射、总览、请求、调试、设置；无支付/注册入口。

### R-015 Provider Adapter 以原生协议为边界；渠道协议与入站协议不匹配时不调度，不做隐式跨协议转换。
- 验收：Responses 请求只选 openai_responses 渠道，Chat 只选 openai_chat 渠道。
- 验收：协议不匹配返回 model_not_available；不调用转换器，上游调用次数为 0。
- 验收：请求中的 tools/input_image/未知字段在直通后保留。

### R-016 技术栈固定 Go + React/TypeScript + SQLite；单进程、无 Redis/MQ/微服务；可本机运行并迁移 Linux/Docker；只做 clean-room，不复制不兼容代码。
- 验收：go.mod/go.sum 无 Redis/MQ/多租户依赖；package.json/package-lock 为 React/TS；SQLite 是唯一存储。
- 验收：单二进制通过 internal/webassets 嵌入前端；Dockerfile 使用非 root、/data 卷并可配置监听地址。
- 验收：LICENSES.md 记录参考项目与直接依赖许可证；仓库无参考项目代码片段，compliance 测试可验证。

### R-017 上游错误归一化为入站协议对应的错误体，客户端不看到上游 key、URL 或原始敏感 body。
- 验收：上游 401/403 → 502 upstream_auth_failed；429 → 429 upstream_rate_limited；5xx → 502/503；超时 → 504。
- 验收：OpenAI/Responses 返回 {error:{...}}；Anthropic（如实现）返回顶层 type=error。
- 验收：错误日志/响应不含 Authorization、x-api-key、上游 base_url 查询串。

## 目标架构与边界

单进程 Go 服务 + SQLite WAL + 内嵌 React 管理端。公开面为 /v1（Codex Responses、OpenAI Chat、模型列表/Codex manifest）和 /backend-api/codex 别名；管理面为 /admin + /admin/api；运维面为 /healthz 与 /readyz。请求流水线：客户端 Key 鉴权 → 入站协议识别 → 手工模型映射 → 候选渠道调度（优先级/健康/容量）→ 进程内并发槽位 → 原生上游 adapter → 首内容提交门控 → 请求日志/统计/诊断环。运行态并发与熔断状态放内存；渠道、映射、日志、捕获与迁移元数据放 SQLite。第一版不引入 Redis、消息队列、微服务或多实例共享状态。Codex 以 0.164.0 的 Responses HTTP SSE 为基准；ZCode 经用户批准延后至 MVP+1，第一版只保留 adapter 边界。前端构建产物输出到 internal/webassets/dist，由 internal/webassets 的唯一 embed 包嵌入，禁止从包目录外 ../web/dist 取文件。

### 组件与集成边界

- cmd/gateway：单进程入口与 serve/migrate/backup/restore/keygen/version 子命令；不承载业务规则。
- internal/config：环境变量解析、默认值、启动校验、secret 脱敏；包括 GATEWAY_CODEX_CONTEXT_WINDOW。
- internal/store：SQLite 打开（WAL、busy_timeout、foreign_keys、单写连接）、版本化迁移、备份/恢复、渠道/映射/日志/捕获仓储；channels.last_used_at 与删除渠道 FK 策略在 0001 定义。
- internal/secret：AES-256-GCM v1 信封、主密钥解析、上游 Key 脱敏、日志/错误脱敏。
- internal/adminauth：PBKDF2 密码、opaque session、登录限流；不依赖 gateway 数据面。
- internal/adminapi：管理 REST API、Origin/CSRF 校验、渠道/映射/统计/请求/调试/捕获端点；只依赖 store、adminauth、upstream probe、observability。
- internal/gateway：客户端鉴权、路由表、错误 envelope、请求编排、首内容提交门控；不直接访问 SQLite 细节。
- internal/routing：候选查询、过滤、排序、进程内每渠道信号量、有限等待、熔断状态；更新 channels.last_used_at；不依赖 adminapi。
- internal/upstream：HTTP client、Responses/Chat 原生 adapter、SSE 解析、超时、重定向限制、连通性探针；不做跨协议转换。
- internal/observability：请求日志、usage 提取、统计查询、诊断环形缓冲、捕获加密持久化与 TTL。
- internal/webassets：唯一 Go embed 包；`//go:embed dist/*` 只引用包目录内的 internal/webassets/dist；T05 放占位 index.html，T10 Vite 输出到该目录，T29 验收前用 npm run build 重新生成真实产物。
- web：React 19 + TypeScript + Vite + TanStack Router/Query + 纯 CSS；只通过 /admin/api 访问后端；Vite outDir=../internal/webassets/dist。
- e2e：真实 Codex CLI 跨进程测试；普通测试包，不加 build tag；ZCode 延后至 MVP+1，证据到位后再新增同层测试。
- 依赖规则：adminapi → store/upstream/observability/adminauth；gateway → routing/upstream/observability/store；routing → store；upstream 不反向依赖 gateway/adminapi；server → webassets。

### 安全与隐私

- 上游 Key 只在创建/更新时接收明文，立即 AES-256-GCM 加密；SQLite 仅存 ciphertext 和 mask；管理列表永不返回明文。
- 主密钥来自 GATEWAY_MASTER_KEY（base64 32 字节），启动时校验；缺失/格式错误拒绝启动；错误信息不回显密钥值。
- Key 揭示接口要求有效 admin session，返回 no-store，不做二次密码；不写请求日志；前端仅在用户点击后显示。
- 管理员密码 PBKDF2-HMAC-SHA256 600k 迭代、随机 salt；session token 只存 SHA-256；cookie HttpOnly/SameSite=Lax/Secure 可配；改密/登出撤销。
- 所有管理写操作校验同源 Origin；管理响应 no-store；SPA 使用 CSP default-src 'self'，不使用 dangerouslySetInnerHTML。
- 默认只记元数据、token、延迟、错误；不记 header、prompt、response body；诊断环仅在内存，5 分钟/50 请求/32MB 上限。
- 按需保存 body 需管理员风险确认，使用同一 AES-GCM 加密，TTL 24h，可删除；超过 8MB 标记 truncated；不保存 Authorization/Cookie；status/read 响应 no-store。
- 上游 base_url 只允许 http/https；HTTP client 拒绝跨 host 重定向；不转发客户端 Authorization/Cookie 到上游；响应头过滤 hop-by-hop 和 Set-Cookie。
- 默认监听 127.0.0.1:8080；上服务器由 TLS 反向代理暴露；Docker 显式 0.0.0.0:8080 并挂载 /data；进程内不终止 TLS。
- SQLite 全部参数化查询；日志统一 Redact；错误响应不回显上游 base_url 查询串、key 或原始敏感 body。

### 数据与迁移

- 绿地项目，不从 juhe-ai/sub2api/new-api/claude-code-hub 迁移任何数据；渠道、Key、模型映射全部重新配置。
- 初始迁移 0001 建立 schema_migrations、admin_credentials、admin_sessions、channels（含 last_used_at）、model_routes、request_logs、request_attempts、request_captures。
- 迁移只前进、不自动回滚；每个 SQL 文件一个事务；schema_migrations 记录 version/name/checksum/applied_at；checksum 不匹配拒绝启动。
- 每次迁移前用 VACUUM INTO 生成 GATEWAY_DATA_DIR/backups/pre-migrate-v<from>-<timestamp>.db；失败保留备份并停止启动。
- restore 子命令先校验备份可打开且 schema 版本兼容，创建 pre-restore 备份后再替换；文档要求先停进程并清理 -wal/-shm。
- SQLite 使用 WAL、busy_timeout=5000、foreign_keys=ON、synchronous=NORMAL、SetMaxOpenConns(1)；日志/捕获写入短事务，不占用流式连接。
- model_routes.channel_id FK ON DELETE CASCADE；request_logs/request_attempts.channel_id FK ON DELETE SET NULL，channel_name 快照保留；删除渠道不破坏历史统计。
- 主密钥不随数据库自动轮换；MVP 要求 GATEWAY_MASTER_KEY 稳定并离线备份；v1 信封预留版本字段，轮换作为后续独立迁移。

## 技术与设计决策

### 路线与许可证
- 选择：clean-room 重写，不 fork 任何参考仓库；只借鉴协议、交互和运维模式。
- 仓库证据：reference/sub2api/LICENSE:1 为 GNU LGPL v3；reference/sub2api/backend/go.mod:32/35 使用 lib/pq 与 Redis，前端为 Vue 3。；reference/new-api/LICENSE:1 为 AGPLv3；reference/new-api/NOTICE:6-25 要求保留前端署名、原项目链接并标记修改。；reference/juhe-ai/LICENSE:1 为 Apache-2.0；README.md:151 声明 SQLite 已退役，运行时为 PG+Redis，两个常驻进程 + maintenance CLI。；reference/claude-code-hub/LICENSE:1 为 MIT；但运行时为 Next.js + Hono + PostgreSQL/Redis。
- 理由：四项参考的许可证与固定技术栈均不完全兼容；fork 的许可证义务、UI/DB 重写和长期 rebase 成本高于 clean-room。
- 替代方案：fork new-api（React+SQLite 最近，但 AGPL + 多租户/支付/任务插件）；fork sub2api（LGPL + Vue + PostgreSQL/Redis + 70 万行）；裁剪 juhe-ai（Apache，但 Vue + PG/Redis + 两个常驻进程 + maintenance CLI）；fork claude-code-hub（MIT，但 Next.js + PG/Redis + 严格同格式）
- 取舍：clean-room 需要自行实现管理端、迁移、调度与协议适配，初期功能少于参考；换取许可证清晰、依赖少、长期可维护。
- 风险：低估 Codex/管理端实现量；误复制参考代码造成 AGPL/LGPL 污染；参考项目迭代快，协议事实会漂移

### 后端运行时与数据库
- 选择：Go 1.26 + 标准库 net/http + database/sql + modernc.org/sqlite（CGO_ENABLED=0）；前端构建输出 internal/webassets/dist，由 internal/webassets 唯一 embed；不使用 Gin/Ent/GORM。
- 仓库证据：本机 go version go1.26.5 windows/amd64；npm 11.16.0 / Node v24.18.0。；reference/sub2api/backend/go.mod:64 使用 modernc.org/sqlite v1.44.3；reference/new-api/go.mod:165 使用 modernc.org/sqlite v1.40.1（间接）。；Go embed 的 pattern 相对包目录：internal/webassets 不能引用 ../../web/dist，因此必须让 Vite 输出到 internal/webassets/dist。；reference/new-api/common/database.go:64 使用 WAL/busy_timeout；本计划沿用同等 SQLite 约束。
- 理由：纯 Go SQLite 驱动可在 Windows/Linux 无 CGO 构建；标准库 HTTP 足够覆盖少量路由；唯一 embed 包避免路径不合法。
- 替代方案：Gin + GORM（参考项目做法）；Ent + PostgreSQL（sub2api）；Chi + sqlc（仍增加生成链）
- 取舍：手写路由与 SQL 仓储需要更多测试；单写者限制多实例。
- 风险：SQLite 文件锁/写放大；手写 HTTP 路由遗漏边界；modernc 版本与 Go 1.26 兼容性

### 前端技术栈与构建产物
- 选择：React 19 + TypeScript 5.6 + Vite 5 + TanStack Router/Query + Vitest/RTL；纯 CSS 设计系统，不引入 UI 组件库；Vite outDir=../internal/webassets/dist。
- 仓库证据：reference/new-api/web/package.json 使用 React 19.2.7、TanStack Router/Query、Vitest。；reference/sub2api/frontend/package.json 使用 Vite 5/Vitest；证明 Vite 构建链可用。；需求 docs/requirements/api-gateway.md:25,67 要求参考面板布局、不能过于简陋。
- 理由：满足 React/TS 固定要求；Vite 轻量；TanStack 处理路由与请求缓存；输出到 embed 包目录解决 Go embed 相对路径限制。
- 替代方案：Next.js/Rsbuild（claude-code-hub/new-api）；Vue 3 + Ant Design Vue（juhe-ai/sub2api）；shadcn/Tailwind 组件库
- 取舍：需要自行实现表格/抽屉/表单/空态；初期开发量较大。
- 风险：管理端视觉质量不足；前端依赖膨胀；构建输出目录配置错误导致 embed 仍为占位页

### Codex 入站协议与 manifest
- 选择：按 Codex 0.164.0 的 Responses HTTP SSE 实现：POST /v1/responses、/backend-api/codex/responses、GET /v1/models 与完整 ModelInfo manifest；不实现 WebSocket 与 compact。
- 仓库证据：本机 codex --version 输出 codex-cli 0.164.0；~/.codex/config.toml（token 已隐藏）配置 wire_api="responses"、supports_websockets=false、base_url 以 /v1 结尾。；本机 codex features list 输出 responses_websockets removed false、remote_compaction_v2 removed false。；reference/sub2api/backend/internal/handler/openai_codex_models_handler.go:14-22 说明 Codex 从 /models?client_version= 或 /backend-api/codex/models 刷新模型；reference/sub2api/backend/internal/service/openai_codex_models_service.go:367-410 要求完整 ModelInfo，多个 nullable 字段必须存在。；reference/new-api/pkg/jsplugin/routing.go:90 注册 POST /v1/responses；reference/claude-code-hub/tests/e2e/responses-ws-codex-cli-transport.test.ts:404-433（尤其 :429-433）给出 Codex CLI 配置；该 E2E 默认 skip，不能作为已通过证据。
- 理由：当前安装版本与本地配置直接证明 HTTP Responses；WebSocket 特性已 removed，强行实现没有依据；最小 manifest 会被当前 parser 拒绝。
- 替代方案：同时实现 Responses WebSocket 与 /responses/compact；只做 Chat Completions；代理 Codex 到 ChatGPT 上游
- 取舍：升级 Codex 后可能重新出现 WS/compact 需求；E2E 固定版本并失败提醒；完整 descriptor 字段较多但可契约测试。
- 风险：Codex 模型 manifest 字段缺失/值不兼容；未来版本重新启用 WebSocket；compact 在长会话触发 404

### ZCode 入站协议
- 选择：在取得真实抓包或官方协议证据前不实现 ZCode adapter；用户已批准 ZCode 延后至 MVP+1，第一版只保留 adapter 边界；T28 保持 needs-info，禁止猜测。
- 仓库证据：rg -ni 'zcode|z-code' reference/sub2api reference/new-api reference/claude-code-hub 无命中。；reference/juhe-ai 中对 ZCode 的命中集中在出站身份/版本跟踪（User-Agent、HTTP-Referer、X-ZCode-App-Version、X-Title 等），见 reference/juhe-ai/docs/functions/智谱GLM账号接入.md:135；未发现入站协议证据。；当前环境 where zcode 无输出，无 ZCode 可执行文件。
- 理由：ZCode 可能使用 Chat、Responses、Anthropic 或私有协议；猜错会静默丢图片/tool/流式字段。用户已批准延后至 MVP+1，第一版不做实现，但 adapter 边界保留。
- 替代方案：假设 ZCode 使用 Chat Completions；假设 ZCode 使用 Anthropic Messages；复用 juhe-ai 的 ZCode 出站身份作为入站协议
- 取舍：ZCode 接入从第一版移出，第一版验收不再被阻塞；MVP+1 需要重新取证，可能推迟 ZCode 上线。
- 风险：无法取得 ZCode 抓包导致 MVP+1 延期；ZCode 动态签名字段无法伪造；ZCode 版本变化

### 上游 Provider Adapter 边界
- 选择：只做原生协议直通：openai_responses、openai_chat；anthropic_messages 仅预留，证据后实现。入站协议必须与渠道协议匹配，不做隐式协议转换。
- 仓库证据：reference/claude-code-hub/src/app/v1/_lib/proxy/provider-selector.ts:166-184 明确 response→codex、openai→openai-compatible 的严格同格式路由。；reference/sub2api/backend/internal/pkg/apicompat 与 reference/new-api/relaykit 的跨协议转换规模巨大（recon 统计 10,481 行非测试代码 + 13,282 行测试），不适合 MVP。；上游 new-api/sub2api 本身支持原生 /v1/responses 与 /v1/chat/completions。
- 理由：原生直通保留 tools/input_image/reasoning/未知字段，避免跨协议丢字段；渠道协议可视化，错误明确。
- 替代方案：统一转成 OpenAI Chat；实现 Responses↔Chat↔Anthropic 全矩阵；只代理单一上游
- 取舍：某些只提供 Chat 的中转站无法服务 Codex Responses；需要在管理端配置协议。
- 风险：目标上游不提供原生 Responses；Anthropic 模型需另建渠道；后续转换成本被低估

### 调度与失败切换
- 选择：候选按 route.priority ASC → channel.priority ASC → last_used_at ASC → id ASC；过滤禁用/熔断/满载；每次请求最多 3 个不同渠道；同一渠道不重试；仅重试网络/401/403/404/408/429/5xx/529。
- 仓库证据：reference/sub2api/backend/internal/service/gateway_scheduling.go 使用 priority + last_used + 同优先级随机；recon 指出其同账号重试 3 次。；reference/new-api/model/channel_cache.go 使用 priority 降序、同优先级 weight 随机。；reference/claude-code-hub/src/app/v1/_lib/proxy/provider-selector.ts:1534-1575 使用最小 priority + costMultiplier + weight 加权随机。；计划新增 channels.last_used_at，T19 在获得槽位后短 UPDATE，用于同优先级 LRU。
- 理由：个人单用户更看重可预测与低重复成本；不照搬权重/同账号重试。
- 替代方案：同账号重试 3 次（sub2api）；weight 加权随机（new-api/CCH）；codex session 粘性亲和（new-api）
- 取舍：单渠道瞬时抖动会直接换渠道；不保证同渠道 session 亲和。
- 风险：400/404 分类不当导致不必要切换；多次切换放大成本；last_used 写入增加 SQLite 写次数

### 渠道并发与满载
- 选择：进程内每渠道 chan struct{} 信号量；满载优先换渠道；全部候选满载最多等待 2s（25ms 轮询），仍无槽位返回 429 channel_capacity_exhausted + Retry-After: 1。
- 仓库证据：reference/sub2api/backend/internal/handler/gateway_helper.go:121-131 使用 30s 等待、100ms 起 ×1.5、上限 2s；reference/sub2api/backend/internal/handler/concurrency_error_response.go:13-22 返回 429 gateway_queue_full。；reference/juhe-ai 使用 270s 请求墙钟与队列等待；reference/claude-code-hub 在 provider 并发满时尝试下一个，最终 503；reference/new-api/model/channel.go:23-60 无渠道级并发字段。
- 理由：30s/270s 对个人使用过长；2s 给瞬时释放窗口，之后快速 429 更可控。
- 替代方案：立即 429；30s 排队；全部不可用返回 503
- 取舍：高峰期部分请求会快速失败；客户端自行重试。
- 风险：等待期间占用连接；槽位泄漏；429 与上游 429 混淆

### 流式提交边界与 response.failed
- 选择：首内容前缓冲原始 SSE，首语义内容出现后提交；提交前失败换渠道，提交后只发一次完整 response.failed，不再换渠道。
- 仓库证据：reference/juhe-ai/backend-go/projects/gateway/internal/gatewayresponse/commitstate.go:35 明确 CanRetryUpstream() = !SemanticCommitted。；reference/sub2api/backend/internal/handler/stream_error_event.go:13-45 定义 responsesFailedEvent/Response：sequence_number、response.created_at 等字段缺失会被严格 Rust serde 拒绝；:61-92 给出 response.failed 写出。；reference/sub2api/backend/internal/handler/gateway_handler.go:1990-2018 明确提交后只能写 SSE 错误；reference/claude-code-hub/src/app/v1/_lib/proxy/forwarder.ts:2060-2066 只在首个有效内容帧前做 stream gate。
- 理由：已输出内容无法无缝拼接第二个上游；完整 response.failed 是 Codex 读懂终止事件的必要条件。
- 替代方案：拼接第二个上游实现无缝；提交后静默 EOF；提交后通用 error 帧
- 取舍：用户可见中断；但协议语义正确。
- 风险：事件分类误判导致提前/延迟提交；response.failed 字段缺失导致客户端解析失败；SSE 缓冲内存过大

### 存储、迁移与备份
- 选择：SQLite WAL + 单写连接 + 嵌入式 forward-only 迁移 + checksum + 迁移前 VACUUM INTO 备份 + restore 子命令；channels.last_used_at；model_routes CASCADE，logs/attempts SET NULL。
- 仓库证据：reference/new-api/model/main.go:325-401 仅 AutoMigrate，无版本表/down migration；reference/sub2api 有 323 个 PostgreSQL 迁移，不能直接用于 SQLite。；docs/requirements/api-gateway.md:76 要求迁移可恢复、备份/恢复。；reference/new-api/common/database.go:64 使用 WAL/busy_timeout，可作为 SQLite 运行参数参考。
- 理由：自建迁移器可控制事务、备份、checksum 与失败语义；FK 策略保证删除渠道不破坏历史日志。
- 替代方案：GORM AutoMigrate；golang-migrate；Ent schema migration
- 取舍：需要手写 SQL 与迁移测试；单写者限制。
- 风险：迁移失败损坏库；WAL 备份不一致；Windows 文件锁

### 密钥与管理员鉴权
- 选择：上游 Key 用 AES-256-GCM v1 信封，主密钥来自环境变量；管理员密码用 PBKDF2-HMAC-SHA256；session 为 opaque token，DB 只存哈希。
- 仓库证据：reference/juhe-ai/backend-go/shared/platform/accountcrypto/crypto.go:89 使用 sha256(secret) 作为 AES-256-GCM 密钥，信封 v1:{iv}:{tag}:{ciphertext}。；reference/sub2api/backend/internal/repository/account_repo.go:151 将 credentials 直接写入；reference/sub2api/backend/internal/repository/aes_encryptor.go:22-35 的 AES key 取自 TOTP 配置，只用于 TOTP 等模块。；reference/new-api/model/channel.go:26 的 Key 为普通 string 列，查看需二次验证；reference/claude-code-hub 使用 ADMIN_TOKEN + HMAC session。
- 理由：满足环境变量主密钥、无二次密码、加密落库；PBKDF2 使用 Go 1.26 标准库 crypto/pbkdf2，减少依赖。
- 替代方案：复用 TOTP 密钥（sub2api）；明文渠道 Key + 2FA（new-api）；ADMIN_TOKEN（CCH）
- 取舍：MVP 不做主密钥轮换；密码遗忘无自动重置。
- 风险：主密钥丢失导致不可解；弱主密钥；session 固定/CSRF

### 日志、统计与按需 body 捕获
- 选择：默认仅元数据；诊断环仅内存（5 分钟/50 请求/32MB）+ 管理员显式保存（AES-GCM、24h TTL、可删除、读取 no-store）；统计从 request_logs 聚合。
- 仓库证据：reference/new-api/model/log.go:57-79 只存元数据/配额，无完整 body；reference/new-api/common/body_storage.go 是临时重放存储，请求结束清理。；reference/sub2api/backend/migrations/136_remove_ops_retry_replay.sql 删除 request_body/request_headers，说明默认不存 body 的取舍。；reference/claude-code-hub/.env.example:129 默认把响应正文写 Redis，与本项目隐私目标相反。
- 理由：默认不持久化 prompt，降低隐私与写放大；内存环保留短期调试能力，管理员显式保存满足按需取证。
- 替代方案：预置 capture 开关（下次请求抓取）；默认全量 body；只存错误 body
- 取舍：超过 5 分钟的请求无法事后抓取；需要明确 UI 提示。
- 风险：内存环泄漏 prompt；捕获数据未清理；统计 SQL 扫描慢

### 管理端范围与布局
- 选择：固定登录 + 6 个受保护页（共 7 页）：登录、总览、渠道、模型映射、请求观测、调试、设置；侧边栏只显示 6 个受保护页；不引入多租户/支付/运营。
- 仓库证据：reference/sub2api/frontend/src/router/index.ts:402-472 管理端页面路由；AccountsView.vue 2,635 行、ChannelsView.vue 1,736 行，说明参考面板信息层级但不宜照搬规模。；reference/new-api/web/src/routes/_authenticated/channels/index.tsx:36、dashboard/index.tsx:23、usage-logs/index.tsx:23 提供 React 路由与页面布局参考。；find . -iname '*workbuddy*' 无输出，workbuddy2api-panel 不在工作区。
- 理由：7 页覆盖需求且避免功能堆砌；登录页不在侧边栏，避免计数歧义。
- 替代方案：照搬 sub2api 17+ 页面；只做一个表格页；引入完整组件库
- 取舍：需要自行实现组件与图表；无多语言/暗色主题。
- 风险：视觉质量不足；页面状态/错误处理不完整；管理端范围膨胀

### 部署与发布
- 选择：多阶段 Docker：Node 构建 internal/webassets/dist，Go 构建单二进制；运行镜像非 root、VOLUME /data、ENV GATEWAY_DATA_DIR=/data、EXPOSE 8080；TLS 由反向代理终止；真实 docker build 为发布前人工验收，argv 验收覆盖 Go 层与 Dockerfile 静态契约。
- 仓库证据：docs/requirements/api-gateway.md:30-31 要求本机优先、可迁 Linux+Docker、无 Redis/消息队列/微服务。；Go embed 限制：pattern 相对包目录，因此 Docker 构建必须先把 web 输出到 internal/webassets/dist。；T29 的 Dockerfile 契约测试读取 Dockerfile，断言 USER/VOLUME/ENV/EXPOSE 且无 secret；实际 docker build 不在 strict argv 白名单内。
- 理由：单二进制 + 数据卷 + 反向代理 TLS 符合个人自托管；不引入容器编排。
- 替代方案：本地裸二进制；Kubernetes；进程内 TLS
- 取舍：真实容器构建需要人工验收；Dockerfile 契约测试只能覆盖声明，不覆盖运行时。
- 风险：镜像泄露 secret；卷未持久化；Linux 文件权限/路径错误

### 测试与验收
- 选择：Go httptest + 临时 SQLite + 假上游；React Vitest/RTL；真实 Codex CLI E2E；全部验收使用 strict 白名单内的 go test 与 npm run test:<slice>，不使用 shell/python -c。
- 仓库证据：需求 docs/requirements/api-gateway.md:96-98 要求行为验收 + 真实测试、唯一 argv 命令、禁止 shell/python -c。；reference/claude-code-hub/tests/e2e/responses-ws-codex-cli-transport.test.ts:404-433 提供真实 Codex CLI 配置思路，但默认 skip；本计划改为普通 e2e 包，版本不符/缺 codex 即失败。；本机 codex-cli 0.164.0 可执行，满足真实 E2E 前置条件。
- 理由：行为测试覆盖协议/调度/故障；真实 Codex 能发现模拟客户端看不到的模型发现/请求形状问题；T22 用 Node 脚本同时跑前后端测试。
- 替代方案：只做单元测试；只做假客户端契约测试；使用 shell 脚本验收
- 取舍：E2E 较慢且依赖本机 codex；固定版本降低漂移。
- 风险：Codex 版本漂移；Windows 进程/端口差异；测试误跳过

## 任务 T01：建立 Go 模块、配置校验与依赖许可基线
- 状态：ready
- 覆盖需求：R-013、R-016
- 文件：`go.mod`、`go.sum`、`cmd/gateway/main.go`、`internal/config/config.go`、`internal/config/config_test.go`、`internal/compliance/licenses_test.go`、`LICENSES.md`
- 依赖：无
- 做法：
  1. 初始化 module（本地名 ai-router，Go 1.26.0），只加入后续任务确实使用的依赖。
  2. 实现 Config：GATEWAY_LISTEN_ADDR 默认 127.0.0.1:8080、GATEWAY_DATA_DIR、GATEWAY_DB_PATH、GATEWAY_MASTER_KEY（base64 32 字节）、GATEWAY_CLIENT_KEY（>=24 字符）、GATEWAY_ADMIN_PASSWORD（首次启动）、GATEWAY_COOKIE_SECURE、GATEWAY_CODEX_CONTEXT_WINDOW、上游超时/重试/容量等待/捕获上限。
  3. 实现启动校验与脱敏 String()/错误信息；缺失或非法值只引用变量名，不输出值；非法监听地址也失败。
  4. 在 main 中分发 serve/version/keygen 子命令；serve 目前只完成配置加载与占位服务器。
  5. go mod tidy 生成 go.sum；LICENSES.md 增加直接依赖许可证清单并与 go.mod/go.sum 对齐；compliance 测试读取 LICENSES.md、go.mod 与 go.sum，检查四个参考许可证、直接依赖许可证清单与禁用 Redis/MQ 依赖。禁用标识/路径扫描只覆盖项目生产源码：cmd/、internal/、web/src/、e2e/ 下的 *.go、*.ts、*.tsx、*.js；排除所有 *_test.go、*.test.ts、*.test.tsx、*.test.js 以及 internal/compliance/licenses_test.go 自身；显式排除且不遍历 reference/、.zcode/、web/node_modules/、internal/webassets/dist/、LICENSES.md、docs/，扫描不因 LICENSES.md 列出 sub2api/new-api/juhe-ai/claude-code-hub 而命中。对扫描范围内的文件断言不 import reference/、不含参考项目专有标识；文件注明自动扫描不替代人工审计。
- 行为验收：
  - 缺少 GATEWAY_MASTER_KEY、base64 不是 32 字节、GATEWAY_CLIENT_KEY 少于 24 字符或 GATEWAY_LISTEN_ADDR 非法时启动失败，错误不含密钥值。
  - 合法配置得到默认 127.0.0.1:8080 与数据目录；CODEX_CONTEXT_WINDOW 默认 128000。
  - 配置的 String()/日志输出不包含 master key、client key、admin password。
  - LICENSES.md 含 sub2api LGPL-3.0-or-later、new-api AGPLv3、juhe-ai Apache-2.0、claude-code-hub MIT，并列出直接依赖许可证清单；compliance 测试读取 LICENSES.md、go.mod 与 go.sum，检查四个参考许可证、直接依赖许可证与禁用 Redis/MQ 依赖均通过。
  - 禁用标识/路径扫描仅覆盖 cmd/、internal/、web/src/、e2e/ 下的 *.go、*.ts、*.tsx、*.js 生产源码，排除所有 *_test.go、*.test.ts、*.test.tsx、*.test.js 及 internal/compliance/licenses_test.go 自身；扫描范围内不出现 reference/ 导入或参考项目专有标识；显式排除且不遍历 reference/、.zcode/、web/node_modules/、internal/webassets/dist/、LICENSES.md、docs/，扫描不因 LICENSES.md 列出 sub2api/new-api/juhe-ai/claude-code-hub 而命中；文件注明自动扫描不替代人工审计。
- 自动化验收（argv）：`go test ./internal/config ./internal/compliance`
- 风险：环境变量中的主密钥/客户端 Key 可能被错误信息或日志带出；弱 Key 会让加密形同虚设；许可证基线可能随依赖漂移。
- 风险缓解：错误信息只引用变量名；Config 实现脱敏 String()；主密钥强制 base64 32 字节；compliance 测试同时读取 LICENSES.md、go.mod 与 go.sum 检查禁用依赖；测试断言输出不含 secret；禁用标识/路径扫描只能发现明显复制，自动扫描不替代人工审计
- 依据：所有后续模块需要统一配置与 secret 约束；先把许可证与依赖基线固定，避免实现中误拷参考代码。

## 任务 T02：SQLite 初始化、版本化迁移与迁移前备份
- 状态：ready
- 覆盖需求：R-003、R-011、R-012、R-013
- 文件：`internal/store/db.go`、`internal/store/migrate.go`、`internal/store/backup.go`、`internal/store/migrate_test.go`、`internal/store/migrations/0001_init.sql`、`cmd/gateway/main.go`
- 依赖：T01
- 做法：
  1. 用 modernc.org/sqlite 打开数据库，设置 WAL、busy_timeout=5000、foreign_keys=ON、synchronous=NORMAL、SetMaxOpenConns(1)。
  2. 实现 go:embed 迁移加载、schema_migrations 版本/checksum 记录、每个迁移一个事务、checksum 不匹配拒绝启动。
  3. 在 0001_init.sql 建立全部表与索引；channels 含 last_used_at；model_routes.channel_id ON DELETE CASCADE；request_logs/request_attempts.channel_id ON DELETE SET NULL 并保留 channel_name。
  4. 实现 VACUUM INTO 备份与 restore 校验；迁移前自动备份到 filepath.Join(GATEWAY_DATA_DIR, "backups", "pre-migrate-v<from>-<timestamp>.db")。
  5. 测试 fresh migrate、重复 migrate、坏 checksum、迁移失败不改库、备份可重新打开、删除 channel 的 FK 行为。
  6. 在 cmd/gateway/main.go 注册 migrate/backup/restore 子命令，只做参数解析与退出码处理，调用 internal/store 导出的迁移/备份/恢复入口；不在 cmd 层复制业务逻辑。
- 行为验收：
  - 新库迁移后 schema_migrations 有当前版本，所有表/索引存在；第二次 migrate 不重复执行。
  - 修改已应用迁移内容导致 checksum 不匹配时启动失败，原库 schema 不变。
  - 迁移前备份文件可打开，包含迁移前已插入的数据；restore 校验失败时拒绝覆盖。
  - 删除 channel 后 model_routes 级联删除，request_logs/request_attempts.channel_id 置 NULL 且 channel_name 保留；SQLite WAL/foreign_keys/单写连接生效。
  - cmd/gateway 的 migrate/backup/restore 子命令在 main 中注册并调用 store 导出入口；store 层在 internal/store/migrate_test.go 以导出入口覆盖迁移/备份/恢复行为；go test ./cmd/gateway 仅做编译级验证，不执行子命令。
  - 迁移后的 sqlite_master 不含 users/roles/groups/billing 表。
  - 备份文件落在 GATEWAY_DATA_DIR/backups，不依赖进程 cwd 的 data/ 目录。
- 自动化验收（argv）：`go test ./internal/store ./cmd/gateway`
- 风险：SQLite 在 Windows 文件锁、WAL 与备份并发下可能 busy 或产生不一致备份；迁移失败可能留下半成品；FK 策略错误会阻止删除或破坏历史。
- 风险缓解：单写连接 + busy_timeout；迁移事务与 checksum；VACUUM INTO 前完成写事务；失败迁移测试验证 schema 不变；FK ON DELETE SET NULL + channel_name 快照；restore 前创建 pre-restore 备份
- 依据：数据层是所有功能的基础；先固定可恢复迁移、备份和删除渠道的数据保留语义。

## 任务 T03：上游 Key AES-GCM 信封与脱敏
- 状态：ready
- 覆盖需求：R-003、R-013
- 文件：`internal/secret/envelope.go`、`internal/secret/envelope_test.go`、`internal/secret/redact.go`、`internal/secret/redact_test.go`
- 依赖：T01
- 做法：
  1. 解析 GATEWAY_MASTER_KEY 为 32 字节；实现 AES-256-GCM，12 字节随机 nonce，AAD 绑定用途，输出 v1:<base64url(nonce)>:<base64url(ciphertext)>。
  2. 实现 Encrypt/Decrypt 与版本校验；错误不返回明文。
  3. 实现 Key 脱敏：长 Key 保留前缀 3 + 后缀 4，其余为 *；短 Key 全部 *。
  4. 实现 Redact(map/header/value)，覆盖 authorization、x-api-key、cookie、set-cookie、proxy-authorization。
  5. 测试 roundtrip、错误 key、篡改、格式错误、脱敏输出。
- 行为验收：
  - 同一明文加密两次得到不同 nonce/密文，Decrypt 均还原。
  - 错误主密钥或篡改密文时 Decrypt 失败，错误不含明文。
  - 列表可得到 sk-***abcd 形式 mask；Redact 后日志不含完整 key。
  - v1 前缀/版本不匹配被拒绝。
- 自动化验收（argv）：`go test ./internal/secret`
- 风险：自行实现加密容易在 nonce/AAD/错误处理上出错；主密钥丢失会导致所有上游 Key 不可解。
- 风险缓解：使用标准库 crypto/aes + cipher.NewGCM；随机 nonce + AAD + 版本字段；测试错误 key/篡改；文档要求离线备份主密钥
- 依据：渠道 Key 加密是硬性安全要求；先独立实现并用测试锁定格式，再接入渠道仓储。

## 任务 T04：单管理员密码、session 与登录限流
- 状态：ready
- 覆盖需求：R-001、R-002
- 文件：`internal/adminauth/password.go`、`internal/adminauth/session.go`、`internal/adminauth/limiter.go`、`internal/adminauth/password_test.go`、`internal/adminauth/session_test.go`
- 依赖：T02
- 做法：
  1. 用 crypto/pbkdf2（HMAC-SHA256，600000 迭代、16 字节 salt、32 字节 key）实现密码哈希与校验；编码为 pbkdf2-sha256$iter$salt$hash。
  2. 首次启动无 admin 记录时从 GATEWAY_ADMIN_PASSWORD 创建；密码最短 12 字符；已有记录时忽略环境变量。
  3. 实现 opaque session：32 字节随机 token，DB 存 SHA-256，12h 绝对过期；登录时生成新 token，登出/改密撤销。
  4. 实现登录限流：同一来源 15 分钟最多 5 次失败，成功清除；使用常量时间比较避免用户枚举。
  5. 测试哈希、错误密码、过期/撤销、session 轮换、限流。
- 行为验收：
  - 正确密码登录成功；错误密码失败；数据库不含明文密码。
  - session 过期或被撤销后校验失败；重新登录生成不同 token。
  - 第 6 次连续失败被限流；成功登录后计数清零。
  - 改密后除当前 session 外的旧 session 全部失效。
- 自动化验收（argv）：`go test ./internal/adminauth`
- 风险：密码哈希参数过低、session token 泄露、登录暴力破解。
- 风险缓解：PBKDF2 600k + 随机 salt；session 只存哈希 + 绝对过期；登录限流 + 常量时间比较；改密撤销其他 session
- 依据：单管理员鉴权是管理端的第一道边界；先独立于 HTTP 实现，便于测试和复用。

## 任务 T05：HTTP 服务器骨架、健康检查与优雅退出
- 状态：ready
- 覆盖需求：R-013、R-014
- 文件：`internal/server/server.go`、`internal/server/health.go`、`internal/server/server_test.go`、`internal/webassets/assets.go`、`internal/webassets/dist/index.html`、`cmd/gateway/main.go`
- 依赖：T02、T04
- 做法：
  1. 用 stdlib net/http ServeMux 组装服务器；加入 request ID、panic recover、security headers、管理面 no-store、请求体大小限制。
  2. 实现 /healthz 与 /readyz；ready 检查 DB ping、schema_migrations 当前版本、主密钥加载状态。
  3. 实现 SIGINT/SIGTERM：停止接受新连接，readiness 立即 503，http.Server.Shutdown 30s；超时取消全局 context。
  4. 在 internal/webassets 包内用 //go:embed dist/* 嵌入 internal/webassets/dist；SPA fallback 到 index.html；T05 先放占位 index.html，T10 构建覆盖。
  5. 测试健康状态、迁移未就绪、退出中的 readiness、嵌入资源。
- 行为验收：
  - /healthz 200；DB 关闭或迁移版本落后时 /readyz 503，迁移完成后 200。
  - Shutdown 开始后 /readyz 返回 503；进行中的请求在 30s 内完成或被取消。
  - GET /admin 返回嵌入的 SPA；未知 /admin 路径 fallback 到 index.html。
  - 服务器默认监听 127.0.0.1:8080，不自动暴露公网。
- 自动化验收（argv）：`go test ./internal/server`
- 风险：健康检查只返回静态 200 会误报；优雅退出等待过短会截断长流；embed 路径错误会导致构建失败。
- 风险缓解：readiness 检查 DB ping + 迁移版本；退出前先置 503；30s Shutdown + 超时取消；embed 包只使用包内 dist；测试覆盖未就绪/退出中/嵌入资源
- 依据：健康与退出语义必须先固定；唯一 embed 包避免 Go embed 不能跨包目录的硬限制。

## 任务 T06：管理端登录/session API 与 CSRF 防护
- 状态：ready
- 覆盖需求：R-002、R-014
- 文件：`internal/adminapi/auth.go`、`internal/adminapi/middleware.go`、`internal/adminapi/auth_test.go`、`internal/adminapi/middleware_test.go`、`internal/server/server.go`
- 依赖：T04、T05
- 做法：
  1. 实现 POST /admin/api/auth/login、POST /admin/api/auth/logout、GET /admin/api/auth/session、POST /admin/api/auth/password。
  2. 设置 cookie：HttpOnly、SameSite=Lax、Path=/、Secure 可配、Max-Age 12h；响应 no-store。
  3. 实现 admin session 中间件；实现 Origin 校验，所有 POST/PATCH/DELETE 要求同源。
  4. 实现 session 列表/撤销接口（供设置页）；改密撤销其他 session。
  5. 测试登录/登出/会话/CSRF/改密。
- 行为验收：
  - 登录成功设置 HttpOnly session cookie；GET session 返回管理员身份，logout 后返回 401。
  - 跨源 POST 返回 403；同源 POST 成功。
  - 错误密码返回 401，不泄露用户名/密码细节。
  - 改密后其他 session 失效，当前 session 继续可用。
- 自动化验收（argv）：`go test ./internal/adminapi`
- 风险：CSRF、cookie 配置错误、session 固定。
- 风险缓解：SameSite=Lax + Origin 校验；HttpOnly/Secure 可配；登录后轮换 token；改密撤销其他 session
- 依据：管理 API 的所有后续端点都依赖 session 中间件；先完成登录边界。

## 任务 T07：渠道 CRUD、加密存储与配置校验
- 状态：ready
- 覆盖需求：R-003、R-004
- 文件：`internal/store/channels.go`、`internal/store/channels_test.go`、`internal/adminapi/channels.go`、`internal/adminapi/channels_test.go`、`internal/store/migrations/0001_init.sql`、`internal/store/migrate_test.go`
- 依赖：T02、T03、T06
- 做法：
  1. 实现 channels 仓储：Create/Get/List/Update/Delete/SetEnabled/UpdateHealth/TouchLastUsed，Key 只写 key_ciphertext + key_mask。
  2. 实现 /admin/api/channels CRUD；list/detail 永不返回 key；更新时只有显式提供 key 才重新加密。
  3. 校验 base_url（http/https、无 userinfo、无 fragment）、name 唯一、protocol 枚举、priority 0..1000、max_concurrency 1..64。
  4. 删除渠道时依赖 FK：model_routes 级联，request_logs/attempts channel_id 置 NULL 但保留 channel_name。
  5. 测试 CRUD、密文落库、列表脱敏、非法输入、删除级联/历史保留、TouchLastUsed。
  6. 在 0001_init.sql 的 channels 定义上增加 name UNIQUE（项目尚未发布，直接修改 0001 并同步更新迁移 checksum 基线；不新增迁移文件），并把仓储层唯一冲突映射为 409/400；迁移测试由 internal/store/migrate_test.go 覆盖。
- 行为验收：
  - 创建渠道后 SQLite 中 key_ciphertext 为密文，明文不可检索；列表/detail JSON 无 key。
  - 更新 key 后旧密文不可解、新 key 可解；不提供 key 时保持原 key。
  - 非法 base URL、protocol、并发上限返回 400。
  - 删除渠道后 model_routes 删除，历史 request_logs 保留 channel_name 且 channel_id 为 NULL；TouchLastUsed 更新 last_used_at。
  - 重复 channels.name 的创建或更新返回 409/400，数据库保留原记录。
  - 0001_init.sql 增加 name UNIQUE 后，internal/store 迁移测试通过（checksum 基线同步，fresh migrate 正常）。
- 自动化验收（argv）：`go test -count=1 ./internal/adminapi ./internal/store`
- 风险：渠道 Key 可能在列表/错误/日志中泄漏；删除渠道破坏历史统计；last_used 写入影响性能。
- 风险缓解：仓储层统一加密/脱敏；list/detail 结构体不含 key；FK SET NULL + channel_name 快照；TouchLastUsed 短 UPDATE；测试断言 JSON/日志无明文
- 依据：渠道是路由和上游凭据的载体；先完成安全 CRUD 和删除语义，后续映射/调度/探针都依赖它。

## 任务 T08：上游 Key 明文揭示接口
- 状态：ready
- 覆盖需求：R-003、R-014
- 文件：`internal/adminapi/channel_secrets.go`、`internal/adminapi/channel_secrets_test.go`、`internal/server/server.go`
- 依赖：T07
- 做法：
  1. 实现 GET /admin/api/channels/{id}/key：校验 admin session，读取并解密 key，返回 {key, mask}。
  2. 设置 Cache-Control:no-store、Pragma:no-cache；不写请求日志、不记录 key。
  3. 前端点击才调用；错误统一 404/500，不区分“渠道不存在/解密失败”的敏感细节。
  4. 测试揭示明文、列表仍脱敏、no-store、日志无 key。
- 行为验收：
  - 揭示接口返回完整明文，且与创建时一致。
  - 响应头包含 no-store；列表接口仍只返回 mask。
  - 解密失败返回 500 且响应/日志不含密文或明文。
  - 无 admin session 返回 401。
- 自动化验收（argv）：`go test -v ./internal/adminapi`
- 风险：揭示接口被滥用或浏览器缓存泄漏完整 Key。
- 风险缓解：admin session + no-store；不写日志；前端只在点击后调用；测试缓存头/日志
- 依据：需求明确要求点击直接显示、不做二次密码；用 session + no-store 控制风险。

## 任务 T09：模型映射 CRUD 与调度校验
- 状态：ready
- 覆盖需求：R-004、R-005
- 文件：`internal/store/routes.go`、`internal/store/routes_test.go`、`internal/adminapi/routes.go`、`internal/adminapi/routes_test.go`
- 依赖：T07
- 做法：
  1. 实现 model_routes 仓储：Create/Update/Delete/List/ListByModelAndProtocol。
  2. 实现 /admin/api/model-routes CRUD；字段 client_model、channel_id、upstream_model、priority、enabled。
  3. 校验同一 client_model+channel_id 唯一；channel 必须存在；upstream_model 非空；priority 0..1000。
  4. 提供 /admin/api/models：从启用的 model_routes 去重得到客户端模型列表。
  5. 测试 CRUD、唯一约束、禁用过滤、模型列表。
- 行为验收：
  - 创建映射成功；重复 client_model+channel_id 返回 409/400。
  - 禁用映射不出现在模型列表/候选查询中。
  - 删除映射后模型若已无启用路由则从 /admin/api/models 消失。
  - 非法 channel_id/priority 被拒绝。
- 自动化验收（argv）：`go test -v -count=1 ./internal/adminapi`
- 风险：映射重复/漂移导致调度不可预测；禁用状态被忽略。
- 风险缓解：数据库唯一约束；仓储查询统一过滤 enabled；API 校验 channel 存在；测试覆盖禁用/重复
- 依据：手工模型映射是 MVP 的唯一路由来源；必须先把数据约束固定。

## 任务 T10：React 管理端骨架、登录页与设计系统
- 状态：ready
- 覆盖需求：R-002、R-014
- 文件：`web/package.json`、`web/package-lock.json`、`web/tsconfig.json`、`web/vite.config.ts`、`web/index.html`、`web/src/main.tsx`、`web/src/app/App.tsx`、`web/src/app/App.test.tsx`、`web/src/styles.css`
- 依赖：T06
- 做法：
  1. 创建 React 19 + TypeScript 5.6 + Vite 5 + TanStack Router/Query + Vitest/RTL 工程；npm install 生成 package-lock。
  2. package.json 定义 build（vite build）、test 以及 test:app/test:settings/test:channels/test:mappings/test:dashboard/test:requests/test:capture/test:debug 脚本，分别运行对应 Vitest 文件；test:debug 交给 T22 的 Node 脚本。
  3. Vite build.outDir=../internal/webassets/dist，emptyOutDir=true；确保 T29 构建后 embed 包拿到真实 UI。
  4. 实现设计 token、6 项侧边栏 + 独立登录页、顶栏、内容区、响应式折叠；loading/empty/error/toast；禁止 dangerouslySetInnerHTML。
  5. 测试未登录重定向、登录成功、6 个导航项、登出、package.json 声明 React/TS/Vite/Vitest。
  6. 路由表只注册登录页与 6 个受保护页，不提供 /register、/payment、/topup、/rebate 路由或导航入口。
- 行为验收：
  - 未登录访问 / 显示登录页，不渲染管理导航。
  - 登录成功后侧边栏出现总览、渠道、模型映射、请求、调试、设置 6 项；登录页不在侧边栏，总计 7 页。
  - 登出后回到登录页，session 查询返回 401。
  - package.json 声明 React/TypeScript/Vite/Vitest，并定义 build=vite build 脚本。
  - 路由与导航中不存在 /register、/payment、/topup、/rebate；直接访问这些路径显示 404 或重定向登录，不渲染管理页。
- 自动化验收（argv）：`npm run test:app`
- 风险：从零实现设计系统可能简陋或过度；依赖版本漂移；Vite 输出目录错误导致 embed 占位页。
- 风险缓解：固定 package-lock；先做少量高质量基础组件；Vite outDir 指向 internal/webassets/dist；组件测试覆盖关键页面状态；不引入 UI 组件库
- 依据：管理端是需求中的独立交付物；先建立壳、设计系统和正确的构建输出目录。

## 任务 T11：管理端设置页：改密与会话撤销
- 状态：ready
- 覆盖需求：R-002、R-014
- 文件：`web/src/features/settings/SettingsPage.tsx`、`web/src/features/settings/SettingsPage.test.tsx`、`web/src/lib/api.ts`
- 依赖：T06、T10
- 做法：
  1. 在 web/src/lib/api.ts 封装 fetch：credentials include、JSON、401 跳登录、no-store 处理。
  2. 实现设置页：修改密码（当前密码/新密码/确认）、活跃 session 列表、撤销单个/全部、登出。
  3. 密码规则前端提示 12 字符；错误显示服务端安全文案。
  4. 测试改密、撤销 session、登出。
- 行为验收：
  - 输入正确当前密码与新密码后改密成功，其他 session 被撤销。
  - session 列表显示创建/最后使用/过期时间；撤销后该行消失。
  - 登出按钮清除 cookie 并跳转登录。
  - 401 时统一跳登录，不渲染受保护内容。
- 自动化验收（argv）：`npm run test:settings`
- 风险：密码/session 管理 UI 可能泄露 token 或错误处理不一致。
- 风险缓解：只显示 session 元数据，不显示 token；统一 API 401 处理；测试撤销/改密
- 依据：需求要求管理员登录和 session 管理；设置页集中处理安全操作。

## 任务 T12：管理端渠道页：脱敏 Key、揭示、复制与测试入口
- 状态：ready
- 覆盖需求：R-003、R-014
- 文件：`web/src/features/channels/ChannelsPage.tsx`、`web/src/features/channels/ChannelsPage.test.tsx`、`web/src/features/channels/api.ts`、`web/src/features/channels/types.ts`
- 依赖：T07、T08、T10、T11、T22
- 做法：
  1. 实现渠道列表：名称、base URL、协议、优先级、并发、启停、健康状态、mask。
  2. 实现创建/编辑抽屉：base URL、协议、key（编辑时可留空）、优先级、并发、启用；提交后刷新。
  3. 实现点击揭示：调用 key 接口，在只读输入框显示完整 key + 复制按钮；离开页面/关闭抽屉后清除。
  4. 实现删除确认、启停开关、测试按钮（调用 T22 的调试 API，失败显示脱敏错误）。
  5. 测试初始 mask、揭示、复制、创建校验、启停、测试按钮失败脱敏。
- 行为验收：
  - 列表初始不显示完整 key；点击后只显示当前渠道完整 key，关闭后清除。
  - 创建非法 URL/并发 0 时显示字段错误，不发请求。
  - 启停后列表状态更新；删除需二次确认。
  - 测试按钮失败时显示脱敏错误，不显示上游 key。
- 自动化验收（argv）：`npm run test:channels`
- 风险：完整 Key 可能停留在 DOM/状态中，或被错误信息带回；测试按钮依赖 T22 的调试 API。
- 风险缓解：只在点击后请求；关闭即清除状态；错误统一脱敏；T22 先完成调试 API；测试断言初始 DOM 无完整 key
- 依据：渠道页是管理端最核心的运维入口；Key 展示和测试按钮必须与后端安全/调试接口一起验收。

## 任务 T13：管理端模型映射页
- 状态：ready
- 覆盖需求：R-004、R-014
- 文件：`web/src/features/mappings/MappingsPage.tsx`、`web/src/features/mappings/MappingsPage.test.tsx`、`web/src/features/mappings/api.ts`
- 依赖：T09、T10、T11
- 做法：
  1. 实现映射表格：客户端模型、渠道、上游模型、优先级、启停、更新时间。
  2. 实现创建/编辑：模型下拉来自 /admin/api/models 与渠道列表；priority/enabled；重复映射提示。
  3. 按客户端模型分组展示；支持筛选渠道/模型。
  4. 测试创建、重复冲突、启停、空态。
- 行为验收：
  - 创建映射后表格出现新行，字段与输入一致。
  - 重复 client_model+channel 显示冲突错误，不新增行。
  - 禁用映射后状态展示为禁用，模型列表刷新后不再包含无启用路由的模型。
  - 无映射时显示空态并引导创建。
- 自动化验收（argv）：`npm run test:mappings`
- 风险：映射表单字段多，容易出现渠道/模型不一致。
- 风险缓解：后端唯一约束兜底；表单联动校验；测试冲突/空态
- 依据：手工映射是 MVP 的路由来源；管理 UI 必须能可靠维护。

## 任务 T14：请求日志、统计查询与管理 API
- 状态：ready
- 覆盖需求：R-011、R-013
- 文件：`internal/observability/record.go`、`internal/observability/query.go`、`internal/observability/observability_test.go`、`internal/adminapi/observability.go`、`internal/adminapi/observability_test.go`
- 依赖：T02、T06
- 做法：
  1. 实现请求日志/attempt 写入：request_id、protocol、endpoint、model、channel_name/id、status、error、stream、attempt_count、ttft、latency、nullable tokens、usage_source。
  2. 实现查询：时间范围、模型、渠道、状态、分页；详情返回 attempts 与错误。
  3. 实现统计：请求数、成功/错误率、输入/输出 token、平均/P95 延迟，按小时/模型/渠道聚合。
  4. 实现 90 天日志保留清理任务；索引覆盖 started_at/channel/model/status。
  5. 测试只写元数据、usage 缺失为空、筛选、错误率、P95。
- 行为验收：
  - 请求日志不包含 header/body/Authorization/prompt。
  - 统计 API 返回请求数、错误率、输入/输出 token、平均/P95 延迟，并可按模型/渠道过滤。
  - usage 缺失时 token 字段为 null，统计 sum 不把 null 当 0 伪装成已知值。
  - 超过保留期的日志被清理，捕获表不受影响。
- 自动化验收（argv）：`go test ./internal/observability`
- 风险：SQLite 写放大/统计慢；日志误存 body 或 header。
- 风险缓解：短事务 + 单写连接；只存元数据 + 索引；nullable token；测试断言无 body/header
- 依据：观测和管理端数据源依赖统一日志/统计查询；先完成存储与 API，再做页面。

## 任务 T15：管理端总览 Dashboard
- 状态：ready
- 覆盖需求：R-011、R-014
- 文件：`web/src/features/dashboard/DashboardPage.tsx`、`web/src/features/dashboard/DashboardPage.test.tsx`、`web/src/features/dashboard/charts.tsx`
- 依赖：T10、T11、T14
- 做法：
  1. 实现时间范围选择（24h/7d/30d）、四张指标卡（请求数、token、平均/P95 延迟、错误率）。
  2. 实现按小时趋势的轻量 SVG 折线/柱状图，以及按渠道/模型 Top 表。
  3. 实现 loading/empty/error；错误显示重试。
  4. 测试指标渲染、筛选请求、空态、错误态。
- 行为验收：
  - 默认 24h 数据渲染四张指标卡和趋势图。
  - 切换时间范围会带新参数请求；空数据展示空态而非 NaN。
  - 渠道/模型 Top 表可跳转请求页并带上筛选。
  - 统计错误显示可重试错误，不白屏。
- 自动化验收（argv）：`npm run test:dashboard`
- 风险：图表库引入过大；统计口径与后端不一致。
- 风险缓解：使用自绘 SVG，不加图表库；API 契约测试/类型；空态/错误态覆盖
- 依据：总览是管理端门面；先做轻量可维护的可视化，不引入图表依赖。

## 任务 T16：管理端请求观测列表与详情
- 状态：ready
- 覆盖需求：R-011、R-014
- 文件：`web/src/features/requests/RequestsPage.tsx`、`web/src/features/requests/RequestsPage.test.tsx`、`web/src/features/requests/RequestDetail.tsx`
- 依赖：T10、T11、T14
- 做法：
  1. 实现筛选：时间、模型、渠道、状态、错误码；分页表格显示请求数/token/延迟/渠道/状态。
  2. 实现详情抽屉：元数据、usage、attempts 列表、错误 code/message、semantic_committed、capture 可用提示。
  3. 默认不展示 body；捕获按钮由 T25 接入。
  4. 测试筛选参数、分页、详情 attempts、空态/错误。
- 行为验收：
  - 列表按时间倒序，筛选后请求带对应 query 参数。
  - 详情显示每次 attempt 的渠道、状态、延迟与错误，不显示 body/header。
  - usage 缺失显示 —；错误请求有可读错误摘要。
  - 无数据时显示空态，不是空白表格。
- 自动化验收（argv）：`npm run test:requests`
- 风险：日志量大导致表格卡顿；详情误展示敏感数据。
- 风险缓解：服务端分页 + 索引；只展示元数据；body 入口只走捕获接口
- 依据：请求观测是排障核心；先完成元数据视图，body 捕获作为独立任务。

## 任务 T17：客户端鉴权、入站路由、错误 envelope 与模型列表
- 状态：ready
- 覆盖需求：R-001、R-008、R-015、R-017
- 文件：`internal/gateway/auth.go`、`internal/gateway/errors.go`、`internal/gateway/router.go`、`internal/gateway/models.go`、`internal/gateway/router_test.go`
- 依赖：T02、T05、T09
- 做法：
  1. 注册 POST /v1/responses、/backend-api/codex/responses、POST /v1/chat/completions、GET /v1/models、/v1/models/{model}、/backend-api/codex/models。
  2. 实现单客户端 Key 鉴权：Authorization Bearer / x-api-key，SHA-256 + constant-time；返回统一 401。
  3. 实现入站协议识别与 endpoint 常量；不支持路径 404，不注册 embeddings/images/audio/video。
  4. 实现 OpenAI/Responses 错误 envelope；未知上游错误不泄露 key/URL；无路由/协议不匹配返回 404 model_not_available。
  5. 实现普通模型列表与 Codex manifest 占位（T26 完成细节），从启用映射取模型。
  6. 测试鉴权、路由、404、错误形状、模型列表、无映射/协议不匹配不调用上游。
- 行为验收：
  - 错误/缺失客户端 Key 返回 401；正确 Key 进入 handler。
  - /v1/responses、/v1/chat/completions、/backend-api/codex/responses 可用；/v1/embeddings 返回 404。
  - /v1/models 只列启用的客户端模型；带 client_version 走 manifest 分支；无映射/协议不匹配返回 model_not_available。
  - 错误响应为 {error:{message,type,code}}，不含上游 key。
- 自动化验收（argv）：`go test ./internal/gateway`
- 风险：路由/错误形状不兼容 Codex；客户端 Key 时序/泄露；无映射错误码不一致。
- 风险缓解：endpoint 常量集中；协议专属错误构造器；constant-time compare；测试 401/404/错误体；model_not_available 统一
- 依据：客户端入口是所有数据面请求的第一层；先固定 surface、错误契约和 model_not_available 语义。

## 任务 T18：上游 HTTP client 与 Responses/Chat 原生 adapter
- 状态：ready
- 覆盖需求：R-008、R-010、R-015
- 文件：`internal/upstream/client.go`、`internal/upstream/responses.go`、`internal/upstream/chat.go`、`internal/upstream/probe.go`、`internal/upstream/client_test.go`
- 依赖：T01、T07
- 做法：
  1. 实现 http.Transport 超时（连接 10s、TLS 10s、响应头 180s）、流式读空闲 300s、非流式总 300s、流式总 15m；拒绝跨 host 重定向。
  2. 实现 base URL 校验与路径拼接；Responses → /responses，Chat → /chat/completions；Authorization: Bearer 上游 key。
  3. 请求体用 map[string]json.RawMessage 仅替换 model；保留 tools/input_image/reasoning/未知字段；过滤客户端 Authorization/Cookie/hop-by-hop。
  4. 实现 probe：最小 Responses/Chat 请求，返回状态/延迟/脱敏错误；更新渠道 health。
  5. 测试路径/鉴权/字段保留/超时/重定向/key 不泄漏。
  6. 过滤上游响应头：只转发安全头（如 content-type、cache-control），移除 Set-Cookie 与 Connection/Transfer-Encoding/Upgrade/Keep-Alive/Trailer 等 hop-by-hop 头。
- 行为验收：
  - Responses adapter 请求命中 {base_url}/responses，Chat 命中 /chat/completions。
  - 请求中的 tools、input_image、reasoning 与未知字段在发送后仍存在；仅 model 被替换。
  - 跨 host 302 被拒绝；超时返回可分类错误；错误不含上游 key。
  - probe 成功/失败返回状态与延迟，不记录 key。
  - 上游响应的 Set-Cookie 与 hop-by-hop 头不出现在客户端响应中，content-type 正常保留。
- 自动化验收（argv）：`go test ./internal/upstream`
- 风险：路径拼接错误、重定向泄漏 Authorization、超时设置不当。
- 风险缓解：base URL 规范校验；CheckRedirect 限制；字段级 passthrough 测试；超时/错误分类测试
- 依据：上游 adapter 是数据面唯一出站边界；先保证原生直通不丢字段，后续调度/流式复用。

## 任务 T19：候选调度、优先级、健康熔断与并发槽位
- 状态：ready
- 覆盖需求：R-005、R-006
- 文件：`internal/routing/scheduler.go`、`internal/routing/scheduler_test.go`、`internal/routing/limiter.go`、`internal/routing/limiter_test.go`
- 依赖：T09、T18
- 做法：
  1. 实现候选查询：client_model + 入站协议、enabled、channel enabled、circuit closed、容量可用。
  2. 实现排序：route.priority ASC、channel.priority ASC、last_used_at ASC、id ASC；无权重/随机；获得槽位后调用 store.TouchLastUsed。
  3. 实现每渠道信号量（chan struct{}）与 TryAcquire/Release；全部满载时 2s 等待，25ms 轮询，超时返回 ErrChannelCapacity。
  4. 实现内存熔断：401/403 打开 5m，429 打开 10s，5xx 打开 20s，成功重置；记录 last_error。
  5. 测试过滤/排序/并发不超限/等待 429/槽位释放/协议不匹配不选/ last_used 更新。
- 行为验收：
  - 禁用/channel disabled/circuit open/协议不匹配的渠道不被选中。
  - 按 route.priority 再 channel.priority 再 last_used_at 排序；TouchLastUsed 后顺序变化。
  - 并发 N 个请求不超过 max_concurrency；流式取消后槽位可再次获取。
  - 全部满载时等待约 2s 后返回 429 channel_capacity_exhausted；有槽位释放则请求继续。
- 自动化验收（argv）：`go test ./internal/routing`
- 风险：并发竞态、槽位泄漏、熔断过于激进、last_used 写入增加锁竞争。
- 风险缓解：信号量 + defer Release；表驱动排序测试；并发测试；熔断窗口可配置；TouchLastUsed 短 UPDATE
- 依据：调度与容量是稳定性的核心；last_used_at 持久化后排序有明确落点。

## 任务 T20：非流式失败切换、重试策略与 attempt 记录
- 状态：ready
- 覆盖需求：R-005、R-017
- 文件：`internal/gateway/proxy.go`、`internal/gateway/proxy_test.go`、`internal/gateway/attempt.go`、`internal/gateway/attempt_test.go`
- 依赖：T14、T17、T18、T19
- 做法：
  1. 实现非流式请求编排：取候选、TryAcquire、构建上游请求、发送、分类状态、释放、排除失败渠道。
  2. 重试网络/401/403/404/408/429/5xx/529；不重试 400/413/415/422；最多 3 个不同渠道，不重同渠道。
  3. 按入站协议构造错误 envelope；上游 401/403→502，429→429，5xx→502/503，超时→504；无兼容渠道返回 404 model_not_available 且上游调用 0 次。
  4. 每次 attempt 写 channel、status、latency、error、semantic_committed；最终写 request_log。
  5. 测试 503→200、401→200、400 不重试、全部失败、无映射/协议不匹配 0 上游调用、key 不泄漏。
- 行为验收：
  - 首个渠道 503、第二个 200 时客户端得 200，attempt 记录 2 次。
  - 首个渠道 400 时不尝试第二个，返回 400；首个 401 时尝试下一个。
  - 无启用映射或只有 openai_chat 渠道收到 Responses 时返回 404 model_not_available，上游调用次数为 0。
  - 所有渠道失败返回最后一次归一化错误，不含上游 key/URL；attempts 顺序/渠道/状态与真实请求一致。
- 自动化验收（argv）：`go test -count=1 ./internal/gateway`
- 风险：错误分类错误导致重复/不必要切换；重试放大成本；协议不匹配误调用上游。
- 风险缓解：状态码白名单/黑名单测试；最多 3 渠道；每次尝试日志；协议过滤测试；错误脱敏测试
- 依据：非流式路径先验证调度/重试/错误转换，再复用到流式；显式验收 model_not_available。

## 任务 T21：SSE 解析、首内容提交门控与提交后错误
- 状态：ready
- 覆盖需求：R-007、R-008、R-010
- 文件：`internal/gateway/stream.go`、`internal/gateway/stream_test.go`、`internal/upstream/sse.go`、`internal/upstream/sse_test.go`
- 依赖：T18、T20
- 做法：
  1. 实现 SSE 解析：保留 event/id/data/注释原始字节，支持多行 data、CRLF/LF；单事件大小上限；流空闲超时。
  2. 实现语义分类：Responses 的 response.output_text.delta/function_call_arguments.delta/completed/incomplete；Chat 的 choices.delta.content/tool_calls/reasoning_content/finish_reason/[DONE]。
  3. 实现缓冲：首语义内容前不写出；错误/EOF 前可换渠道；提交后写缓冲并 flush，后续只转发。
  4. 实现提交后错误：Responses 只发一次完整 response.failed（type、sequence_number、response.id/object/created_at/model/status/output/error）；Chat 发 error + [DONE]；不发起第二个上游。
  5. 测试 503→SSE、首内容前断流重试、首内容后断流终止、response.failed 字段契约、客户端取消。
- 行为验收：
  - 流开始后上游首个事件是 error/断开时，客户端未收到语义内容，网关可换渠道。
  - 首个文本/tool delta 后上游断开，Responses 客户端只收到一次 response.failed，且含 sequence_number 与 response.created_at，attempts 中无第二个渠道。
  - Chat 提交后错误收到 error 对象 + [DONE]，HTTP 200。
  - 客户端断开时上游 context 取消，attempt 标记取消，不重试。
- 自动化验收（argv）：`go test -v ./internal/gateway`
- 风险：事件分类误判、缓冲内存过大、response.failed 字段缺失被严格客户端拒绝。
- 风险缓解：协议帧分类表驱动测试；事件/缓冲大小上限；完整 response.failed 契约测试；提交后不重试断言
- 依据：流式行为是 Codex/网关稳定性的核心；必须锁定“能否重试”和严格终止事件字段。

## 任务 T22：调试 API 与管理端调试页
- 状态：ready
- 覆盖需求：R-003、R-014、R-017
- 文件：`internal/adminapi/debug.go`、`internal/adminapi/debug_test.go`、`web/src/features/debug/DebugPage.tsx`、`web/src/features/debug/DebugPage.test.tsx`、`web/scripts/test-debug.mjs`
- 依赖：T07、T10、T11、T14、T18
- 做法：
  1. 实现 POST /admin/api/debug/channels/{id}/test，body 可选 model；选择 route/channel，调用 upstream probe，返回 status/latency/脱敏错误，更新渠道 health。
  2. 实现 POST /admin/api/debug/models/{model}/test（或同一端点带 model）使用第一个候选渠道发最小请求。
  3. 实现调试页：选择渠道/模型，运行测试，显示状态、延迟、错误、最近请求链接；提示会消耗上游 tokens。
  4. 新增 web/scripts/test-debug.mjs：用 Node spawnSync（无 shell）依次运行 repo 根的 go test -cover ./internal/adminapi 和 web 的 vitest run src/features/debug/DebugPage.test.tsx；任一失败则非零退出。
  5. 测试成功/失败/超时/错误脱敏，并用 npm run test:debug 同时验证前后端。
  6. 保留 npm run test:debug 为唯一验收：它是 package.json 中的 npm 脚本，Node spawnSync 直接启动 go/npm、不经过 shell，属于 strict 白名单内的 argv 命令；不拆分任务或验收。
- 行为验收：
  - 渠道测试成功显示 200 与延迟；失败显示脱敏错误，不含 key。
  - 模型测试只使用该模型已启用的映射渠道；无映射时返回明确错误。
  - 测试结果更新渠道 last_health_status/latency/at；错误详情可跳转请求页。
  - npm run test:debug 同时通过 internal/adminapi 的 Go 测试和 DebugPage 的 Vitest。
- 自动化验收（argv）：`npm run test:debug`
- 风险：调试请求会消耗上游额度；错误可能泄露上游信息；跨栈测试脚本复杂。
- 风险缓解：UI 明确提示消耗 tokens；最小 probe payload；错误统一脱敏；Node spawnSync 无 shell；测试断言无 key
- 依据：需求要求渠道连通性/模型测试和错误详情；T22 的单一 argv 同时覆盖后端 debug API 与前端调试页。npm run test:debug 是项目 npm 脚本，Node spawnSync 无 shell，属于 strict 白名单。

## 任务 T23：usage 提取与请求日志集成
- 状态：ready
- 覆盖需求：R-011、R-012
- 文件：`internal/gateway/observability.go`、`internal/gateway/observability_test.go`、`internal/observability/usage.go`、`internal/observability/usage_test.go`
- 依赖：T14、T20、T21
- 做法：
  1. 在非流式/流式路径解析 usage：Responses response.completed/failed 的 response.usage；Chat 非流式 usage 与流式 final chunk usage。
  2. 记录 request_log：status、http_status、error_code、stream、attempt_count、retry_count、ttft、latency、input/output/total token、usage_source、semantic_committed、response_bytes。
  3. 写 attempts（每次渠道尝试）；确保一次请求只写一条 request_log；客户端断开记录 499。
  4. 测试 Responses/Chat usage、缺失为 null、流式错误、attempts 数量。
- 行为验收：
  - Responses completed 带 usage 时 request_log token 与上游一致；缺失时 null。
  - Chat 流式 final usage 可用时记录；没有 usage 时不估算。
  - 一次请求只生成一条 request_log，attempt 数与实际渠道尝试一致。
  - 默认日志无 header/body/prompt；semantic_committed 正确。
- 自动化验收（argv）：`go test -cover ./internal/gateway`
- 风险：流式 usage 解析失败导致统计缺失；重复写日志。
- 风险缓解：协议级 usage 解析器；缺失 nullable；一次请求单 writer；测试 completed/failed/无 usage
- 依据：观测数据必须在真实请求路径中产生；先完成 usage 与 request_log 集成，再做捕获。

## 任务 T24：内存诊断环与按需 body 捕获后端
- 状态：ready
- 覆盖需求：R-012
- 文件：`internal/observability/capture.go`、`internal/observability/capture_test.go`、`internal/adminapi/capture.go`、`internal/adminapi/capture_test.go`
- 依赖：T03、T14、T23
- 做法：
  1. 实现请求/响应 body 的诊断环：按 request_id 保存最近 5 分钟/50 请求/32MB；单侧上限 8MB，超出 truncated；默认不写 SQLite。
  2. 在请求结束时把 request/response 字节 tee 到诊断环；不存 header；流式保存转发后的原始 SSE 字节。
  3. 实现 GET /admin/api/requests/{id}/capture/status、POST /admin/api/requests/{id}/capture（确认风险）保存、GET 读取、DELETE 删除；status/read 响应加 Cache-Control:no-store、Pragma:no-cache。
  4. 保存时 AES-GCM 加密 request/response body，TTL 24h；后台每 10 分钟清理过期。
  5. 测试默认无捕获、环过期/驱逐、truncated、加密、TTL、删除、no-store。
- 行为验收：
  - 默认请求完成不产生 request_captures 行；诊断环 5 分钟后不可用。
  - 管理员保存后 DB 只有密文，读取解密内容与原始 body 一致（<=8MB）。
  - 超过 8MB 标记 truncated，捕获读取返回 truncated 标志；status/read 响应含 no-store。
  - 删除/过期后读取 404；不保存 Authorization/Cookie header。
- 自动化验收（argv）：`go test -count=1 ./internal/observability`
- 风险：内存环保存 prompt 造成隐私暴露；捕获表长期留存敏感数据；响应缓存。
- 风险缓解：默认不落库 + 5 分钟 TTL/32MB 上限；保存前风险确认；AES-GCM 加密 + 24h TTL；可删除 + 后台清理；不存 header；status/read no-store
- 依据：需求要求默认仅元数据、按需抓取；内存环+显式保存同时满足事后查看与不默认落库。

## 任务 T25：请求详情中的 body 捕获 UI
- 状态：ready
- 覆盖需求：R-012、R-014
- 文件：`web/src/features/requests/CaptureDialog.tsx`、`web/src/features/requests/CaptureDialog.test.tsx`、`web/src/features/requests/RequestDetail.tsx`
- 依赖：T16、T24
- 做法：
  1. 在请求详情中根据 capture/status 显示“抓取 body”按钮；不可用时说明已过期。
  2. 点击弹出风险提示：包含 prompt/response 敏感信息、24h 后自动删除；确认后调用保存接口。
  3. 保存后展示请求/响应 body（truncated 提示）、复制/下载、删除；不做 HTML 渲染。
  4. 测试可用/过期/确认/保存/删除/truncated。
- 行为验收：
  - 默认详情不显示 body；只有诊断环可用时显示抓取按钮。
  - 未确认风险不调用保存接口；确认后显示内容。
  - truncated 显示提示；删除后按钮回到不可用。
  - body 以纯文本展示，不执行 HTML/脚本。
- 自动化验收（argv）：`npm run test:capture`
- 风险：用户误存敏感 prompt；UI 渲染 XSS。
- 风险缓解：显式风险确认；纯文本/等宽展示；TTL + 删除；不 dangerouslySetInnerHTML
- 依据：捕获是敏感操作，必须由用户在请求详情中明确触发并看到风险。

## 任务 T26：Codex 模型发现与完整 ModelInfo manifest
- 状态：ready
- 覆盖需求：R-008、R-004
- 文件：`internal/gateway/codex_models.go`、`internal/gateway/codex_models_test.go`、`internal/gateway/router.go`
- 依赖：T17
- 做法：
  1. 实现 GET /v1/models 普通 OpenAI list；带 client_version 查询或 /backend-api/codex/models 返回 Codex manifest。
  2. manifest 从启用的 model_routes 去重生成完整 ModelInfo descriptor：slug/display_name/description/default_reasoning_level/supported_reasoning_levels/multi_agent_reasoning_effort/shell_type/visibility/supported_in_api/priority/additional_speed_tiers/service_tiers/default_service_tier/availability_nux/upgrade/model_messages/include_*_usage_instructions/supports_reasoning_summary_parameter/default_reasoning_summary/support_verbosity/default_verbosity/apply_patch_tool_type/web_search_tool_type/truncation_policy/supports_image_detail_original/supports_parallel_tool_calls/context_window/max_context_window/auto_compact_token_limit/comp_hash/effective_context_window_percent/experimental_supported_tools/input_modalities/supports_search_tool/use_responses_lite/node_repl_*/auto_review_model_override/model_specialty/tool_mode/multi_agent_version。
  3. context_window 使用 GATEWAY_CODEX_CONTEXT_WINDOW 默认 128000；input_modalities 默认 [text,image]；no-store；不访问上游模型目录。
  4. 测试普通 list、完整字段契约、空列表、禁用模型过滤、content-type/no-store。
- 行为验收：
  - 普通 GET /v1/models 返回 OpenAI 风格 object=list/data。
  - 带 client_version 返回 Codex manifest，slug 与启用的客户端模型一致。
  - manifest 每个模型含完整字段（含 supported_reasoning_levels、context_window、input_modalities、model_messages 等），无缺字段；空列表合法。
  - manifest 响应 no-store，且不包含上游模型名/渠道 key。
- 自动化验收（argv）：`go test -v -count=1 ./internal/gateway`
- 风险：Codex manifest 字段缺失/类型错误导致 0.164.0 解析失败或模型选择器为空。
- 风险缓解：按 sub2api/juhe-ai 的完整 descriptor 字段实现；字段级 contract 测试；真实 Codex E2E；可配置 context window；空列表返回合法结构
- 依据：Codex 模型选择器依赖完整 ModelInfo；最小 {slug,display_name} 已被参考实现注明不足。

## 任务 T27：真实 Codex CLI 的 Responses E2E
- 状态：ready
- 覆盖需求：R-008、R-010、R-013
- 文件：`e2e/codex_test.go`、`e2e/harness_test.go`、`e2e/fake_upstream_test.go`、`e2e/testdata/codex-0.164.0/config.toml.tmpl`
- 依赖：T18、T21、T26
- 做法：
  1. Go E2E 启动临时 SQLite、网关、假上游；写临时 CODEX_HOME/config.toml，base_url 指向网关 /v1，wire_api=responses，supports_websockets=false，requires_openai_auth=false，experimental_bearer_token 为测试 key；模板不设置 model_catalog_json，并使用不在 Codex 内置目录中的自定义模型名（由 harness seed 的 model_routes 提供）。
  2. E2E 文件是普通测试包，不加 build tag；go test ./e2e 即运行。preflight 断言 harness seed 中至少一个启用的 openai_responses 渠道；无则失败。真实上游可用性由 A-04/Q-UPSTREAM-NATIVE 覆盖，不在 E2E 内探测外网。运行真实 codex exec --json --skip-git-repo-check --ephemeral -C <temp> --sandbox read-only；断言 codex --version 为 0.164.x，否则失败。
  3. 子测试覆盖：纯文本、-i 图片输入、两轮 tool calling（假上游返回工具调用，Codex 回传 tool output，再返回最终文本）、模型发现、提交后断流（网关发完整 response.failed，不换渠道）。
  4. 记录网关收到的 method/path/headers，断言命中 /v1/responses 与 /v1/models，未出现 WebSocket 升级；若出现 compact/其他路径，测试失败并记录。
  5. 假上游不访问外网；超时 120s。
- 行为验收：
  - codex exec 文本请求成功，输出包含假上游指定的最终文本。
  - 带 -i 的请求在假上游 body 中出现 input_image。
  - tool calling 两轮完成：第一次返回工具调用，第二次收到 tool output 并返回最终文本。
  - 模型发现请求返回完整 manifest；提交后断流只出现 response.failed；未观察到 WebSocket；harness seed 缺少 openai_responses 渠道时 preflight 失败；codex 版本不是 0.164.x 时失败而不是 skip；自定义模型名触发 /v1/models 请求，未被 model_catalog_json 绕过。
- 自动化验收（argv）：`go test ./e2e`
- 风险：Codex 版本漂移、Windows 子进程/端口、模型发现/工具调用形状不兼容、harness seed 缺少 openai_responses 渠道。
- 风险缓解：固定 0.164.x 并在不匹配时失败；临时 CODEX_HOME 隔离用户配置；假上游本地端口 + 120s 超时；E2E 记录异常 endpoint；preflight 检查 harness seed 的 openai_responses 渠道；普通测试包，go test ./e2e 直接运行；模板不设置 model_catalog_json 且使用自定义模型名
- 依据：需求要求 Codex 实际接入验收；模拟客户端不足以证明请求形状、完整 manifest 和提交后终止事件兼容。

## 任务 T28：ZCode 入站协议取证与适配
- 状态：needs-info
- 覆盖需求：R-009
- 文件：`docs/protocols/zcode-capture.md`、`internal/gateway/zcode.go`、`internal/upstream/zcode.go`、`e2e/zcode_test.go`
- 依赖：T17、T18、T21
- 做法：
  1. 第一版不实现本任务；以下为 MVP+1 范围，仅在取得协议证据后启动。
  2. 取得 ZCode 版本、安装包/可执行文件和真实请求抓包；记录 endpoint、method、鉴权头、body、SSE 事件、图片与 tool 字段。
  3. 核对 ZCode 是否使用 OpenAI Chat、Responses、Anthropic Messages 或私有协议；与现有 adapter 边界对齐。
  4. 仅在证据确认后实现入站/上游 adapter；不猜测字段，不伪造动态签名/设备字段。
  5. 增加真实 ZCode E2E，覆盖文本、图片、tool calling、流式终止；记录版本。
  6. 若证据仍不足，保持 needs-info，不进入实现。
- 行为验收：
  - （未指定）
- 自动化验收：（不可执行）
- 风险：ZCode 协议未知，任何猜测都会静默丢图片/tool/流式字段；ZCode 动态签名字段可能无法伪造。
- 风险缓解：先抓包/官方协议，再实现；版本固定并记录；真实 E2E 覆盖四类能力；无证据不写 adapter
- 依据：用户已批准 ZCode 延后至 MVP+1；四个参考仓库未发现 ZCode 入站协议证据，juhe-ai 的命中集中在出站身份/版本跟踪。T28 保持 needs-info，MVP+1 取证后再实现，不得猜测。

## 任务 T29：Linux/Docker 打包与可迁移数据目录
- 状态：ready
- 覆盖需求：R-013、R-016
- 文件：`Dockerfile`、`.dockerignore`、`docs/deploy.md`、`internal/server/embed_test.go`、`internal/server/dockerfile_test.go`
- 依赖：T05、T07、T10、T11、T12、T13、T15、T16、T22、T25
- 做法：
  1. 验收前置：在 web 目录执行 npm run build，生成真实的 internal/webassets/dist（含 hashed assets）；随后运行唯一 argv 验收，避免只验证 T05 占位页。
  2. 多阶段构建：Node 构建 web 并输出 internal/webassets/dist，Go 构建单二进制并嵌入；运行镜像含 ca-certificates、非 root 用户。
  3. 挂载 /data 卷并设置 ENV GATEWAY_DATA_DIR=/data；默认监听 0.0.0.0:8080；环境变量注入 master key/client key/admin password；不把 secret 写入镜像。
  4. docs/deploy.md 写明本机运行、Docker 运行、反向代理 TLS、备份/恢复、迁移失败处理；真实 docker build 为发布前人工验收。
  5. Go 测试验证嵌入 SPA 可服务、GATEWAY_DATA_DIR 可重定位；dockerfile_test 读取 Dockerfile 断言 USER 非 root、VOLUME /data、ENV GATEWAY_DATA_DIR=/data、EXPOSE 8080、无 secret 字面量；embed_test 断言 index.html 含 Vite hashed asset 引用；若仍是 T05 静态占位页（无 hashed asset 引用），测试以明确错误要求先执行 npm run build。
  6. 确保 T29 依赖全部 UI 任务，Docker 构建时 web 源码完整。
- 行为验收：
  - 嵌入 SPA 可服务；GATEWAY_DATA_DIR 指向临时目录时数据库/备份都落在该目录。
  - embed_test 断言 internal/webassets/dist/index.html 是 npm run build 的真实产物（引用 /assets/index-<hash>.js）；若仍是 T05 静态占位页（无 hashed asset 引用），测试以明确错误要求先执行 npm run build。
  - Dockerfile 契约测试断言非 root、VOLUME /data、ENV GATEWAY_DATA_DIR=/data、EXPOSE 8080，且不含 GATEWAY_MASTER_KEY/客户端 Key/管理员密码字面量。
  - 服务默认监听可在 127.0.0.1 与 0.0.0.0 间配置。
  - 文档中的环境变量与代码默认一致；真实 docker build 明确为人工验收，不声称已运行。
- 自动化验收（argv）：`go test -v ./internal/server`
- 风险：Windows 路径/文件权限在 Linux 失效；镜像泄露 secret；容器数据未持久化；Docker 构建未在 argv 中真实执行。嵌入资源可能仍是占位页。
- 风险缓解：路径使用 filepath.Join + GATEWAY_DATA_DIR；多阶段构建不复制 .env；文档挂载卷；非 root 运行；Dockerfile 静态契约测试；真实 docker build 发布前人工验收；验收前 npm run build，embed_test 断言真实构建资产而非占位页
- 依据：需求要求本机优先但可迁 Linux+Docker；唯一 embed 包和数据目录边界先固定，Docker 运行时验证明确为人工验收。

## 勘察缺口

- 无

## 不修清单

- 不 fork 或复制 sub2api/new-api/juhe-ai/claude-code-hub 的任何代码；只读参考协议与设计。
- 不做多租户、注册、支付、充值、返利、订阅、邀请、OAuth/SSO、Passkey、2FA、RBAC。
- 不做 embeddings、图片生成/编辑、音频转写/TTS、视频、rerank、Realtime。
- 不做 Gemini /v1beta 原生入口。
- 不做 Responses↔Chat↔Anthropic 跨协议转换；渠道必须原生支持入站协议。
- 不做 Codex Responses WebSocket；0.164.0 的 responses_websockets 已 removed，当前配置 supports_websockets=false。
- 不做 /v1/responses/compact 与 /alpha/search；若固定 Codex 版本 E2E 出现，另行立项。
- 不做 Redis、消息队列、微服务、多实例共享限流。
- 不做自动模型发现/自动映射。
- 不做完整费用账单、对账、实际扣费同步。
- 不做自动数据库回滚/down migration；只做迁移前备份与显式 restore。
- 不做上游 Key 自动轮换；MVP 要求主密钥稳定并离线备份。
- 不做 Key 揭示二次密码（需求明确要求点击直接显示）。
- 不做默认 body 落库；捕获必须管理员显式保存。
- 不做移动端、多语言、插件系统、暗色主题。
- 不在本计划 argv 验收中真实运行 docker build；只做 Go 层与 Dockerfile 静态契约，真实构建发布前人工验收。
- 不把 workbuddy2api-panel 当现有参考；当前工作区没有该目录。
- 第一版不实现 ZCode adapter 或入站协议；ZCode 经用户批准延后至 MVP+1，取得协议证据后再立项。

## 待确认

- [非阻塞] Q-ZCODE: ZCode 实际使用哪个入站 endpoint、method、鉴权头、body 和 SSE 事件？用户已批准延后至 MVP+1；MVP+1 启动前需提供 ZCode 版本、真实抓包或官方协议文档，T28 在此之前保持 needs-info。已记录 resolution：decision=延后到 MVP+1；evidence=“zcode版本我不知道，批准延后 其他你帮我做决定。你继续吧。”（任务：T28）
- [非阻塞] Q-CODEX-PIN: 是否确认以当前本机 codex-cli 0.164.0（wire_api=responses、supports_websockets=false）作为第一版验收版本？未来升级会重新评估 WS/compact。（任务：T26、T27）
- [非阻塞] Q-CODEX-COMPACT: 固定 Codex 版本若在长会话中实际请求 /v1/responses/compact，是否把 compact 透传提升为 MVP？当前证据未显示必需。（任务：T27）
- [非阻塞] Q-UPSTREAM-NATIVE: 目标中转站是否都提供原生 /v1/responses？T27 使用 harness seed 的 openai_responses 渠道；真实上游若只有 Chat，是否接受后续单独做 Responses→Chat 转换？（任务：T18、T27）
- [非阻塞] Q-CAPTURE: 按需 body 捕获采用“默认不落库 + 5 分钟内存诊断环 + 管理员显式保存”是否满足使用习惯？若要求任意历史请求事后抓取，需要重新设计缓冲窗口。（任务：T24、T25）
- [非阻塞] Q-WORKBUDDY: workbuddy2api-panel 不在工作区；是否接受仅以 sub2api/new-api 作为布局参考？（任务：T10、T12、T13、T15、T16、T22）
- [非阻塞] Q-PROJECT-LICENSE: 项目是保持个人私有，还是未来公开？公开前需要选择许可证并审计直接依赖；当前 LICENSES.md 只记录参考项目许可证。（任务：T01）
- [非阻塞] Q-FULL-POLICY: 渠道全部满载时采用“换渠道 → 等待 2s → 429”的策略是否确认？若希望更长排队，可调整 GATEWAY_CAPACITY_WAIT，但需重新验收客户端超时。（任务：T19）
- [非阻塞] Q-DOCKER-VERIFY: 真实 docker build/容器启动不在 strict argv 白名单内；是否接受 T29 用 Go 测试覆盖嵌入资源/GATEWAY_DATA_DIR/Dockerfile 契约，并在发布前人工执行 docker build？（任务：T29）

## 假设

- A-01：绿地项目，无历史数据迁移；参考项目数据不导入。
- A-02：单进程、单用户、SQLite 单写者可接受；未来多实例再评估。
- A-03：Codex 目标版本为当前环境的 0.164.0；wire_api=responses、supports_websockets=false。
- A-04：至少有一个上游原生 Responses 渠道供 Codex 使用；T27 preflight 检查 harness seed 的 openai_responses 渠道，真实上游由本条与 Q-UPSTREAM-NATIVE 覆盖，若只有 Chat 上游需先决定转换方案。
- A-05：body 捕获采用默认不落库 + 5 分钟内存诊断环 + 管理员显式保存，不预先武装抓取。
- A-06：渠道满载策略为优先换渠道、全满等待 2s、然后 429。
- A-07：workbuddy2api-panel 不在工作区，布局参考 sub2api/new-api。
- A-08：项目暂按个人私有使用，不决定公开许可证；公开前单独选许可证并做依赖审计。
- A-09：管理员密码首次从 GATEWAY_ADMIN_PASSWORD 创建，之后只在 UI 修改；不提供自动忘记密码重置。
- A-10：不自动发现模型；模型列表完全来自手工映射。
- A-11：管理端前端用 npm + Vite；本机 Node v24.18.0 / npm 11.16.0 可运行。
- A-12：不实现 Anthropic Messages，直到 ZCode 证据或明确需求出现；预留 adapter 边界。
- A-13：Codex manifest 的 context_window 默认 128000，可由 GATEWAY_CODEX_CONTEXT_WINDOW 覆盖；不按模型自动推断，避免过度承诺。
- A-14：真实 docker build 不在 strict argv 验收内，T29 以 Go 测试 + Dockerfile 契约测试 + 人工发布验收覆盖。

## 执行合同

仅修改合同 JSON 中的 approved 和 resolutions。回答阻塞问题后，如需将相关任务改为 ready，必须重新运行 task-plan changes 并复核。不要直接改 plan；内容变更会使 reviewedPlanSnapshot 失效。

<!-- TASK_RUN_CONTRACT_START -->
{
  "contractVersion": 2,
  "mode": "strict",
  "allowShell": false,
  "approvalRequired": true,
  "approved": true,
  "review": {
    "verdict": "approve",
    "issues": [],
    "missing": [],
    "scopeConcerns": [],
    "acceptanceConcerns": [],
    "summary": "增量评审通过：结构化 diff 仅列 T01 的 steps 与 behaviorAcceptance，files/modules/dependencies 均为 false，勘察闸门未触发；其余任务、需求、技术决策、Q-ZCODE 记录与 T28 的 needs-info 未见变化。新 T01 step 5 将禁用标识/路径扫描限定为 cmd/、internal/、web/src/、e2e/ 下的 *.go/*.ts/*.tsx/*.js，排除所有 *_test.go/*.test.ts/*.test.tsx/*.test.js 与 internal/compliance/licenses_test.go 自身，显式排除且不遍历 reference/、.zcode/、web/node_modules/、internal/webassets/dist/、LICENSES.md、docs/，并明确 LICENSES.md 中列出 sub2api/new-api/juhe-ai/claude-code-hub 不触发扫描；behaviorAcceptance 第 5 条逐项复述同一范围与排除项，保留“自动扫描不替代人工审计”，第 4 条把 step 5 已要求的 LICENSES.md/go.mod/go.sum 检查写入验收，未新增依赖或文件。只读核验：命令 `find reference -type f \\( -name '*.go' -o -name '*.ts' -o -name '*.tsx' -o -name '*.js' \\) | wc -l` 输出 12437；命令 `grep -o \"sub2api\\|new-api\\|juhe-ai\\|claude-code-hub\" .zcode/workflow-drafts/task-plan.dwf.ts | sort | uniq -c` 显示四个名字各 1 次；根目录只有 .zcode、docs、reference（ls -la），无 go.mod/业务源码，故未运行 T01 的 `go test ./internal/config ./internal/compliance`；`go version`、`node --version`、`npm --version`、`codex --version` 分别输出 go1.26.5 windows/amd64、v24.18.0、11.16.0、codex-cli 0.164.0，与计划引用一致。Q-ZCODE 记录与旧合同一致（docs/plans/api-gateway-plan.md:920、:977-980）；本次 changes 的逐字授权句“其他你帮我做决定。你继续吧。”包含在既有 evidence 中，未编造新决定；Q-ZCODE blocking=false，T28 保持 needs-info，未发现新的 blocking question 关联 ready 任务。T01 执行时 web/src/、e2e/ 尚不存在，扫描器实现应将缺失根按空处理并用 filepath 归一化排除路径（Windows）；这是实现细节，不影响本次修订批准。未修改任何文件；docs/plans/api-gateway-plan.md:289/:294 仍是修订前 T01 文本，批准后由工作流重写。"
  },
  "reviewedPlanSnapshot": "{\"architecture\":{\"boundaries\":[\"cmd/gateway：单进程入口与 serve/migrate/backup/restore/keygen/version 子命令；不承载业务规则。\",\"internal/config：环境变量解析、默认值、启动校验、secret 脱敏；包括 GATEWAY_CODEX_CONTEXT_WINDOW。\",\"internal/store：SQLite 打开（WAL、busy_timeout、foreign_keys、单写连接）、版本化迁移、备份/恢复、渠道/映射/日志/捕获仓储；channels.last_used_at 与删除渠道 FK 策略在 0001 定义。\",\"internal/secret：AES-256-GCM v1 信封、主密钥解析、上游 Key 脱敏、日志/错误脱敏。\",\"internal/adminauth：PBKDF2 密码、opaque session、登录限流；不依赖 gateway 数据面。\",\"internal/adminapi：管理 REST API、Origin/CSRF 校验、渠道/映射/统计/请求/调试/捕获端点；只依赖 store、adminauth、upstream probe、observability。\",\"internal/gateway：客户端鉴权、路由表、错误 envelope、请求编排、首内容提交门控；不直接访问 SQLite 细节。\",\"internal/routing：候选查询、过滤、排序、进程内每渠道信号量、有限等待、熔断状态；更新 channels.last_used_at；不依赖 adminapi。\",\"internal/upstream：HTTP client、Responses/Chat 原生 adapter、SSE 解析、超时、重定向限制、连通性探针；不做跨协议转换。\",\"internal/observability：请求日志、usage 提取、统计查询、诊断环形缓冲、捕获加密持久化与 TTL。\",\"internal/webassets：唯一 Go embed 包；`//go:embed dist/*` 只引用包目录内的 internal/webassets/dist；T05 放占位 index.html，T10 Vite 输出到该目录，T29 验收前用 npm run build 重新生成真实产物。\",\"web：React 19 + TypeScript + Vite + TanStack Router/Query + 纯 CSS；只通过 /admin/api 访问后端；Vite outDir=../internal/webassets/dist。\",\"e2e：真实 Codex CLI 跨进程测试；普通测试包，不加 build tag；ZCode 延后至 MVP+1，证据到位后再新增同层测试。\",\"依赖规则：adminapi → store/upstream/observability/adminauth；gateway → routing/upstream/observability/store；routing → store；upstream 不反向依赖 gateway/adminapi；server → webassets。\"],\"dataMigration\":[\"绿地项目，不从 juhe-ai/sub2api/new-api/claude-code-hub 迁移任何数据；渠道、Key、模型映射全部重新配置。\",\"初始迁移 0001 建立 schema_migrations、admin_credentials、admin_sessions、channels（含 last_used_at）、model_routes、request_logs、request_attempts、request_captures。\",\"迁移只前进、不自动回滚；每个 SQL 文件一个事务；schema_migrations 记录 version/name/checksum/applied_at；checksum 不匹配拒绝启动。\",\"每次迁移前用 VACUUM INTO 生成 GATEWAY_DATA_DIR/backups/pre-migrate-v<from>-<timestamp>.db；失败保留备份并停止启动。\",\"restore 子命令先校验备份可打开且 schema 版本兼容，创建 pre-restore 备份后再替换；文档要求先停进程并清理 -wal/-shm。\",\"SQLite 使用 WAL、busy_timeout=5000、foreign_keys=ON、synchronous=NORMAL、SetMaxOpenConns(1)；日志/捕获写入短事务，不占用流式连接。\",\"model_routes.channel_id FK ON DELETE CASCADE；request_logs/request_attempts.channel_id FK ON DELETE SET NULL，channel_name 快照保留；删除渠道不破坏历史统计。\",\"主密钥不随数据库自动轮换；MVP 要求 GATEWAY_MASTER_KEY 稳定并离线备份；v1 信封预留版本字段，轮换作为后续独立迁移。\"],\"overview\":\"单进程 Go 服务 + SQLite WAL + 内嵌 React 管理端。公开面为 /v1（Codex Responses、OpenAI Chat、模型列表/Codex manifest）和 /backend-api/codex 别名；管理面为 /admin + /admin/api；运维面为 /healthz 与 /readyz。请求流水线：客户端 Key 鉴权 → 入站协议识别 → 手工模型映射 → 候选渠道调度（优先级/健康/容量）→ 进程内并发槽位 → 原生上游 adapter → 首内容提交门控 → 请求日志/统计/诊断环。运行态并发与熔断状态放内存；渠道、映射、日志、捕获与迁移元数据放 SQLite。第一版不引入 Redis、消息队列、微服务或多实例共享状态。Codex 以 0.164.0 的 Responses HTTP SSE 为基准；ZCode 经用户批准延后至 MVP+1，第一版只保留 adapter 边界。前端构建产物输出到 internal/webassets/dist，由 internal/webassets 的唯一 embed 包嵌入，禁止从包目录外 ../web/dist 取文件。\",\"securityPrivacy\":[\"上游 Key 只在创建/更新时接收明文，立即 AES-256-GCM 加密；SQLite 仅存 ciphertext 和 mask；管理列表永不返回明文。\",\"主密钥来自 GATEWAY_MASTER_KEY（base64 32 字节），启动时校验；缺失/格式错误拒绝启动；错误信息不回显密钥值。\",\"Key 揭示接口要求有效 admin session，返回 no-store，不做二次密码；不写请求日志；前端仅在用户点击后显示。\",\"管理员密码 PBKDF2-HMAC-SHA256 600k 迭代、随机 salt；session token 只存 SHA-256；cookie HttpOnly/SameSite=Lax/Secure 可配；改密/登出撤销。\",\"所有管理写操作校验同源 Origin；管理响应 no-store；SPA 使用 CSP default-src 'self'，不使用 dangerouslySetInnerHTML。\",\"默认只记元数据、token、延迟、错误；不记 header、prompt、response body；诊断环仅在内存，5 分钟/50 请求/32MB 上限。\",\"按需保存 body 需管理员风险确认，使用同一 AES-GCM 加密，TTL 24h，可删除；超过 8MB 标记 truncated；不保存 Authorization/Cookie；status/read 响应 no-store。\",\"上游 base_url 只允许 http/https；HTTP client 拒绝跨 host 重定向；不转发客户端 Authorization/Cookie 到上游；响应头过滤 hop-by-hop 和 Set-Cookie。\",\"默认监听 127.0.0.1:8080；上服务器由 TLS 反向代理暴露；Docker 显式 0.0.0.0:8080 并挂载 /data；进程内不终止 TLS。\",\"SQLite 全部参数化查询；日志统一 Redact；错误响应不回显上游 base_url 查询串、key 或原始敏感 body。\"]},\"assumptions\":[\"A-01：绿地项目，无历史数据迁移；参考项目数据不导入。\",\"A-02：单进程、单用户、SQLite 单写者可接受；未来多实例再评估。\",\"A-03：Codex 目标版本为当前环境的 0.164.0；wire_api=responses、supports_websockets=false。\",\"A-04：至少有一个上游原生 Responses 渠道供 Codex 使用；T27 preflight 检查 harness seed 的 openai_responses 渠道，真实上游由本条与 Q-UPSTREAM-NATIVE 覆盖，若只有 Chat 上游需先决定转换方案。\",\"A-05：body 捕获采用默认不落库 + 5 分钟内存诊断环 + 管理员显式保存，不预先武装抓取。\",\"A-06：渠道满载策略为优先换渠道、全满等待 2s、然后 429。\",\"A-07：workbuddy2api-panel 不在工作区，布局参考 sub2api/new-api。\",\"A-08：项目暂按个人私有使用，不决定公开许可证；公开前单独选许可证并做依赖审计。\",\"A-09：管理员密码首次从 GATEWAY_ADMIN_PASSWORD 创建，之后只在 UI 修改；不提供自动忘记密码重置。\",\"A-10：不自动发现模型；模型列表完全来自手工映射。\",\"A-11：管理端前端用 npm + Vite；本机 Node v24.18.0 / npm 11.16.0 可运行。\",\"A-12：不实现 Anthropic Messages，直到 ZCode 证据或明确需求出现；预留 adapter 边界。\",\"A-13：Codex manifest 的 context_window 默认 128000，可由 GATEWAY_CODEX_CONTEXT_WINDOW 覆盖；不按模型自动推断，避免过度承诺。\",\"A-14：真实 docker build 不在 strict argv 验收内，T29 以 Go 测试 + Dockerfile 契约测试 + 人工发布验收覆盖。\"],\"goal\":\"在不复制参考仓库代码、不引入 Redis/多租户的前提下，交付一个可本机运行、可迁移 Linux/Docker 的个人 AI API 网关：单管理员 + 单客户端 Key，支持 Codex 的 Responses 文本/图片/工具/SSE、OpenAI Chat、手工模型映射、渠道优先级与并发、失败切换、观测统计、按需 body 捕获；ZCode 经用户批准延后至 MVP+1，第一版只保留 adapter 边界并等待协议证据。\",\"mode\":\"strict\",\"notDoing\":[\"不 fork 或复制 sub2api/new-api/juhe-ai/claude-code-hub 的任何代码；只读参考协议与设计。\",\"不做多租户、注册、支付、充值、返利、订阅、邀请、OAuth/SSO、Passkey、2FA、RBAC。\",\"不做 embeddings、图片生成/编辑、音频转写/TTS、视频、rerank、Realtime。\",\"不做 Gemini /v1beta 原生入口。\",\"不做 Responses↔Chat↔Anthropic 跨协议转换；渠道必须原生支持入站协议。\",\"不做 Codex Responses WebSocket；0.164.0 的 responses_websockets 已 removed，当前配置 supports_websockets=false。\",\"不做 /v1/responses/compact 与 /alpha/search；若固定 Codex 版本 E2E 出现，另行立项。\",\"不做 Redis、消息队列、微服务、多实例共享限流。\",\"不做自动模型发现/自动映射。\",\"不做完整费用账单、对账、实际扣费同步。\",\"不做自动数据库回滚/down migration；只做迁移前备份与显式 restore。\",\"不做上游 Key 自动轮换；MVP 要求主密钥稳定并离线备份。\",\"不做 Key 揭示二次密码（需求明确要求点击直接显示）。\",\"不做默认 body 落库；捕获必须管理员显式保存。\",\"不做移动端、多语言、插件系统、暗色主题。\",\"不在本计划 argv 验收中真实运行 docker build；只做 Go 层与 Dockerfile 静态契约，真实构建发布前人工验收。\",\"不把 workbuddy2api-panel 当现有参考；当前工作区没有该目录。\",\"第一版不实现 ZCode adapter 或入站协议；ZCode 经用户批准延后至 MVP+1，取得协议证据后再立项。\"],\"openQuestions\":[{\"blocking\":false,\"id\":\"Q-ZCODE\",\"question\":\"ZCode 实际使用哪个入站 endpoint、method、鉴权头、body 和 SSE 事件？用户已批准延后至 MVP+1；MVP+1 启动前需提供 ZCode 版本、真实抓包或官方协议文档，T28 在此之前保持 needs-info。已记录 resolution：decision=延后到 MVP+1；evidence=“zcode版本我不知道，批准延后 其他你帮我做决定。你继续吧。”\",\"relatedTaskIds\":[\"T28\"]},{\"blocking\":false,\"id\":\"Q-CODEX-PIN\",\"question\":\"是否确认以当前本机 codex-cli 0.164.0（wire_api=responses、supports_websockets=false）作为第一版验收版本？未来升级会重新评估 WS/compact。\",\"relatedTaskIds\":[\"T26\",\"T27\"]},{\"blocking\":false,\"id\":\"Q-CODEX-COMPACT\",\"question\":\"固定 Codex 版本若在长会话中实际请求 /v1/responses/compact，是否把 compact 透传提升为 MVP？当前证据未显示必需。\",\"relatedTaskIds\":[\"T27\"]},{\"blocking\":false,\"id\":\"Q-UPSTREAM-NATIVE\",\"question\":\"目标中转站是否都提供原生 /v1/responses？T27 使用 harness seed 的 openai_responses 渠道；真实上游若只有 Chat，是否接受后续单独做 Responses→Chat 转换？\",\"relatedTaskIds\":[\"T18\",\"T27\"]},{\"blocking\":false,\"id\":\"Q-CAPTURE\",\"question\":\"按需 body 捕获采用“默认不落库 + 5 分钟内存诊断环 + 管理员显式保存”是否满足使用习惯？若要求任意历史请求事后抓取，需要重新设计缓冲窗口。\",\"relatedTaskIds\":[\"T24\",\"T25\"]},{\"blocking\":false,\"id\":\"Q-WORKBUDDY\",\"question\":\"workbuddy2api-panel 不在工作区；是否接受仅以 sub2api/new-api 作为布局参考？\",\"relatedTaskIds\":[\"T10\",\"T12\",\"T13\",\"T15\",\"T16\",\"T22\"]},{\"blocking\":false,\"id\":\"Q-PROJECT-LICENSE\",\"question\":\"项目是保持个人私有，还是未来公开？公开前需要选择许可证并审计直接依赖；当前 LICENSES.md 只记录参考项目许可证。\",\"relatedTaskIds\":[\"T01\"]},{\"blocking\":false,\"id\":\"Q-FULL-POLICY\",\"question\":\"渠道全部满载时采用“换渠道 → 等待 2s → 429”的策略是否确认？若希望更长排队，可调整 GATEWAY_CAPACITY_WAIT，但需重新验收客户端超时。\",\"relatedTaskIds\":[\"T19\"]},{\"blocking\":false,\"id\":\"Q-DOCKER-VERIFY\",\"question\":\"真实 docker build/容器启动不在 strict argv 白名单内；是否接受 T29 用 Go 测试覆盖嵌入资源/GATEWAY_DATA_DIR/Dockerfile 契约，并在发布前人工执行 docker build？\",\"relatedTaskIds\":[\"T29\"]}],\"requirements\":[{\"acceptanceCriteria\":[\"客户端只凭单一 key 的 Authorization: Bearer 或 x-api-key 通过 /v1 鉴权；错误 key 返回 401，且不影响管理端 session。\",\"数据库没有 users/roles/groups/billing 表；管理端只有 admin_credentials 单行与 admin_sessions。\",\"管理前端不出现注册、支付、充值、返利入口。\"],\"id\":\"R-001\",\"statement\":\"网关只有一个管理员、一个统一客户端 API key；不提供注册、多租户、支付、充值和复杂权限。\"},{\"acceptanceCriteria\":[\"密码只以 PBKDF2-HMAC-SHA256 哈希存储；数据库与日志无明文。\",\"session cookie 为 HttpOnly/SameSite=Lax；登出、改密或显式撤销后旧 token 失效。\",\"跨源写请求被拒绝；同源管理写请求成功。\"],\"id\":\"R-002\",\"statement\":\"管理端使用单管理员密码和可撤销 session；支持登录、登出、改密和 CSRF 防护。\"},{\"acceptanceCriteria\":[\"channels.key_ciphertext 只含 AES-256-GCM 密文；列表 API 不返回 key。\",\"揭示接口返回完整明文并带 no-store；错误或篡改密文读取失败。\",\"base URL 只允许 http/https；并发上限 1..64；name 唯一。\"],\"id\":\"R-003\",\"statement\":\"渠道以 base URL、上游 key、协议、优先级、并发上限和启停状态管理；上游 key 加密存储，列表脱敏，点击显示明文且不要求二次密码。\"},{\"acceptanceCriteria\":[\"model_routes 可 CRUD；同一客户端模型+渠道唯一。\",\"禁用映射/渠道不进入候选；/v1/models 只列有启用映射的模型。\",\"映射不存在或只有不匹配协议渠道时返回 model_not_available，不自动创建、不调用上游。\"],\"id\":\"R-004\",\"statement\":\"模型到渠道的映射由管理员手工配置，包含模型名、渠道、上游模型、优先级、启停；不做自动发现。\"},{\"acceptanceCriteria\":[\"按 route.priority、channel.priority、last_used、id 排序；禁用/熔断/满载渠道被过滤。\",\"上游 503/429/网络错误会尝试下一个渠道；400 不重试；最多 3 个渠道，同一渠道不重试。\",\"每次尝试写入 request_attempts；最终错误按入站协议返回。\"],\"id\":\"R-005\",\"statement\":\"同一模型可对应多个渠道，按优先级、健康和容量调度；首 token 前失败切换到下一个渠道，重试有上限。\"},{\"acceptanceCriteria\":[\"并发计数不超过 max_concurrency，流式/非流式/取消路径都释放槽位。\",\"全部候选满载时最多等待 2s，然后返回 429 + code=channel_capacity_exhausted + Retry-After。\",\"不存在 Redis 依赖。\"],\"id\":\"R-006\",\"statement\":\"渠道有进程内并发上限；满载时优先切换其他渠道，全部满载时短暂排队后返回 429。\"},{\"acceptanceCriteria\":[\"首内容前上游断流/错误可换渠道；客户端未收到语义字节。\",\"首内容后错误只产生一次 response.failed（Responses）或 error+DONE（Chat），不发起第二个上游；response.failed 含 type、sequence_number、response.id/object/created_at/model/status/output/error。\",\"客户端断开取消上游并记录 499，不重试。\"],\"id\":\"R-007\",\"statement\":\"流式响应在首 token 前可切换；已输出语义内容后不得拼接第二个上游，必须按协议终止并让客户端重试。\"},{\"acceptanceCriteria\":[\"codex exec 配置 wire_api=responses、supports_websockets=false 后可完成文本、图片、工具调用。\",\"请求命中 /v1/responses；模型发现返回包含 supported_reasoning_levels/context_window/input_modalities 等完整字段的 Codex manifest；不出现 WebSocket 升级。\",\"真实 Codex E2E 在 codex-cli 0.164.x 下通过；版本不匹配时测试失败。\"],\"id\":\"R-008\",\"statement\":\"Codex 0.164.0 作为第一版核心客户端，使用 Responses HTTP SSE，支持文本、图片输入、tool calling、流式输出和完整 ModelInfo 模型发现，并通过真实 CLI E2E。\"},{\"acceptanceCriteria\":[\"第一版不注册 ZCode 入站 endpoint、不实现 ZCode adapter，只在 gateway/upstream 预留协议边界。\",\"MVP+1 启动前记录 ZCode 版本、endpoint、method、headers、body、SSE 事件、图片与工具字段，并以取证结果驱动实现。\",\"无证据时 T28 保持 needs-info，不以 Chat/Responses/Anthropic 猜测替代；ZCode 不阻塞第一版验收。\"],\"id\":\"R-009\",\"statement\":\"ZCode 延后至 MVP+1；第一版只保留 adapter 边界并等待协议证据（用户已批准延后）。\"},{\"acceptanceCriteria\":[\"Responses/Chat 请求的 tools、input_image、reasoning 等字段原样到达上游。\",\"未实现端点返回 404；不注册 embeddings/images/audio/video 路由。\",\"流式事件与终止事件按协议保留。\"],\"id\":\"R-010\",\"statement\":\"第一版只提供文本、SSE、tool calling、图片输入；不支持 embeddings、图片生成、音频、视频、rerank。\"},{\"acceptanceCriteria\":[\"request_logs 不含 header/body/prompt；默认无 request_captures 行。\",\"能从 Responses completed 与 Chat usage 提取 token；缺失时为空，不估算。\",\"统计 API/页面支持时间、模型、渠道筛选，展示请求数、token、平均/P95 延迟和错误率。\"],\"id\":\"R-011\",\"statement\":\"请求日志默认只保存元数据、token、延迟和错误；统计包含请求数、输入/输出 token、延迟、错误率和渠道维度。\"},{\"acceptanceCriteria\":[\"默认请求只写元数据；内存诊断环有 5 分钟/50 请求/32MB 上限。\",\"管理员确认后保存的 body 为 AES-GCM 密文，TTL 24h，可删除；不保存 header；读取响应带 no-store。\",\"超过 8MB 的单侧 body 标记 truncated，不截断流式转发。\"],\"id\":\"R-012\",\"statement\":\"完整请求/响应 body 仅在管理员按需保存；默认不落库，保存前提示风险。\"},{\"acceptanceCriteria\":[\"缺主密钥/客户端 key/非法监听地址启动失败；/readyz 在 DB 或迁移未就绪时 503。\",\"迁移有版本/checksum 与迁移前备份；迁移失败不改变原库；删除渠道不破坏历史日志。\",\"SIGTERM 后 readiness 503，30s 内完成或取消；上游超时/重试上限可配置且有默认值。\"],\"id\":\"R-013\",\"statement\":\"配置校验、数据库迁移、备份/恢复、优雅退出、健康检查、上游超时和重试上限必须可用。\"},{\"acceptanceCriteria\":[\"未登录访问重定向登录；登录后侧边栏显示 6 个受保护页；登出清除 session。\",\"渠道页初始只显示脱敏 key，点击后才显示完整 key；复制可用。\",\"请求页默认只显示元数据；抓取按钮仅在内存诊断环可用时出现。\",\"前端测试覆盖登录、渠道、映射、总览、请求、调试、设置；无支付/注册入口。\"],\"id\":\"R-014\",\"statement\":\"管理端使用 React/TypeScript，包含登录 + 6 个受保护页（共 7 页），布局完整但不堆砌多租户/支付。\"},{\"acceptanceCriteria\":[\"Responses 请求只选 openai_responses 渠道，Chat 只选 openai_chat 渠道。\",\"协议不匹配返回 model_not_available；不调用转换器，上游调用次数为 0。\",\"请求中的 tools/input_image/未知字段在直通后保留。\"],\"id\":\"R-015\",\"statement\":\"Provider Adapter 以原生协议为边界；渠道协议与入站协议不匹配时不调度，不做隐式跨协议转换。\"},{\"acceptanceCriteria\":[\"go.mod/go.sum 无 Redis/MQ/多租户依赖；package.json/package-lock 为 React/TS；SQLite 是唯一存储。\",\"单二进制通过 internal/webassets 嵌入前端；Dockerfile 使用非 root、/data 卷并可配置监听地址。\",\"LICENSES.md 记录参考项目与直接依赖许可证；仓库无参考项目代码片段，compliance 测试可验证。\"],\"id\":\"R-016\",\"statement\":\"技术栈固定 Go + React/TypeScript + SQLite；单进程、无 Redis/MQ/微服务；可本机运行并迁移 Linux/Docker；只做 clean-room，不复制不兼容代码。\"},{\"acceptanceCriteria\":[\"上游 401/403 → 502 upstream_auth_failed；429 → 429 upstream_rate_limited；5xx → 502/503；超时 → 504。\",\"OpenAI/Responses 返回 {error:{...}}；Anthropic（如实现）返回顶层 type=error。\",\"错误日志/响应不含 Authorization、x-api-key、上游 base_url 查询串。\"],\"id\":\"R-017\",\"statement\":\"上游错误归一化为入站协议对应的错误体，客户端不看到上游 key、URL 或原始敏感 body。\"}],\"summary\":\"已按独立评审修订。推荐路线仍为 clean-room 重写：sub2api 是 LGPL + Vue + PostgreSQL/Redis，new-api 是 AGPLv3 + 多租户/支付，juhe-ai 是 Apache-2.0 但 Vue + PG/Redis + 两个常驻进程 + maintenance CLI，claude-code-hub 是 MIT 但 Next.js + PG/Redis。目标技术栈固定为 Go 1.26 + React 19/TypeScript + SQLite 单文件，单进程、单管理员、单客户端 Key。计划文件已内联完整任务合同、独立部署章节；Go embed 改为 internal/webassets/dist；Codex manifest 改为完整 ModelInfo；response.failed 补齐严格字段；增加 channels.last_used_at 与删除渠道 FK 策略；T22 用 npm run test:debug 同时执行前后端测试。Codex 按本机 0.164.0 的 Responses HTTP SSE 实现并做真实 CLI E2E；ZCode 经用户批准延后至 MVP+1，第一版只保留 adapter 边界并等待协议证据，T28 保持 needs-info 但不再阻塞第一版。\",\"tasks\":[{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"./internal/config\",\"./internal/compliance\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"缺少 GATEWAY_MASTER_KEY、base64 不是 32 字节、GATEWAY_CLIENT_KEY 少于 24 字符或 GATEWAY_LISTEN_ADDR 非法时启动失败，错误不含密钥值。\",\"合法配置得到默认 127.0.0.1:8080 与数据目录；CODEX_CONTEXT_WINDOW 默认 128000。\",\"配置的 String()/日志输出不包含 master key、client key、admin password。\",\"LICENSES.md 含 sub2api LGPL-3.0-or-later、new-api AGPLv3、juhe-ai Apache-2.0、claude-code-hub MIT，并列出直接依赖许可证清单；compliance 测试读取 LICENSES.md、go.mod 与 go.sum，检查四个参考许可证、直接依赖许可证与禁用 Redis/MQ 依赖均通过。\",\"禁用标识/路径扫描仅覆盖 cmd/、internal/、web/src/、e2e/ 下的 *.go、*.ts、*.tsx、*.js 生产源码，排除所有 *_test.go、*.test.ts、*.test.tsx、*.test.js 及 internal/compliance/licenses_test.go 自身；扫描范围内不出现 reference/ 导入或参考项目专有标识；显式排除且不遍历 reference/、.zcode/、web/node_modules/、internal/webassets/dist/、LICENSES.md、docs/，扫描不因 LICENSES.md 列出 sub2api/new-api/juhe-ai/claude-code-hub 而命中；文件注明自动扫描不替代人工审计。\"],\"dependsOn\":[],\"files\":[\"go.mod\",\"go.sum\",\"cmd/gateway/main.go\",\"internal/config/config.go\",\"internal/config/config_test.go\",\"internal/compliance/licenses_test.go\",\"LICENSES.md\"],\"id\":\"T01\",\"rationale\":\"所有后续模块需要统一配置与 secret 约束；先把许可证与依赖基线固定，避免实现中误拷参考代码。\",\"requirementIds\":[\"R-013\",\"R-016\"],\"risk\":\"环境变量中的主密钥/客户端 Key 可能被错误信息或日志带出；弱 Key 会让加密形同虚设；许可证基线可能随依赖漂移。\",\"riskMitigation\":[\"错误信息只引用变量名\",\"Config 实现脱敏 String()\",\"主密钥强制 base64 32 字节\",\"compliance 测试同时读取 LICENSES.md、go.mod 与 go.sum 检查禁用依赖\",\"测试断言输出不含 secret\",\"禁用标识/路径扫描只能发现明显复制，自动扫描不替代人工审计\"],\"status\":\"ready\",\"steps\":[\"初始化 module（本地名 ai-router，Go 1.26.0），只加入后续任务确实使用的依赖。\",\"实现 Config：GATEWAY_LISTEN_ADDR 默认 127.0.0.1:8080、GATEWAY_DATA_DIR、GATEWAY_DB_PATH、GATEWAY_MASTER_KEY（base64 32 字节）、GATEWAY_CLIENT_KEY（>=24 字符）、GATEWAY_ADMIN_PASSWORD（首次启动）、GATEWAY_COOKIE_SECURE、GATEWAY_CODEX_CONTEXT_WINDOW、上游超时/重试/容量等待/捕获上限。\",\"实现启动校验与脱敏 String()/错误信息；缺失或非法值只引用变量名，不输出值；非法监听地址也失败。\",\"在 main 中分发 serve/version/keygen 子命令；serve 目前只完成配置加载与占位服务器。\",\"go mod tidy 生成 go.sum；LICENSES.md 增加直接依赖许可证清单并与 go.mod/go.sum 对齐；compliance 测试读取 LICENSES.md、go.mod 与 go.sum，检查四个参考许可证、直接依赖许可证清单与禁用 Redis/MQ 依赖。禁用标识/路径扫描只覆盖项目生产源码：cmd/、internal/、web/src/、e2e/ 下的 *.go、*.ts、*.tsx、*.js；排除所有 *_test.go、*.test.ts、*.test.tsx、*.test.js 以及 internal/compliance/licenses_test.go 自身；显式排除且不遍历 reference/、.zcode/、web/node_modules/、internal/webassets/dist/、LICENSES.md、docs/，扫描不因 LICENSES.md 列出 sub2api/new-api/juhe-ai/claude-code-hub 而命中。对扫描范围内的文件断言不 import reference/、不含参考项目专有标识；文件注明自动扫描不替代人工审计。\"],\"title\":\"建立 Go 模块、配置校验与依赖许可基线\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"./internal/store\",\"./cmd/gateway\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"新库迁移后 schema_migrations 有当前版本，所有表/索引存在；第二次 migrate 不重复执行。\",\"修改已应用迁移内容导致 checksum 不匹配时启动失败，原库 schema 不变。\",\"迁移前备份文件可打开，包含迁移前已插入的数据；restore 校验失败时拒绝覆盖。\",\"删除 channel 后 model_routes 级联删除，request_logs/request_attempts.channel_id 置 NULL 且 channel_name 保留；SQLite WAL/foreign_keys/单写连接生效。\",\"cmd/gateway 的 migrate/backup/restore 子命令在 main 中注册并调用 store 导出入口；store 层在 internal/store/migrate_test.go 以导出入口覆盖迁移/备份/恢复行为；go test ./cmd/gateway 仅做编译级验证，不执行子命令。\",\"迁移后的 sqlite_master 不含 users/roles/groups/billing 表。\",\"备份文件落在 GATEWAY_DATA_DIR/backups，不依赖进程 cwd 的 data/ 目录。\"],\"dependsOn\":[\"T01\"],\"files\":[\"internal/store/db.go\",\"internal/store/migrate.go\",\"internal/store/backup.go\",\"internal/store/migrate_test.go\",\"internal/store/migrations/0001_init.sql\",\"cmd/gateway/main.go\"],\"id\":\"T02\",\"rationale\":\"数据层是所有功能的基础；先固定可恢复迁移、备份和删除渠道的数据保留语义。\",\"requirementIds\":[\"R-003\",\"R-011\",\"R-012\",\"R-013\"],\"risk\":\"SQLite 在 Windows 文件锁、WAL 与备份并发下可能 busy 或产生不一致备份；迁移失败可能留下半成品；FK 策略错误会阻止删除或破坏历史。\",\"riskMitigation\":[\"单写连接 + busy_timeout\",\"迁移事务与 checksum\",\"VACUUM INTO 前完成写事务\",\"失败迁移测试验证 schema 不变\",\"FK ON DELETE SET NULL + channel_name 快照\",\"restore 前创建 pre-restore 备份\"],\"status\":\"ready\",\"steps\":[\"用 modernc.org/sqlite 打开数据库，设置 WAL、busy_timeout=5000、foreign_keys=ON、synchronous=NORMAL、SetMaxOpenConns(1)。\",\"实现 go:embed 迁移加载、schema_migrations 版本/checksum 记录、每个迁移一个事务、checksum 不匹配拒绝启动。\",\"在 0001_init.sql 建立全部表与索引；channels 含 last_used_at；model_routes.channel_id ON DELETE CASCADE；request_logs/request_attempts.channel_id ON DELETE SET NULL 并保留 channel_name。\",\"实现 VACUUM INTO 备份与 restore 校验；迁移前自动备份到 filepath.Join(GATEWAY_DATA_DIR, \\\"backups\\\", \\\"pre-migrate-v<from>-<timestamp>.db\\\")。\",\"测试 fresh migrate、重复 migrate、坏 checksum、迁移失败不改库、备份可重新打开、删除 channel 的 FK 行为。\",\"在 cmd/gateway/main.go 注册 migrate/backup/restore 子命令，只做参数解析与退出码处理，调用 internal/store 导出的迁移/备份/恢复入口；不在 cmd 层复制业务逻辑。\"],\"title\":\"SQLite 初始化、版本化迁移与迁移前备份\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"./internal/secret\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"同一明文加密两次得到不同 nonce/密文，Decrypt 均还原。\",\"错误主密钥或篡改密文时 Decrypt 失败，错误不含明文。\",\"列表可得到 sk-***abcd 形式 mask；Redact 后日志不含完整 key。\",\"v1 前缀/版本不匹配被拒绝。\"],\"dependsOn\":[\"T01\"],\"files\":[\"internal/secret/envelope.go\",\"internal/secret/envelope_test.go\",\"internal/secret/redact.go\",\"internal/secret/redact_test.go\"],\"id\":\"T03\",\"rationale\":\"渠道 Key 加密是硬性安全要求；先独立实现并用测试锁定格式，再接入渠道仓储。\",\"requirementIds\":[\"R-003\",\"R-013\"],\"risk\":\"自行实现加密容易在 nonce/AAD/错误处理上出错；主密钥丢失会导致所有上游 Key 不可解。\",\"riskMitigation\":[\"使用标准库 crypto/aes + cipher.NewGCM\",\"随机 nonce + AAD + 版本字段\",\"测试错误 key/篡改\",\"文档要求离线备份主密钥\"],\"status\":\"ready\",\"steps\":[\"解析 GATEWAY_MASTER_KEY 为 32 字节；实现 AES-256-GCM，12 字节随机 nonce，AAD 绑定用途，输出 v1:<base64url(nonce)>:<base64url(ciphertext)>。\",\"实现 Encrypt/Decrypt 与版本校验；错误不返回明文。\",\"实现 Key 脱敏：长 Key 保留前缀 3 + 后缀 4，其余为 *；短 Key 全部 *。\",\"实现 Redact(map/header/value)，覆盖 authorization、x-api-key、cookie、set-cookie、proxy-authorization。\",\"测试 roundtrip、错误 key、篡改、格式错误、脱敏输出。\"],\"title\":\"上游 Key AES-GCM 信封与脱敏\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"./internal/adminauth\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"正确密码登录成功；错误密码失败；数据库不含明文密码。\",\"session 过期或被撤销后校验失败；重新登录生成不同 token。\",\"第 6 次连续失败被限流；成功登录后计数清零。\",\"改密后除当前 session 外的旧 session 全部失效。\"],\"dependsOn\":[\"T02\"],\"files\":[\"internal/adminauth/password.go\",\"internal/adminauth/session.go\",\"internal/adminauth/limiter.go\",\"internal/adminauth/password_test.go\",\"internal/adminauth/session_test.go\"],\"id\":\"T04\",\"rationale\":\"单管理员鉴权是管理端的第一道边界；先独立于 HTTP 实现，便于测试和复用。\",\"requirementIds\":[\"R-001\",\"R-002\"],\"risk\":\"密码哈希参数过低、session token 泄露、登录暴力破解。\",\"riskMitigation\":[\"PBKDF2 600k + 随机 salt\",\"session 只存哈希 + 绝对过期\",\"登录限流 + 常量时间比较\",\"改密撤销其他 session\"],\"status\":\"ready\",\"steps\":[\"用 crypto/pbkdf2（HMAC-SHA256，600000 迭代、16 字节 salt、32 字节 key）实现密码哈希与校验；编码为 pbkdf2-sha256$iter$salt$hash。\",\"首次启动无 admin 记录时从 GATEWAY_ADMIN_PASSWORD 创建；密码最短 12 字符；已有记录时忽略环境变量。\",\"实现 opaque session：32 字节随机 token，DB 存 SHA-256，12h 绝对过期；登录时生成新 token，登出/改密撤销。\",\"实现登录限流：同一来源 15 分钟最多 5 次失败，成功清除；使用常量时间比较避免用户枚举。\",\"测试哈希、错误密码、过期/撤销、session 轮换、限流。\"],\"title\":\"单管理员密码、session 与登录限流\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"./internal/server\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"/healthz 200；DB 关闭或迁移版本落后时 /readyz 503，迁移完成后 200。\",\"Shutdown 开始后 /readyz 返回 503；进行中的请求在 30s 内完成或被取消。\",\"GET /admin 返回嵌入的 SPA；未知 /admin 路径 fallback 到 index.html。\",\"服务器默认监听 127.0.0.1:8080，不自动暴露公网。\"],\"dependsOn\":[\"T02\",\"T04\"],\"files\":[\"internal/server/server.go\",\"internal/server/health.go\",\"internal/server/server_test.go\",\"internal/webassets/assets.go\",\"internal/webassets/dist/index.html\",\"cmd/gateway/main.go\"],\"id\":\"T05\",\"rationale\":\"健康与退出语义必须先固定；唯一 embed 包避免 Go embed 不能跨包目录的硬限制。\",\"requirementIds\":[\"R-013\",\"R-014\"],\"risk\":\"健康检查只返回静态 200 会误报；优雅退出等待过短会截断长流；embed 路径错误会导致构建失败。\",\"riskMitigation\":[\"readiness 检查 DB ping + 迁移版本\",\"退出前先置 503\",\"30s Shutdown + 超时取消\",\"embed 包只使用包内 dist\",\"测试覆盖未就绪/退出中/嵌入资源\"],\"status\":\"ready\",\"steps\":[\"用 stdlib net/http ServeMux 组装服务器；加入 request ID、panic recover、security headers、管理面 no-store、请求体大小限制。\",\"实现 /healthz 与 /readyz；ready 检查 DB ping、schema_migrations 当前版本、主密钥加载状态。\",\"实现 SIGINT/SIGTERM：停止接受新连接，readiness 立即 503，http.Server.Shutdown 30s；超时取消全局 context。\",\"在 internal/webassets 包内用 //go:embed dist/* 嵌入 internal/webassets/dist；SPA fallback 到 index.html；T05 先放占位 index.html，T10 构建覆盖。\",\"测试健康状态、迁移未就绪、退出中的 readiness、嵌入资源。\"],\"title\":\"HTTP 服务器骨架、健康检查与优雅退出\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"./internal/adminapi\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"登录成功设置 HttpOnly session cookie；GET session 返回管理员身份，logout 后返回 401。\",\"跨源 POST 返回 403；同源 POST 成功。\",\"错误密码返回 401，不泄露用户名/密码细节。\",\"改密后其他 session 失效，当前 session 继续可用。\"],\"dependsOn\":[\"T04\",\"T05\"],\"files\":[\"internal/adminapi/auth.go\",\"internal/adminapi/middleware.go\",\"internal/adminapi/auth_test.go\",\"internal/adminapi/middleware_test.go\",\"internal/server/server.go\"],\"id\":\"T06\",\"rationale\":\"管理 API 的所有后续端点都依赖 session 中间件；先完成登录边界。\",\"requirementIds\":[\"R-002\",\"R-014\"],\"risk\":\"CSRF、cookie 配置错误、session 固定。\",\"riskMitigation\":[\"SameSite=Lax + Origin 校验\",\"HttpOnly/Secure 可配\",\"登录后轮换 token\",\"改密撤销其他 session\"],\"status\":\"ready\",\"steps\":[\"实现 POST /admin/api/auth/login、POST /admin/api/auth/logout、GET /admin/api/auth/session、POST /admin/api/auth/password。\",\"设置 cookie：HttpOnly、SameSite=Lax、Path=/、Secure 可配、Max-Age 12h；响应 no-store。\",\"实现 admin session 中间件；实现 Origin 校验，所有 POST/PATCH/DELETE 要求同源。\",\"实现 session 列表/撤销接口（供设置页）；改密撤销其他 session。\",\"测试登录/登出/会话/CSRF/改密。\"],\"title\":\"管理端登录/session API 与 CSRF 防护\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"-count=1\",\"./internal/adminapi\",\"./internal/store\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"创建渠道后 SQLite 中 key_ciphertext 为密文，明文不可检索；列表/detail JSON 无 key。\",\"更新 key 后旧密文不可解、新 key 可解；不提供 key 时保持原 key。\",\"非法 base URL、protocol、并发上限返回 400。\",\"删除渠道后 model_routes 删除，历史 request_logs 保留 channel_name 且 channel_id 为 NULL；TouchLastUsed 更新 last_used_at。\",\"重复 channels.name 的创建或更新返回 409/400，数据库保留原记录。\",\"0001_init.sql 增加 name UNIQUE 后，internal/store 迁移测试通过（checksum 基线同步，fresh migrate 正常）。\"],\"dependsOn\":[\"T02\",\"T03\",\"T06\"],\"files\":[\"internal/store/channels.go\",\"internal/store/channels_test.go\",\"internal/adminapi/channels.go\",\"internal/adminapi/channels_test.go\",\"internal/store/migrations/0001_init.sql\",\"internal/store/migrate_test.go\"],\"id\":\"T07\",\"rationale\":\"渠道是路由和上游凭据的载体；先完成安全 CRUD 和删除语义，后续映射/调度/探针都依赖它。\",\"requirementIds\":[\"R-003\",\"R-004\"],\"risk\":\"渠道 Key 可能在列表/错误/日志中泄漏；删除渠道破坏历史统计；last_used 写入影响性能。\",\"riskMitigation\":[\"仓储层统一加密/脱敏\",\"list/detail 结构体不含 key\",\"FK SET NULL + channel_name 快照\",\"TouchLastUsed 短 UPDATE\",\"测试断言 JSON/日志无明文\"],\"status\":\"ready\",\"steps\":[\"实现 channels 仓储：Create/Get/List/Update/Delete/SetEnabled/UpdateHealth/TouchLastUsed，Key 只写 key_ciphertext + key_mask。\",\"实现 /admin/api/channels CRUD；list/detail 永不返回 key；更新时只有显式提供 key 才重新加密。\",\"校验 base_url（http/https、无 userinfo、无 fragment）、name 唯一、protocol 枚举、priority 0..1000、max_concurrency 1..64。\",\"删除渠道时依赖 FK：model_routes 级联，request_logs/attempts channel_id 置 NULL 但保留 channel_name。\",\"测试 CRUD、密文落库、列表脱敏、非法输入、删除级联/历史保留、TouchLastUsed。\",\"在 0001_init.sql 的 channels 定义上增加 name UNIQUE（项目尚未发布，直接修改 0001 并同步更新迁移 checksum 基线；不新增迁移文件），并把仓储层唯一冲突映射为 409/400；迁移测试由 internal/store/migrate_test.go 覆盖。\"],\"title\":\"渠道 CRUD、加密存储与配置校验\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"-v\",\"./internal/adminapi\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"揭示接口返回完整明文，且与创建时一致。\",\"响应头包含 no-store；列表接口仍只返回 mask。\",\"解密失败返回 500 且响应/日志不含密文或明文。\",\"无 admin session 返回 401。\"],\"dependsOn\":[\"T07\"],\"files\":[\"internal/adminapi/channel_secrets.go\",\"internal/adminapi/channel_secrets_test.go\",\"internal/server/server.go\"],\"id\":\"T08\",\"rationale\":\"需求明确要求点击直接显示、不做二次密码；用 session + no-store 控制风险。\",\"requirementIds\":[\"R-003\",\"R-014\"],\"risk\":\"揭示接口被滥用或浏览器缓存泄漏完整 Key。\",\"riskMitigation\":[\"admin session + no-store\",\"不写日志\",\"前端只在点击后调用\",\"测试缓存头/日志\"],\"status\":\"ready\",\"steps\":[\"实现 GET /admin/api/channels/{id}/key：校验 admin session，读取并解密 key，返回 {key, mask}。\",\"设置 Cache-Control:no-store、Pragma:no-cache；不写请求日志、不记录 key。\",\"前端点击才调用；错误统一 404/500，不区分“渠道不存在/解密失败”的敏感细节。\",\"测试揭示明文、列表仍脱敏、no-store、日志无 key。\"],\"title\":\"上游 Key 明文揭示接口\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"-v\",\"-count=1\",\"./internal/adminapi\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"创建映射成功；重复 client_model+channel_id 返回 409/400。\",\"禁用映射不出现在模型列表/候选查询中。\",\"删除映射后模型若已无启用路由则从 /admin/api/models 消失。\",\"非法 channel_id/priority 被拒绝。\"],\"dependsOn\":[\"T07\"],\"files\":[\"internal/store/routes.go\",\"internal/store/routes_test.go\",\"internal/adminapi/routes.go\",\"internal/adminapi/routes_test.go\"],\"id\":\"T09\",\"rationale\":\"手工模型映射是 MVP 的唯一路由来源；必须先把数据约束固定。\",\"requirementIds\":[\"R-004\",\"R-005\"],\"risk\":\"映射重复/漂移导致调度不可预测；禁用状态被忽略。\",\"riskMitigation\":[\"数据库唯一约束\",\"仓储查询统一过滤 enabled\",\"API 校验 channel 存在\",\"测试覆盖禁用/重复\"],\"status\":\"ready\",\"steps\":[\"实现 model_routes 仓储：Create/Update/Delete/List/ListByModelAndProtocol。\",\"实现 /admin/api/model-routes CRUD；字段 client_model、channel_id、upstream_model、priority、enabled。\",\"校验同一 client_model+channel_id 唯一；channel 必须存在；upstream_model 非空；priority 0..1000。\",\"提供 /admin/api/models：从启用的 model_routes 去重得到客户端模型列表。\",\"测试 CRUD、唯一约束、禁用过滤、模型列表。\"],\"title\":\"模型映射 CRUD 与调度校验\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"run\",\"test:app\"],\"program\":\"npm\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"未登录访问 / 显示登录页，不渲染管理导航。\",\"登录成功后侧边栏出现总览、渠道、模型映射、请求、调试、设置 6 项；登录页不在侧边栏，总计 7 页。\",\"登出后回到登录页，session 查询返回 401。\",\"package.json 声明 React/TypeScript/Vite/Vitest，并定义 build=vite build 脚本。\",\"路由与导航中不存在 /register、/payment、/topup、/rebate；直接访问这些路径显示 404 或重定向登录，不渲染管理页。\"],\"dependsOn\":[\"T06\"],\"files\":[\"web/package.json\",\"web/package-lock.json\",\"web/tsconfig.json\",\"web/vite.config.ts\",\"web/index.html\",\"web/src/main.tsx\",\"web/src/app/App.tsx\",\"web/src/app/App.test.tsx\",\"web/src/styles.css\"],\"id\":\"T10\",\"rationale\":\"管理端是需求中的独立交付物；先建立壳、设计系统和正确的构建输出目录。\",\"requirementIds\":[\"R-002\",\"R-014\"],\"risk\":\"从零实现设计系统可能简陋或过度；依赖版本漂移；Vite 输出目录错误导致 embed 占位页。\",\"riskMitigation\":[\"固定 package-lock\",\"先做少量高质量基础组件\",\"Vite outDir 指向 internal/webassets/dist\",\"组件测试覆盖关键页面状态\",\"不引入 UI 组件库\"],\"status\":\"ready\",\"steps\":[\"创建 React 19 + TypeScript 5.6 + Vite 5 + TanStack Router/Query + Vitest/RTL 工程；npm install 生成 package-lock。\",\"package.json 定义 build（vite build）、test 以及 test:app/test:settings/test:channels/test:mappings/test:dashboard/test:requests/test:capture/test:debug 脚本，分别运行对应 Vitest 文件；test:debug 交给 T22 的 Node 脚本。\",\"Vite build.outDir=../internal/webassets/dist，emptyOutDir=true；确保 T29 构建后 embed 包拿到真实 UI。\",\"实现设计 token、6 项侧边栏 + 独立登录页、顶栏、内容区、响应式折叠；loading/empty/error/toast；禁止 dangerouslySetInnerHTML。\",\"测试未登录重定向、登录成功、6 个导航项、登出、package.json 声明 React/TS/Vite/Vitest。\",\"路由表只注册登录页与 6 个受保护页，不提供 /register、/payment、/topup、/rebate 路由或导航入口。\"],\"title\":\"React 管理端骨架、登录页与设计系统\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"run\",\"test:settings\"],\"program\":\"npm\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"输入正确当前密码与新密码后改密成功，其他 session 被撤销。\",\"session 列表显示创建/最后使用/过期时间；撤销后该行消失。\",\"登出按钮清除 cookie 并跳转登录。\",\"401 时统一跳登录，不渲染受保护内容。\"],\"dependsOn\":[\"T06\",\"T10\"],\"files\":[\"web/src/features/settings/SettingsPage.tsx\",\"web/src/features/settings/SettingsPage.test.tsx\",\"web/src/lib/api.ts\"],\"id\":\"T11\",\"rationale\":\"需求要求管理员登录和 session 管理；设置页集中处理安全操作。\",\"requirementIds\":[\"R-002\",\"R-014\"],\"risk\":\"密码/session 管理 UI 可能泄露 token 或错误处理不一致。\",\"riskMitigation\":[\"只显示 session 元数据，不显示 token\",\"统一 API 401 处理\",\"测试撤销/改密\"],\"status\":\"ready\",\"steps\":[\"在 web/src/lib/api.ts 封装 fetch：credentials include、JSON、401 跳登录、no-store 处理。\",\"实现设置页：修改密码（当前密码/新密码/确认）、活跃 session 列表、撤销单个/全部、登出。\",\"密码规则前端提示 12 字符；错误显示服务端安全文案。\",\"测试改密、撤销 session、登出。\"],\"title\":\"管理端设置页：改密与会话撤销\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"run\",\"test:channels\"],\"program\":\"npm\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"列表初始不显示完整 key；点击后只显示当前渠道完整 key，关闭后清除。\",\"创建非法 URL/并发 0 时显示字段错误，不发请求。\",\"启停后列表状态更新；删除需二次确认。\",\"测试按钮失败时显示脱敏错误，不显示上游 key。\"],\"dependsOn\":[\"T07\",\"T08\",\"T10\",\"T11\",\"T22\"],\"files\":[\"web/src/features/channels/ChannelsPage.tsx\",\"web/src/features/channels/ChannelsPage.test.tsx\",\"web/src/features/channels/api.ts\",\"web/src/features/channels/types.ts\"],\"id\":\"T12\",\"rationale\":\"渠道页是管理端最核心的运维入口；Key 展示和测试按钮必须与后端安全/调试接口一起验收。\",\"requirementIds\":[\"R-003\",\"R-014\"],\"risk\":\"完整 Key 可能停留在 DOM/状态中，或被错误信息带回；测试按钮依赖 T22 的调试 API。\",\"riskMitigation\":[\"只在点击后请求\",\"关闭即清除状态\",\"错误统一脱敏\",\"T22 先完成调试 API\",\"测试断言初始 DOM 无完整 key\"],\"status\":\"ready\",\"steps\":[\"实现渠道列表：名称、base URL、协议、优先级、并发、启停、健康状态、mask。\",\"实现创建/编辑抽屉：base URL、协议、key（编辑时可留空）、优先级、并发、启用；提交后刷新。\",\"实现点击揭示：调用 key 接口，在只读输入框显示完整 key + 复制按钮；离开页面/关闭抽屉后清除。\",\"实现删除确认、启停开关、测试按钮（调用 T22 的调试 API，失败显示脱敏错误）。\",\"测试初始 mask、揭示、复制、创建校验、启停、测试按钮失败脱敏。\"],\"title\":\"管理端渠道页：脱敏 Key、揭示、复制与测试入口\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"run\",\"test:mappings\"],\"program\":\"npm\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"创建映射后表格出现新行，字段与输入一致。\",\"重复 client_model+channel 显示冲突错误，不新增行。\",\"禁用映射后状态展示为禁用，模型列表刷新后不再包含无启用路由的模型。\",\"无映射时显示空态并引导创建。\"],\"dependsOn\":[\"T09\",\"T10\",\"T11\"],\"files\":[\"web/src/features/mappings/MappingsPage.tsx\",\"web/src/features/mappings/MappingsPage.test.tsx\",\"web/src/features/mappings/api.ts\"],\"id\":\"T13\",\"rationale\":\"手工映射是 MVP 的路由来源；管理 UI 必须能可靠维护。\",\"requirementIds\":[\"R-004\",\"R-014\"],\"risk\":\"映射表单字段多，容易出现渠道/模型不一致。\",\"riskMitigation\":[\"后端唯一约束兜底\",\"表单联动校验\",\"测试冲突/空态\"],\"status\":\"ready\",\"steps\":[\"实现映射表格：客户端模型、渠道、上游模型、优先级、启停、更新时间。\",\"实现创建/编辑：模型下拉来自 /admin/api/models 与渠道列表；priority/enabled；重复映射提示。\",\"按客户端模型分组展示；支持筛选渠道/模型。\",\"测试创建、重复冲突、启停、空态。\"],\"title\":\"管理端模型映射页\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"./internal/observability\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"请求日志不包含 header/body/Authorization/prompt。\",\"统计 API 返回请求数、错误率、输入/输出 token、平均/P95 延迟，并可按模型/渠道过滤。\",\"usage 缺失时 token 字段为 null，统计 sum 不把 null 当 0 伪装成已知值。\",\"超过保留期的日志被清理，捕获表不受影响。\"],\"dependsOn\":[\"T02\",\"T06\"],\"files\":[\"internal/observability/record.go\",\"internal/observability/query.go\",\"internal/observability/observability_test.go\",\"internal/adminapi/observability.go\",\"internal/adminapi/observability_test.go\"],\"id\":\"T14\",\"rationale\":\"观测和管理端数据源依赖统一日志/统计查询；先完成存储与 API，再做页面。\",\"requirementIds\":[\"R-011\",\"R-013\"],\"risk\":\"SQLite 写放大/统计慢；日志误存 body 或 header。\",\"riskMitigation\":[\"短事务 + 单写连接\",\"只存元数据 + 索引\",\"nullable token\",\"测试断言无 body/header\"],\"status\":\"ready\",\"steps\":[\"实现请求日志/attempt 写入：request_id、protocol、endpoint、model、channel_name/id、status、error、stream、attempt_count、ttft、latency、nullable tokens、usage_source。\",\"实现查询：时间范围、模型、渠道、状态、分页；详情返回 attempts 与错误。\",\"实现统计：请求数、成功/错误率、输入/输出 token、平均/P95 延迟，按小时/模型/渠道聚合。\",\"实现 90 天日志保留清理任务；索引覆盖 started_at/channel/model/status。\",\"测试只写元数据、usage 缺失为空、筛选、错误率、P95。\"],\"title\":\"请求日志、统计查询与管理 API\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"run\",\"test:dashboard\"],\"program\":\"npm\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"默认 24h 数据渲染四张指标卡和趋势图。\",\"切换时间范围会带新参数请求；空数据展示空态而非 NaN。\",\"渠道/模型 Top 表可跳转请求页并带上筛选。\",\"统计错误显示可重试错误，不白屏。\"],\"dependsOn\":[\"T10\",\"T11\",\"T14\"],\"files\":[\"web/src/features/dashboard/DashboardPage.tsx\",\"web/src/features/dashboard/DashboardPage.test.tsx\",\"web/src/features/dashboard/charts.tsx\"],\"id\":\"T15\",\"rationale\":\"总览是管理端门面；先做轻量可维护的可视化，不引入图表依赖。\",\"requirementIds\":[\"R-011\",\"R-014\"],\"risk\":\"图表库引入过大；统计口径与后端不一致。\",\"riskMitigation\":[\"使用自绘 SVG，不加图表库\",\"API 契约测试/类型\",\"空态/错误态覆盖\"],\"status\":\"ready\",\"steps\":[\"实现时间范围选择（24h/7d/30d）、四张指标卡（请求数、token、平均/P95 延迟、错误率）。\",\"实现按小时趋势的轻量 SVG 折线/柱状图，以及按渠道/模型 Top 表。\",\"实现 loading/empty/error；错误显示重试。\",\"测试指标渲染、筛选请求、空态、错误态。\"],\"title\":\"管理端总览 Dashboard\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"run\",\"test:requests\"],\"program\":\"npm\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"列表按时间倒序，筛选后请求带对应 query 参数。\",\"详情显示每次 attempt 的渠道、状态、延迟与错误，不显示 body/header。\",\"usage 缺失显示 —；错误请求有可读错误摘要。\",\"无数据时显示空态，不是空白表格。\"],\"dependsOn\":[\"T10\",\"T11\",\"T14\"],\"files\":[\"web/src/features/requests/RequestsPage.tsx\",\"web/src/features/requests/RequestsPage.test.tsx\",\"web/src/features/requests/RequestDetail.tsx\"],\"id\":\"T16\",\"rationale\":\"请求观测是排障核心；先完成元数据视图，body 捕获作为独立任务。\",\"requirementIds\":[\"R-011\",\"R-014\"],\"risk\":\"日志量大导致表格卡顿；详情误展示敏感数据。\",\"riskMitigation\":[\"服务端分页 + 索引\",\"只展示元数据\",\"body 入口只走捕获接口\"],\"status\":\"ready\",\"steps\":[\"实现筛选：时间、模型、渠道、状态、错误码；分页表格显示请求数/token/延迟/渠道/状态。\",\"实现详情抽屉：元数据、usage、attempts 列表、错误 code/message、semantic_committed、capture 可用提示。\",\"默认不展示 body；捕获按钮由 T25 接入。\",\"测试筛选参数、分页、详情 attempts、空态/错误。\"],\"title\":\"管理端请求观测列表与详情\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"./internal/gateway\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"错误/缺失客户端 Key 返回 401；正确 Key 进入 handler。\",\"/v1/responses、/v1/chat/completions、/backend-api/codex/responses 可用；/v1/embeddings 返回 404。\",\"/v1/models 只列启用的客户端模型；带 client_version 走 manifest 分支；无映射/协议不匹配返回 model_not_available。\",\"错误响应为 {error:{message,type,code}}，不含上游 key。\"],\"dependsOn\":[\"T02\",\"T05\",\"T09\"],\"files\":[\"internal/gateway/auth.go\",\"internal/gateway/errors.go\",\"internal/gateway/router.go\",\"internal/gateway/models.go\",\"internal/gateway/router_test.go\"],\"id\":\"T17\",\"rationale\":\"客户端入口是所有数据面请求的第一层；先固定 surface、错误契约和 model_not_available 语义。\",\"requirementIds\":[\"R-001\",\"R-008\",\"R-015\",\"R-017\"],\"risk\":\"路由/错误形状不兼容 Codex；客户端 Key 时序/泄露；无映射错误码不一致。\",\"riskMitigation\":[\"endpoint 常量集中\",\"协议专属错误构造器\",\"constant-time compare\",\"测试 401/404/错误体\",\"model_not_available 统一\"],\"status\":\"ready\",\"steps\":[\"注册 POST /v1/responses、/backend-api/codex/responses、POST /v1/chat/completions、GET /v1/models、/v1/models/{model}、/backend-api/codex/models。\",\"实现单客户端 Key 鉴权：Authorization Bearer / x-api-key，SHA-256 + constant-time；返回统一 401。\",\"实现入站协议识别与 endpoint 常量；不支持路径 404，不注册 embeddings/images/audio/video。\",\"实现 OpenAI/Responses 错误 envelope；未知上游错误不泄露 key/URL；无路由/协议不匹配返回 404 model_not_available。\",\"实现普通模型列表与 Codex manifest 占位（T26 完成细节），从启用映射取模型。\",\"测试鉴权、路由、404、错误形状、模型列表、无映射/协议不匹配不调用上游。\"],\"title\":\"客户端鉴权、入站路由、错误 envelope 与模型列表\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"./internal/upstream\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"Responses adapter 请求命中 {base_url}/responses，Chat 命中 /chat/completions。\",\"请求中的 tools、input_image、reasoning 与未知字段在发送后仍存在；仅 model 被替换。\",\"跨 host 302 被拒绝；超时返回可分类错误；错误不含上游 key。\",\"probe 成功/失败返回状态与延迟，不记录 key。\",\"上游响应的 Set-Cookie 与 hop-by-hop 头不出现在客户端响应中，content-type 正常保留。\"],\"dependsOn\":[\"T01\",\"T07\"],\"files\":[\"internal/upstream/client.go\",\"internal/upstream/responses.go\",\"internal/upstream/chat.go\",\"internal/upstream/probe.go\",\"internal/upstream/client_test.go\"],\"id\":\"T18\",\"rationale\":\"上游 adapter 是数据面唯一出站边界；先保证原生直通不丢字段，后续调度/流式复用。\",\"requirementIds\":[\"R-008\",\"R-010\",\"R-015\"],\"risk\":\"路径拼接错误、重定向泄漏 Authorization、超时设置不当。\",\"riskMitigation\":[\"base URL 规范校验\",\"CheckRedirect 限制\",\"字段级 passthrough 测试\",\"超时/错误分类测试\"],\"status\":\"ready\",\"steps\":[\"实现 http.Transport 超时（连接 10s、TLS 10s、响应头 180s）、流式读空闲 300s、非流式总 300s、流式总 15m；拒绝跨 host 重定向。\",\"实现 base URL 校验与路径拼接；Responses → /responses，Chat → /chat/completions；Authorization: Bearer 上游 key。\",\"请求体用 map[string]json.RawMessage 仅替换 model；保留 tools/input_image/reasoning/未知字段；过滤客户端 Authorization/Cookie/hop-by-hop。\",\"实现 probe：最小 Responses/Chat 请求，返回状态/延迟/脱敏错误；更新渠道 health。\",\"测试路径/鉴权/字段保留/超时/重定向/key 不泄漏。\",\"过滤上游响应头：只转发安全头（如 content-type、cache-control），移除 Set-Cookie 与 Connection/Transfer-Encoding/Upgrade/Keep-Alive/Trailer 等 hop-by-hop 头。\"],\"title\":\"上游 HTTP client 与 Responses/Chat 原生 adapter\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"./internal/routing\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"禁用/channel disabled/circuit open/协议不匹配的渠道不被选中。\",\"按 route.priority 再 channel.priority 再 last_used_at 排序；TouchLastUsed 后顺序变化。\",\"并发 N 个请求不超过 max_concurrency；流式取消后槽位可再次获取。\",\"全部满载时等待约 2s 后返回 429 channel_capacity_exhausted；有槽位释放则请求继续。\"],\"dependsOn\":[\"T09\",\"T18\"],\"files\":[\"internal/routing/scheduler.go\",\"internal/routing/scheduler_test.go\",\"internal/routing/limiter.go\",\"internal/routing/limiter_test.go\"],\"id\":\"T19\",\"rationale\":\"调度与容量是稳定性的核心；last_used_at 持久化后排序有明确落点。\",\"requirementIds\":[\"R-005\",\"R-006\"],\"risk\":\"并发竞态、槽位泄漏、熔断过于激进、last_used 写入增加锁竞争。\",\"riskMitigation\":[\"信号量 + defer Release\",\"表驱动排序测试\",\"并发测试\",\"熔断窗口可配置\",\"TouchLastUsed 短 UPDATE\"],\"status\":\"ready\",\"steps\":[\"实现候选查询：client_model + 入站协议、enabled、channel enabled、circuit closed、容量可用。\",\"实现排序：route.priority ASC、channel.priority ASC、last_used_at ASC、id ASC；无权重/随机；获得槽位后调用 store.TouchLastUsed。\",\"实现每渠道信号量（chan struct{}）与 TryAcquire/Release；全部满载时 2s 等待，25ms 轮询，超时返回 ErrChannelCapacity。\",\"实现内存熔断：401/403 打开 5m，429 打开 10s，5xx 打开 20s，成功重置；记录 last_error。\",\"测试过滤/排序/并发不超限/等待 429/槽位释放/协议不匹配不选/ last_used 更新。\"],\"title\":\"候选调度、优先级、健康熔断与并发槽位\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"-count=1\",\"./internal/gateway\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"首个渠道 503、第二个 200 时客户端得 200，attempt 记录 2 次。\",\"首个渠道 400 时不尝试第二个，返回 400；首个 401 时尝试下一个。\",\"无启用映射或只有 openai_chat 渠道收到 Responses 时返回 404 model_not_available，上游调用次数为 0。\",\"所有渠道失败返回最后一次归一化错误，不含上游 key/URL；attempts 顺序/渠道/状态与真实请求一致。\"],\"dependsOn\":[\"T14\",\"T17\",\"T18\",\"T19\"],\"files\":[\"internal/gateway/proxy.go\",\"internal/gateway/proxy_test.go\",\"internal/gateway/attempt.go\",\"internal/gateway/attempt_test.go\"],\"id\":\"T20\",\"rationale\":\"非流式路径先验证调度/重试/错误转换，再复用到流式；显式验收 model_not_available。\",\"requirementIds\":[\"R-005\",\"R-017\"],\"risk\":\"错误分类错误导致重复/不必要切换；重试放大成本；协议不匹配误调用上游。\",\"riskMitigation\":[\"状态码白名单/黑名单测试\",\"最多 3 渠道\",\"每次尝试日志\",\"协议过滤测试\",\"错误脱敏测试\"],\"status\":\"ready\",\"steps\":[\"实现非流式请求编排：取候选、TryAcquire、构建上游请求、发送、分类状态、释放、排除失败渠道。\",\"重试网络/401/403/404/408/429/5xx/529；不重试 400/413/415/422；最多 3 个不同渠道，不重同渠道。\",\"按入站协议构造错误 envelope；上游 401/403→502，429→429，5xx→502/503，超时→504；无兼容渠道返回 404 model_not_available 且上游调用 0 次。\",\"每次 attempt 写 channel、status、latency、error、semantic_committed；最终写 request_log。\",\"测试 503→200、401→200、400 不重试、全部失败、无映射/协议不匹配 0 上游调用、key 不泄漏。\"],\"title\":\"非流式失败切换、重试策略与 attempt 记录\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"-v\",\"./internal/gateway\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"流开始后上游首个事件是 error/断开时，客户端未收到语义内容，网关可换渠道。\",\"首个文本/tool delta 后上游断开，Responses 客户端只收到一次 response.failed，且含 sequence_number 与 response.created_at，attempts 中无第二个渠道。\",\"Chat 提交后错误收到 error 对象 + [DONE]，HTTP 200。\",\"客户端断开时上游 context 取消，attempt 标记取消，不重试。\"],\"dependsOn\":[\"T18\",\"T20\"],\"files\":[\"internal/gateway/stream.go\",\"internal/gateway/stream_test.go\",\"internal/upstream/sse.go\",\"internal/upstream/sse_test.go\"],\"id\":\"T21\",\"rationale\":\"流式行为是 Codex/网关稳定性的核心；必须锁定“能否重试”和严格终止事件字段。\",\"requirementIds\":[\"R-007\",\"R-008\",\"R-010\"],\"risk\":\"事件分类误判、缓冲内存过大、response.failed 字段缺失被严格客户端拒绝。\",\"riskMitigation\":[\"协议帧分类表驱动测试\",\"事件/缓冲大小上限\",\"完整 response.failed 契约测试\",\"提交后不重试断言\"],\"status\":\"ready\",\"steps\":[\"实现 SSE 解析：保留 event/id/data/注释原始字节，支持多行 data、CRLF/LF；单事件大小上限；流空闲超时。\",\"实现语义分类：Responses 的 response.output_text.delta/function_call_arguments.delta/completed/incomplete；Chat 的 choices.delta.content/tool_calls/reasoning_content/finish_reason/[DONE]。\",\"实现缓冲：首语义内容前不写出；错误/EOF 前可换渠道；提交后写缓冲并 flush，后续只转发。\",\"实现提交后错误：Responses 只发一次完整 response.failed（type、sequence_number、response.id/object/created_at/model/status/output/error）；Chat 发 error + [DONE]；不发起第二个上游。\",\"测试 503→SSE、首内容前断流重试、首内容后断流终止、response.failed 字段契约、客户端取消。\"],\"title\":\"SSE 解析、首内容提交门控与提交后错误\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"run\",\"test:debug\"],\"program\":\"npm\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"渠道测试成功显示 200 与延迟；失败显示脱敏错误，不含 key。\",\"模型测试只使用该模型已启用的映射渠道；无映射时返回明确错误。\",\"测试结果更新渠道 last_health_status/latency/at；错误详情可跳转请求页。\",\"npm run test:debug 同时通过 internal/adminapi 的 Go 测试和 DebugPage 的 Vitest。\"],\"dependsOn\":[\"T07\",\"T10\",\"T11\",\"T14\",\"T18\"],\"files\":[\"internal/adminapi/debug.go\",\"internal/adminapi/debug_test.go\",\"web/src/features/debug/DebugPage.tsx\",\"web/src/features/debug/DebugPage.test.tsx\",\"web/scripts/test-debug.mjs\"],\"id\":\"T22\",\"rationale\":\"需求要求渠道连通性/模型测试和错误详情；T22 的单一 argv 同时覆盖后端 debug API 与前端调试页。npm run test:debug 是项目 npm 脚本，Node spawnSync 无 shell，属于 strict 白名单。\",\"requirementIds\":[\"R-003\",\"R-014\",\"R-017\"],\"risk\":\"调试请求会消耗上游额度；错误可能泄露上游信息；跨栈测试脚本复杂。\",\"riskMitigation\":[\"UI 明确提示消耗 tokens\",\"最小 probe payload\",\"错误统一脱敏\",\"Node spawnSync 无 shell\",\"测试断言无 key\"],\"status\":\"ready\",\"steps\":[\"实现 POST /admin/api/debug/channels/{id}/test，body 可选 model；选择 route/channel，调用 upstream probe，返回 status/latency/脱敏错误，更新渠道 health。\",\"实现 POST /admin/api/debug/models/{model}/test（或同一端点带 model）使用第一个候选渠道发最小请求。\",\"实现调试页：选择渠道/模型，运行测试，显示状态、延迟、错误、最近请求链接；提示会消耗上游 tokens。\",\"新增 web/scripts/test-debug.mjs：用 Node spawnSync（无 shell）依次运行 repo 根的 go test -cover ./internal/adminapi 和 web 的 vitest run src/features/debug/DebugPage.test.tsx；任一失败则非零退出。\",\"测试成功/失败/超时/错误脱敏，并用 npm run test:debug 同时验证前后端。\",\"保留 npm run test:debug 为唯一验收：它是 package.json 中的 npm 脚本，Node spawnSync 直接启动 go/npm、不经过 shell，属于 strict 白名单内的 argv 命令；不拆分任务或验收。\"],\"title\":\"调试 API 与管理端调试页\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"-cover\",\"./internal/gateway\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"Responses completed 带 usage 时 request_log token 与上游一致；缺失时 null。\",\"Chat 流式 final usage 可用时记录；没有 usage 时不估算。\",\"一次请求只生成一条 request_log，attempt 数与实际渠道尝试一致。\",\"默认日志无 header/body/prompt；semantic_committed 正确。\"],\"dependsOn\":[\"T14\",\"T20\",\"T21\"],\"files\":[\"internal/gateway/observability.go\",\"internal/gateway/observability_test.go\",\"internal/observability/usage.go\",\"internal/observability/usage_test.go\"],\"id\":\"T23\",\"rationale\":\"观测数据必须在真实请求路径中产生；先完成 usage 与 request_log 集成，再做捕获。\",\"requirementIds\":[\"R-011\",\"R-012\"],\"risk\":\"流式 usage 解析失败导致统计缺失；重复写日志。\",\"riskMitigation\":[\"协议级 usage 解析器\",\"缺失 nullable\",\"一次请求单 writer\",\"测试 completed/failed/无 usage\"],\"status\":\"ready\",\"steps\":[\"在非流式/流式路径解析 usage：Responses response.completed/failed 的 response.usage；Chat 非流式 usage 与流式 final chunk usage。\",\"记录 request_log：status、http_status、error_code、stream、attempt_count、retry_count、ttft、latency、input/output/total token、usage_source、semantic_committed、response_bytes。\",\"写 attempts（每次渠道尝试）；确保一次请求只写一条 request_log；客户端断开记录 499。\",\"测试 Responses/Chat usage、缺失为 null、流式错误、attempts 数量。\"],\"title\":\"usage 提取与请求日志集成\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"-count=1\",\"./internal/observability\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"默认请求完成不产生 request_captures 行；诊断环 5 分钟后不可用。\",\"管理员保存后 DB 只有密文，读取解密内容与原始 body 一致（<=8MB）。\",\"超过 8MB 标记 truncated，捕获读取返回 truncated 标志；status/read 响应含 no-store。\",\"删除/过期后读取 404；不保存 Authorization/Cookie header。\"],\"dependsOn\":[\"T03\",\"T14\",\"T23\"],\"files\":[\"internal/observability/capture.go\",\"internal/observability/capture_test.go\",\"internal/adminapi/capture.go\",\"internal/adminapi/capture_test.go\"],\"id\":\"T24\",\"rationale\":\"需求要求默认仅元数据、按需抓取；内存环+显式保存同时满足事后查看与不默认落库。\",\"requirementIds\":[\"R-012\"],\"risk\":\"内存环保存 prompt 造成隐私暴露；捕获表长期留存敏感数据；响应缓存。\",\"riskMitigation\":[\"默认不落库 + 5 分钟 TTL/32MB 上限\",\"保存前风险确认\",\"AES-GCM 加密 + 24h TTL\",\"可删除 + 后台清理\",\"不存 header\",\"status/read no-store\"],\"status\":\"ready\",\"steps\":[\"实现请求/响应 body 的诊断环：按 request_id 保存最近 5 分钟/50 请求/32MB；单侧上限 8MB，超出 truncated；默认不写 SQLite。\",\"在请求结束时把 request/response 字节 tee 到诊断环；不存 header；流式保存转发后的原始 SSE 字节。\",\"实现 GET /admin/api/requests/{id}/capture/status、POST /admin/api/requests/{id}/capture（确认风险）保存、GET 读取、DELETE 删除；status/read 响应加 Cache-Control:no-store、Pragma:no-cache。\",\"保存时 AES-GCM 加密 request/response body，TTL 24h；后台每 10 分钟清理过期。\",\"测试默认无捕获、环过期/驱逐、truncated、加密、TTL、删除、no-store。\"],\"title\":\"内存诊断环与按需 body 捕获后端\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"run\",\"test:capture\"],\"program\":\"npm\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"默认详情不显示 body；只有诊断环可用时显示抓取按钮。\",\"未确认风险不调用保存接口；确认后显示内容。\",\"truncated 显示提示；删除后按钮回到不可用。\",\"body 以纯文本展示，不执行 HTML/脚本。\"],\"dependsOn\":[\"T16\",\"T24\"],\"files\":[\"web/src/features/requests/CaptureDialog.tsx\",\"web/src/features/requests/CaptureDialog.test.tsx\",\"web/src/features/requests/RequestDetail.tsx\"],\"id\":\"T25\",\"rationale\":\"捕获是敏感操作，必须由用户在请求详情中明确触发并看到风险。\",\"requirementIds\":[\"R-012\",\"R-014\"],\"risk\":\"用户误存敏感 prompt；UI 渲染 XSS。\",\"riskMitigation\":[\"显式风险确认\",\"纯文本/等宽展示\",\"TTL + 删除\",\"不 dangerouslySetInnerHTML\"],\"status\":\"ready\",\"steps\":[\"在请求详情中根据 capture/status 显示“抓取 body”按钮；不可用时说明已过期。\",\"点击弹出风险提示：包含 prompt/response 敏感信息、24h 后自动删除；确认后调用保存接口。\",\"保存后展示请求/响应 body（truncated 提示）、复制/下载、删除；不做 HTML 渲染。\",\"测试可用/过期/确认/保存/删除/truncated。\"],\"title\":\"请求详情中的 body 捕获 UI\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"-v\",\"-count=1\",\"./internal/gateway\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"普通 GET /v1/models 返回 OpenAI 风格 object=list/data。\",\"带 client_version 返回 Codex manifest，slug 与启用的客户端模型一致。\",\"manifest 每个模型含完整字段（含 supported_reasoning_levels、context_window、input_modalities、model_messages 等），无缺字段；空列表合法。\",\"manifest 响应 no-store，且不包含上游模型名/渠道 key。\"],\"dependsOn\":[\"T17\"],\"files\":[\"internal/gateway/codex_models.go\",\"internal/gateway/codex_models_test.go\",\"internal/gateway/router.go\"],\"id\":\"T26\",\"rationale\":\"Codex 模型选择器依赖完整 ModelInfo；最小 {slug,display_name} 已被参考实现注明不足。\",\"requirementIds\":[\"R-008\",\"R-004\"],\"risk\":\"Codex manifest 字段缺失/类型错误导致 0.164.0 解析失败或模型选择器为空。\",\"riskMitigation\":[\"按 sub2api/juhe-ai 的完整 descriptor 字段实现\",\"字段级 contract 测试\",\"真实 Codex E2E\",\"可配置 context window\",\"空列表返回合法结构\"],\"status\":\"ready\",\"steps\":[\"实现 GET /v1/models 普通 OpenAI list；带 client_version 查询或 /backend-api/codex/models 返回 Codex manifest。\",\"manifest 从启用的 model_routes 去重生成完整 ModelInfo descriptor：slug/display_name/description/default_reasoning_level/supported_reasoning_levels/multi_agent_reasoning_effort/shell_type/visibility/supported_in_api/priority/additional_speed_tiers/service_tiers/default_service_tier/availability_nux/upgrade/model_messages/include_*_usage_instructions/supports_reasoning_summary_parameter/default_reasoning_summary/support_verbosity/default_verbosity/apply_patch_tool_type/web_search_tool_type/truncation_policy/supports_image_detail_original/supports_parallel_tool_calls/context_window/max_context_window/auto_compact_token_limit/comp_hash/effective_context_window_percent/experimental_supported_tools/input_modalities/supports_search_tool/use_responses_lite/node_repl_*/auto_review_model_override/model_specialty/tool_mode/multi_agent_version。\",\"context_window 使用 GATEWAY_CODEX_CONTEXT_WINDOW 默认 128000；input_modalities 默认 [text,image]；no-store；不访问上游模型目录。\",\"测试普通 list、完整字段契约、空列表、禁用模型过滤、content-type/no-store。\"],\"title\":\"Codex 模型发现与完整 ModelInfo manifest\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"./e2e\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"codex exec 文本请求成功，输出包含假上游指定的最终文本。\",\"带 -i 的请求在假上游 body 中出现 input_image。\",\"tool calling 两轮完成：第一次返回工具调用，第二次收到 tool output 并返回最终文本。\",\"模型发现请求返回完整 manifest；提交后断流只出现 response.failed；未观察到 WebSocket；harness seed 缺少 openai_responses 渠道时 preflight 失败；codex 版本不是 0.164.x 时失败而不是 skip；自定义模型名触发 /v1/models 请求，未被 model_catalog_json 绕过。\"],\"dependsOn\":[\"T18\",\"T21\",\"T26\"],\"files\":[\"e2e/codex_test.go\",\"e2e/harness_test.go\",\"e2e/fake_upstream_test.go\",\"e2e/testdata/codex-0.164.0/config.toml.tmpl\"],\"id\":\"T27\",\"rationale\":\"需求要求 Codex 实际接入验收；模拟客户端不足以证明请求形状、完整 manifest 和提交后终止事件兼容。\",\"requirementIds\":[\"R-008\",\"R-010\",\"R-013\"],\"risk\":\"Codex 版本漂移、Windows 子进程/端口、模型发现/工具调用形状不兼容、harness seed 缺少 openai_responses 渠道。\",\"riskMitigation\":[\"固定 0.164.x 并在不匹配时失败\",\"临时 CODEX_HOME 隔离用户配置\",\"假上游本地端口 + 120s 超时\",\"E2E 记录异常 endpoint\",\"preflight 检查 harness seed 的 openai_responses 渠道\",\"普通测试包，go test ./e2e 直接运行\",\"模板不设置 model_catalog_json 且使用自定义模型名\"],\"status\":\"ready\",\"steps\":[\"Go E2E 启动临时 SQLite、网关、假上游；写临时 CODEX_HOME/config.toml，base_url 指向网关 /v1，wire_api=responses，supports_websockets=false，requires_openai_auth=false，experimental_bearer_token 为测试 key；模板不设置 model_catalog_json，并使用不在 Codex 内置目录中的自定义模型名（由 harness seed 的 model_routes 提供）。\",\"E2E 文件是普通测试包，不加 build tag；go test ./e2e 即运行。preflight 断言 harness seed 中至少一个启用的 openai_responses 渠道；无则失败。真实上游可用性由 A-04/Q-UPSTREAM-NATIVE 覆盖，不在 E2E 内探测外网。运行真实 codex exec --json --skip-git-repo-check --ephemeral -C <temp> --sandbox read-only；断言 codex --version 为 0.164.x，否则失败。\",\"子测试覆盖：纯文本、-i 图片输入、两轮 tool calling（假上游返回工具调用，Codex 回传 tool output，再返回最终文本）、模型发现、提交后断流（网关发完整 response.failed，不换渠道）。\",\"记录网关收到的 method/path/headers，断言命中 /v1/responses 与 /v1/models，未出现 WebSocket 升级；若出现 compact/其他路径，测试失败并记录。\",\"假上游不访问外网；超时 120s。\"],\"title\":\"真实 Codex CLI 的 Responses E2E\"},{\"acceptance\":null,\"acceptanceCommand\":null,\"acceptanceKind\":\"none\",\"behaviorAcceptance\":[],\"dependsOn\":[\"T17\",\"T18\",\"T21\"],\"files\":[\"docs/protocols/zcode-capture.md\",\"internal/gateway/zcode.go\",\"internal/upstream/zcode.go\",\"e2e/zcode_test.go\"],\"id\":\"T28\",\"rationale\":\"用户已批准 ZCode 延后至 MVP+1；四个参考仓库未发现 ZCode 入站协议证据，juhe-ai 的命中集中在出站身份/版本跟踪。T28 保持 needs-info，MVP+1 取证后再实现，不得猜测。\",\"requirementIds\":[\"R-009\"],\"risk\":\"ZCode 协议未知，任何猜测都会静默丢图片/tool/流式字段；ZCode 动态签名字段可能无法伪造。\",\"riskMitigation\":[\"先抓包/官方协议，再实现\",\"版本固定并记录\",\"真实 E2E 覆盖四类能力\",\"无证据不写 adapter\"],\"status\":\"needs-info\",\"steps\":[\"第一版不实现本任务；以下为 MVP+1 范围，仅在取得协议证据后启动。\",\"取得 ZCode 版本、安装包/可执行文件和真实请求抓包；记录 endpoint、method、鉴权头、body、SSE 事件、图片与 tool 字段。\",\"核对 ZCode 是否使用 OpenAI Chat、Responses、Anthropic Messages 或私有协议；与现有 adapter 边界对齐。\",\"仅在证据确认后实现入站/上游 adapter；不猜测字段，不伪造动态签名/设备字段。\",\"增加真实 ZCode E2E，覆盖文本、图片、tool calling、流式终止；记录版本。\",\"若证据仍不足，保持 needs-info，不进入实现。\"],\"title\":\"ZCode 入站协议取证与适配\"},{\"acceptance\":null,\"acceptanceCommand\":{\"args\":[\"test\",\"-v\",\"./internal/server\"],\"program\":\"go\"},\"acceptanceKind\":\"argv\",\"behaviorAcceptance\":[\"嵌入 SPA 可服务；GATEWAY_DATA_DIR 指向临时目录时数据库/备份都落在该目录。\",\"embed_test 断言 internal/webassets/dist/index.html 是 npm run build 的真实产物（引用 /assets/index-<hash>.js）；若仍是 T05 静态占位页（无 hashed asset 引用），测试以明确错误要求先执行 npm run build。\",\"Dockerfile 契约测试断言非 root、VOLUME /data、ENV GATEWAY_DATA_DIR=/data、EXPOSE 8080，且不含 GATEWAY_MASTER_KEY/客户端 Key/管理员密码字面量。\",\"服务默认监听可在 127.0.0.1 与 0.0.0.0 间配置。\",\"文档中的环境变量与代码默认一致；真实 docker build 明确为人工验收，不声称已运行。\"],\"dependsOn\":[\"T05\",\"T07\",\"T10\",\"T11\",\"T12\",\"T13\",\"T15\",\"T16\",\"T22\",\"T25\"],\"files\":[\"Dockerfile\",\".dockerignore\",\"docs/deploy.md\",\"internal/server/embed_test.go\",\"internal/server/dockerfile_test.go\"],\"id\":\"T29\",\"rationale\":\"需求要求本机优先但可迁 Linux+Docker；唯一 embed 包和数据目录边界先固定，Docker 运行时验证明确为人工验收。\",\"requirementIds\":[\"R-013\",\"R-016\"],\"risk\":\"Windows 路径/文件权限在 Linux 失效；镜像泄露 secret；容器数据未持久化；Docker 构建未在 argv 中真实执行。嵌入资源可能仍是占位页。\",\"riskMitigation\":[\"路径使用 filepath.Join + GATEWAY_DATA_DIR\",\"多阶段构建不复制 .env\",\"文档挂载卷\",\"非 root 运行\",\"Dockerfile 静态契约测试\",\"真实 docker build 发布前人工验收\",\"验收前 npm run build，embed_test 断言真实构建资产而非占位页\"],\"status\":\"ready\",\"steps\":[\"验收前置：在 web 目录执行 npm run build，生成真实的 internal/webassets/dist（含 hashed assets）；随后运行唯一 argv 验收，避免只验证 T05 占位页。\",\"多阶段构建：Node 构建 web 并输出 internal/webassets/dist，Go 构建单二进制并嵌入；运行镜像含 ca-certificates、非 root 用户。\",\"挂载 /data 卷并设置 ENV GATEWAY_DATA_DIR=/data；默认监听 0.0.0.0:8080；环境变量注入 master key/client key/admin password；不把 secret 写入镜像。\",\"docs/deploy.md 写明本机运行、Docker 运行、反向代理 TLS、备份/恢复、迁移失败处理；真实 docker build 为发布前人工验收。\",\"Go 测试验证嵌入 SPA 可服务、GATEWAY_DATA_DIR 可重定位；dockerfile_test 读取 Dockerfile 断言 USER 非 root、VOLUME /data、ENV GATEWAY_DATA_DIR=/data、EXPOSE 8080、无 secret 字面量；embed_test 断言 index.html 含 Vite hashed asset 引用；若仍是 T05 静态占位页（无 hashed asset 引用），测试以明确错误要求先执行 npm run build。\",\"确保 T29 依赖全部 UI 任务，Docker 构建时 web 源码完整。\"],\"title\":\"Linux/Docker 打包与可迁移数据目录\"}],\"technicalDecisions\":[{\"alternatives\":[\"fork new-api（React+SQLite 最近，但 AGPL + 多租户/支付/任务插件）\",\"fork sub2api（LGPL + Vue + PostgreSQL/Redis + 70 万行）\",\"裁剪 juhe-ai（Apache，但 Vue + PG/Redis + 两个常驻进程 + maintenance CLI）\",\"fork claude-code-hub（MIT，但 Next.js + PG/Redis + 严格同格式）\"],\"area\":\"路线与许可证\",\"evidence\":[\"reference/sub2api/LICENSE:1 为 GNU LGPL v3；reference/sub2api/backend/go.mod:32/35 使用 lib/pq 与 Redis，前端为 Vue 3。\",\"reference/new-api/LICENSE:1 为 AGPLv3；reference/new-api/NOTICE:6-25 要求保留前端署名、原项目链接并标记修改。\",\"reference/juhe-ai/LICENSE:1 为 Apache-2.0；README.md:151 声明 SQLite 已退役，运行时为 PG+Redis，两个常驻进程 + maintenance CLI。\",\"reference/claude-code-hub/LICENSE:1 为 MIT；但运行时为 Next.js + Hono + PostgreSQL/Redis。\"],\"rationale\":\"四项参考的许可证与固定技术栈均不完全兼容；fork 的许可证义务、UI/DB 重写和长期 rebase 成本高于 clean-room。\",\"recommendation\":\"clean-room 重写，不 fork 任何参考仓库；只借鉴协议、交互和运维模式。\",\"risks\":[\"低估 Codex/管理端实现量\",\"误复制参考代码造成 AGPL/LGPL 污染\",\"参考项目迭代快，协议事实会漂移\"],\"tradeoffs\":\"clean-room 需要自行实现管理端、迁移、调度与协议适配，初期功能少于参考；换取许可证清晰、依赖少、长期可维护。\"},{\"alternatives\":[\"Gin + GORM（参考项目做法）\",\"Ent + PostgreSQL（sub2api）\",\"Chi + sqlc（仍增加生成链）\"],\"area\":\"后端运行时与数据库\",\"evidence\":[\"本机 go version go1.26.5 windows/amd64；npm 11.16.0 / Node v24.18.0。\",\"reference/sub2api/backend/go.mod:64 使用 modernc.org/sqlite v1.44.3；reference/new-api/go.mod:165 使用 modernc.org/sqlite v1.40.1（间接）。\",\"Go embed 的 pattern 相对包目录：internal/webassets 不能引用 ../../web/dist，因此必须让 Vite 输出到 internal/webassets/dist。\",\"reference/new-api/common/database.go:64 使用 WAL/busy_timeout；本计划沿用同等 SQLite 约束。\"],\"rationale\":\"纯 Go SQLite 驱动可在 Windows/Linux 无 CGO 构建；标准库 HTTP 足够覆盖少量路由；唯一 embed 包避免路径不合法。\",\"recommendation\":\"Go 1.26 + 标准库 net/http + database/sql + modernc.org/sqlite（CGO_ENABLED=0）；前端构建输出 internal/webassets/dist，由 internal/webassets 唯一 embed；不使用 Gin/Ent/GORM。\",\"risks\":[\"SQLite 文件锁/写放大\",\"手写 HTTP 路由遗漏边界\",\"modernc 版本与 Go 1.26 兼容性\"],\"tradeoffs\":\"手写路由与 SQL 仓储需要更多测试；单写者限制多实例。\"},{\"alternatives\":[\"Next.js/Rsbuild（claude-code-hub/new-api）\",\"Vue 3 + Ant Design Vue（juhe-ai/sub2api）\",\"shadcn/Tailwind 组件库\"],\"area\":\"前端技术栈与构建产物\",\"evidence\":[\"reference/new-api/web/package.json 使用 React 19.2.7、TanStack Router/Query、Vitest。\",\"reference/sub2api/frontend/package.json 使用 Vite 5/Vitest；证明 Vite 构建链可用。\",\"需求 docs/requirements/api-gateway.md:25,67 要求参考面板布局、不能过于简陋。\"],\"rationale\":\"满足 React/TS 固定要求；Vite 轻量；TanStack 处理路由与请求缓存；输出到 embed 包目录解决 Go embed 相对路径限制。\",\"recommendation\":\"React 19 + TypeScript 5.6 + Vite 5 + TanStack Router/Query + Vitest/RTL；纯 CSS 设计系统，不引入 UI 组件库；Vite outDir=../internal/webassets/dist。\",\"risks\":[\"管理端视觉质量不足\",\"前端依赖膨胀\",\"构建输出目录配置错误导致 embed 仍为占位页\"],\"tradeoffs\":\"需要自行实现表格/抽屉/表单/空态；初期开发量较大。\"},{\"alternatives\":[\"同时实现 Responses WebSocket 与 /responses/compact\",\"只做 Chat Completions\",\"代理 Codex 到 ChatGPT 上游\"],\"area\":\"Codex 入站协议与 manifest\",\"evidence\":[\"本机 codex --version 输出 codex-cli 0.164.0；~/.codex/config.toml（token 已隐藏）配置 wire_api=\\\"responses\\\"、supports_websockets=false、base_url 以 /v1 结尾。\",\"本机 codex features list 输出 responses_websockets removed false、remote_compaction_v2 removed false。\",\"reference/sub2api/backend/internal/handler/openai_codex_models_handler.go:14-22 说明 Codex 从 /models?client_version= 或 /backend-api/codex/models 刷新模型；reference/sub2api/backend/internal/service/openai_codex_models_service.go:367-410 要求完整 ModelInfo，多个 nullable 字段必须存在。\",\"reference/new-api/pkg/jsplugin/routing.go:90 注册 POST /v1/responses；reference/claude-code-hub/tests/e2e/responses-ws-codex-cli-transport.test.ts:404-433（尤其 :429-433）给出 Codex CLI 配置；该 E2E 默认 skip，不能作为已通过证据。\"],\"rationale\":\"当前安装版本与本地配置直接证明 HTTP Responses；WebSocket 特性已 removed，强行实现没有依据；最小 manifest 会被当前 parser 拒绝。\",\"recommendation\":\"按 Codex 0.164.0 的 Responses HTTP SSE 实现：POST /v1/responses、/backend-api/codex/responses、GET /v1/models 与完整 ModelInfo manifest；不实现 WebSocket 与 compact。\",\"risks\":[\"Codex 模型 manifest 字段缺失/值不兼容\",\"未来版本重新启用 WebSocket\",\"compact 在长会话触发 404\"],\"tradeoffs\":\"升级 Codex 后可能重新出现 WS/compact 需求；E2E 固定版本并失败提醒；完整 descriptor 字段较多但可契约测试。\"},{\"alternatives\":[\"假设 ZCode 使用 Chat Completions\",\"假设 ZCode 使用 Anthropic Messages\",\"复用 juhe-ai 的 ZCode 出站身份作为入站协议\"],\"area\":\"ZCode 入站协议\",\"evidence\":[\"rg -ni 'zcode|z-code' reference/sub2api reference/new-api reference/claude-code-hub 无命中。\",\"reference/juhe-ai 中对 ZCode 的命中集中在出站身份/版本跟踪（User-Agent、HTTP-Referer、X-ZCode-App-Version、X-Title 等），见 reference/juhe-ai/docs/functions/智谱GLM账号接入.md:135；未发现入站协议证据。\",\"当前环境 where zcode 无输出，无 ZCode 可执行文件。\"],\"rationale\":\"ZCode 可能使用 Chat、Responses、Anthropic 或私有协议；猜错会静默丢图片/tool/流式字段。用户已批准延后至 MVP+1，第一版不做实现，但 adapter 边界保留。\",\"recommendation\":\"在取得真实抓包或官方协议证据前不实现 ZCode adapter；用户已批准 ZCode 延后至 MVP+1，第一版只保留 adapter 边界；T28 保持 needs-info，禁止猜测。\",\"risks\":[\"无法取得 ZCode 抓包导致 MVP+1 延期\",\"ZCode 动态签名字段无法伪造\",\"ZCode 版本变化\"],\"tradeoffs\":\"ZCode 接入从第一版移出，第一版验收不再被阻塞；MVP+1 需要重新取证，可能推迟 ZCode 上线。\"},{\"alternatives\":[\"统一转成 OpenAI Chat\",\"实现 Responses↔Chat↔Anthropic 全矩阵\",\"只代理单一上游\"],\"area\":\"上游 Provider Adapter 边界\",\"evidence\":[\"reference/claude-code-hub/src/app/v1/_lib/proxy/provider-selector.ts:166-184 明确 response→codex、openai→openai-compatible 的严格同格式路由。\",\"reference/sub2api/backend/internal/pkg/apicompat 与 reference/new-api/relaykit 的跨协议转换规模巨大（recon 统计 10,481 行非测试代码 + 13,282 行测试），不适合 MVP。\",\"上游 new-api/sub2api 本身支持原生 /v1/responses 与 /v1/chat/completions。\"],\"rationale\":\"原生直通保留 tools/input_image/reasoning/未知字段，避免跨协议丢字段；渠道协议可视化，错误明确。\",\"recommendation\":\"只做原生协议直通：openai_responses、openai_chat；anthropic_messages 仅预留，证据后实现。入站协议必须与渠道协议匹配，不做隐式协议转换。\",\"risks\":[\"目标上游不提供原生 Responses\",\"Anthropic 模型需另建渠道\",\"后续转换成本被低估\"],\"tradeoffs\":\"某些只提供 Chat 的中转站无法服务 Codex Responses；需要在管理端配置协议。\"},{\"alternatives\":[\"同账号重试 3 次（sub2api）\",\"weight 加权随机（new-api/CCH）\",\"codex session 粘性亲和（new-api）\"],\"area\":\"调度与失败切换\",\"evidence\":[\"reference/sub2api/backend/internal/service/gateway_scheduling.go 使用 priority + last_used + 同优先级随机；recon 指出其同账号重试 3 次。\",\"reference/new-api/model/channel_cache.go 使用 priority 降序、同优先级 weight 随机。\",\"reference/claude-code-hub/src/app/v1/_lib/proxy/provider-selector.ts:1534-1575 使用最小 priority + costMultiplier + weight 加权随机。\",\"计划新增 channels.last_used_at，T19 在获得槽位后短 UPDATE，用于同优先级 LRU。\"],\"rationale\":\"个人单用户更看重可预测与低重复成本；不照搬权重/同账号重试。\",\"recommendation\":\"候选按 route.priority ASC → channel.priority ASC → last_used_at ASC → id ASC；过滤禁用/熔断/满载；每次请求最多 3 个不同渠道；同一渠道不重试；仅重试网络/401/403/404/408/429/5xx/529。\",\"risks\":[\"400/404 分类不当导致不必要切换\",\"多次切换放大成本\",\"last_used 写入增加 SQLite 写次数\"],\"tradeoffs\":\"单渠道瞬时抖动会直接换渠道；不保证同渠道 session 亲和。\"},{\"alternatives\":[\"立即 429\",\"30s 排队\",\"全部不可用返回 503\"],\"area\":\"渠道并发与满载\",\"evidence\":[\"reference/sub2api/backend/internal/handler/gateway_helper.go:121-131 使用 30s 等待、100ms 起 ×1.5、上限 2s；reference/sub2api/backend/internal/handler/concurrency_error_response.go:13-22 返回 429 gateway_queue_full。\",\"reference/juhe-ai 使用 270s 请求墙钟与队列等待；reference/claude-code-hub 在 provider 并发满时尝试下一个，最终 503；reference/new-api/model/channel.go:23-60 无渠道级并发字段。\"],\"rationale\":\"30s/270s 对个人使用过长；2s 给瞬时释放窗口，之后快速 429 更可控。\",\"recommendation\":\"进程内每渠道 chan struct{} 信号量；满载优先换渠道；全部候选满载最多等待 2s（25ms 轮询），仍无槽位返回 429 channel_capacity_exhausted + Retry-After: 1。\",\"risks\":[\"等待期间占用连接\",\"槽位泄漏\",\"429 与上游 429 混淆\"],\"tradeoffs\":\"高峰期部分请求会快速失败；客户端自行重试。\"},{\"alternatives\":[\"拼接第二个上游实现无缝\",\"提交后静默 EOF\",\"提交后通用 error 帧\"],\"area\":\"流式提交边界与 response.failed\",\"evidence\":[\"reference/juhe-ai/backend-go/projects/gateway/internal/gatewayresponse/commitstate.go:35 明确 CanRetryUpstream() = !SemanticCommitted。\",\"reference/sub2api/backend/internal/handler/stream_error_event.go:13-45 定义 responsesFailedEvent/Response：sequence_number、response.created_at 等字段缺失会被严格 Rust serde 拒绝；:61-92 给出 response.failed 写出。\",\"reference/sub2api/backend/internal/handler/gateway_handler.go:1990-2018 明确提交后只能写 SSE 错误；reference/claude-code-hub/src/app/v1/_lib/proxy/forwarder.ts:2060-2066 只在首个有效内容帧前做 stream gate。\"],\"rationale\":\"已输出内容无法无缝拼接第二个上游；完整 response.failed 是 Codex 读懂终止事件的必要条件。\",\"recommendation\":\"首内容前缓冲原始 SSE，首语义内容出现后提交；提交前失败换渠道，提交后只发一次完整 response.failed，不再换渠道。\",\"risks\":[\"事件分类误判导致提前/延迟提交\",\"response.failed 字段缺失导致客户端解析失败\",\"SSE 缓冲内存过大\"],\"tradeoffs\":\"用户可见中断；但协议语义正确。\"},{\"alternatives\":[\"GORM AutoMigrate\",\"golang-migrate\",\"Ent schema migration\"],\"area\":\"存储、迁移与备份\",\"evidence\":[\"reference/new-api/model/main.go:325-401 仅 AutoMigrate，无版本表/down migration；reference/sub2api 有 323 个 PostgreSQL 迁移，不能直接用于 SQLite。\",\"docs/requirements/api-gateway.md:76 要求迁移可恢复、备份/恢复。\",\"reference/new-api/common/database.go:64 使用 WAL/busy_timeout，可作为 SQLite 运行参数参考。\"],\"rationale\":\"自建迁移器可控制事务、备份、checksum 与失败语义；FK 策略保证删除渠道不破坏历史日志。\",\"recommendation\":\"SQLite WAL + 单写连接 + 嵌入式 forward-only 迁移 + checksum + 迁移前 VACUUM INTO 备份 + restore 子命令；channels.last_used_at；model_routes CASCADE，logs/attempts SET NULL。\",\"risks\":[\"迁移失败损坏库\",\"WAL 备份不一致\",\"Windows 文件锁\"],\"tradeoffs\":\"需要手写 SQL 与迁移测试；单写者限制。\"},{\"alternatives\":[\"复用 TOTP 密钥（sub2api）\",\"明文渠道 Key + 2FA（new-api）\",\"ADMIN_TOKEN（CCH）\"],\"area\":\"密钥与管理员鉴权\",\"evidence\":[\"reference/juhe-ai/backend-go/shared/platform/accountcrypto/crypto.go:89 使用 sha256(secret) 作为 AES-256-GCM 密钥，信封 v1:{iv}:{tag}:{ciphertext}。\",\"reference/sub2api/backend/internal/repository/account_repo.go:151 将 credentials 直接写入；reference/sub2api/backend/internal/repository/aes_encryptor.go:22-35 的 AES key 取自 TOTP 配置，只用于 TOTP 等模块。\",\"reference/new-api/model/channel.go:26 的 Key 为普通 string 列，查看需二次验证；reference/claude-code-hub 使用 ADMIN_TOKEN + HMAC session。\"],\"rationale\":\"满足环境变量主密钥、无二次密码、加密落库；PBKDF2 使用 Go 1.26 标准库 crypto/pbkdf2，减少依赖。\",\"recommendation\":\"上游 Key 用 AES-256-GCM v1 信封，主密钥来自环境变量；管理员密码用 PBKDF2-HMAC-SHA256；session 为 opaque token，DB 只存哈希。\",\"risks\":[\"主密钥丢失导致不可解\",\"弱主密钥\",\"session 固定/CSRF\"],\"tradeoffs\":\"MVP 不做主密钥轮换；密码遗忘无自动重置。\"},{\"alternatives\":[\"预置 capture 开关（下次请求抓取）\",\"默认全量 body\",\"只存错误 body\"],\"area\":\"日志、统计与按需 body 捕获\",\"evidence\":[\"reference/new-api/model/log.go:57-79 只存元数据/配额，无完整 body；reference/new-api/common/body_storage.go 是临时重放存储，请求结束清理。\",\"reference/sub2api/backend/migrations/136_remove_ops_retry_replay.sql 删除 request_body/request_headers，说明默认不存 body 的取舍。\",\"reference/claude-code-hub/.env.example:129 默认把响应正文写 Redis，与本项目隐私目标相反。\"],\"rationale\":\"默认不持久化 prompt，降低隐私与写放大；内存环保留短期调试能力，管理员显式保存满足按需取证。\",\"recommendation\":\"默认仅元数据；诊断环仅内存（5 分钟/50 请求/32MB）+ 管理员显式保存（AES-GCM、24h TTL、可删除、读取 no-store）；统计从 request_logs 聚合。\",\"risks\":[\"内存环泄漏 prompt\",\"捕获数据未清理\",\"统计 SQL 扫描慢\"],\"tradeoffs\":\"超过 5 分钟的请求无法事后抓取；需要明确 UI 提示。\"},{\"alternatives\":[\"照搬 sub2api 17+ 页面\",\"只做一个表格页\",\"引入完整组件库\"],\"area\":\"管理端范围与布局\",\"evidence\":[\"reference/sub2api/frontend/src/router/index.ts:402-472 管理端页面路由；AccountsView.vue 2,635 行、ChannelsView.vue 1,736 行，说明参考面板信息层级但不宜照搬规模。\",\"reference/new-api/web/src/routes/_authenticated/channels/index.tsx:36、dashboard/index.tsx:23、usage-logs/index.tsx:23 提供 React 路由与页面布局参考。\",\"find . -iname '*workbuddy*' 无输出，workbuddy2api-panel 不在工作区。\"],\"rationale\":\"7 页覆盖需求且避免功能堆砌；登录页不在侧边栏，避免计数歧义。\",\"recommendation\":\"固定登录 + 6 个受保护页（共 7 页）：登录、总览、渠道、模型映射、请求观测、调试、设置；侧边栏只显示 6 个受保护页；不引入多租户/支付/运营。\",\"risks\":[\"视觉质量不足\",\"页面状态/错误处理不完整\",\"管理端范围膨胀\"],\"tradeoffs\":\"需要自行实现组件与图表；无多语言/暗色主题。\"},{\"alternatives\":[\"本地裸二进制\",\"Kubernetes\",\"进程内 TLS\"],\"area\":\"部署与发布\",\"evidence\":[\"docs/requirements/api-gateway.md:30-31 要求本机优先、可迁 Linux+Docker、无 Redis/消息队列/微服务。\",\"Go embed 限制：pattern 相对包目录，因此 Docker 构建必须先把 web 输出到 internal/webassets/dist。\",\"T29 的 Dockerfile 契约测试读取 Dockerfile，断言 USER/VOLUME/ENV/EXPOSE 且无 secret；实际 docker build 不在 strict argv 白名单内。\"],\"rationale\":\"单二进制 + 数据卷 + 反向代理 TLS 符合个人自托管；不引入容器编排。\",\"recommendation\":\"多阶段 Docker：Node 构建 internal/webassets/dist，Go 构建单二进制；运行镜像非 root、VOLUME /data、ENV GATEWAY_DATA_DIR=/data、EXPOSE 8080；TLS 由反向代理终止；真实 docker build 为发布前人工验收，argv 验收覆盖 Go 层与 Dockerfile 静态契约。\",\"risks\":[\"镜像泄露 secret\",\"卷未持久化\",\"Linux 文件权限/路径错误\"],\"tradeoffs\":\"真实容器构建需要人工验收；Dockerfile 契约测试只能覆盖声明，不覆盖运行时。\"},{\"alternatives\":[\"只做单元测试\",\"只做假客户端契约测试\",\"使用 shell 脚本验收\"],\"area\":\"测试与验收\",\"evidence\":[\"需求 docs/requirements/api-gateway.md:96-98 要求行为验收 + 真实测试、唯一 argv 命令、禁止 shell/python -c。\",\"reference/claude-code-hub/tests/e2e/responses-ws-codex-cli-transport.test.ts:404-433 提供真实 Codex CLI 配置思路，但默认 skip；本计划改为普通 e2e 包，版本不符/缺 codex 即失败。\",\"本机 codex-cli 0.164.0 可执行，满足真实 E2E 前置条件。\"],\"rationale\":\"行为测试覆盖协议/调度/故障；真实 Codex 能发现模拟客户端看不到的模型发现/请求形状问题；T22 用 Node 脚本同时跑前后端测试。\",\"recommendation\":\"Go httptest + 临时 SQLite + 假上游；React Vitest/RTL；真实 Codex CLI E2E；全部验收使用 strict 白名单内的 go test 与 npm run test:<slice>，不使用 shell/python -c。\",\"risks\":[\"Codex 版本漂移\",\"Windows 进程/端口差异\",\"测试误跳过\"],\"tradeoffs\":\"E2E 较慢且依赖本机 codex；固定版本降低漂移。\"}],\"title\":\"个人 AI API 网关 v2 — strict 实施计划（修订版）\"}",
  "reviewedReviewHash": "sha256:49074cd7869b2bfb802ffc0dcc2602dfb97ef6966d7f34dd1c0952bac1b49827",
  "resolutions": [
    {
      "questionId": "Q-ZCODE",
      "decision": "ZCode 延后到 MVP+1；第一版只保留 adapter 边界并等待协议证据，T28 保持 needs-info 但不阻塞第一版。",
      "evidence": "其他你帮我做决定。你继续吧。"
    }
  ],
  "plan": {
    "mode": "strict",
    "title": "个人 AI API 网关 v2 — strict 实施计划（修订版）",
    "summary": "已按独立评审修订。推荐路线仍为 clean-room 重写：sub2api 是 LGPL + Vue + PostgreSQL/Redis，new-api 是 AGPLv3 + 多租户/支付，juhe-ai 是 Apache-2.0 但 Vue + PG/Redis + 两个常驻进程 + maintenance CLI，claude-code-hub 是 MIT 但 Next.js + PG/Redis。目标技术栈固定为 Go 1.26 + React 19/TypeScript + SQLite 单文件，单进程、单管理员、单客户端 Key。计划文件已内联完整任务合同、独立部署章节；Go embed 改为 internal/webassets/dist；Codex manifest 改为完整 ModelInfo；response.failed 补齐严格字段；增加 channels.last_used_at 与删除渠道 FK 策略；T22 用 npm run test:debug 同时执行前后端测试。Codex 按本机 0.164.0 的 Responses HTTP SSE 实现并做真实 CLI E2E；ZCode 经用户批准延后至 MVP+1，第一版只保留 adapter 边界并等待协议证据，T28 保持 needs-info 但不再阻塞第一版。",
    "goal": "在不复制参考仓库代码、不引入 Redis/多租户的前提下，交付一个可本机运行、可迁移 Linux/Docker 的个人 AI API 网关：单管理员 + 单客户端 Key，支持 Codex 的 Responses 文本/图片/工具/SSE、OpenAI Chat、手工模型映射、渠道优先级与并发、失败切换、观测统计、按需 body 捕获；ZCode 经用户批准延后至 MVP+1，第一版只保留 adapter 边界并等待协议证据。",
    "requirements": [
      {
        "id": "R-001",
        "statement": "网关只有一个管理员、一个统一客户端 API key；不提供注册、多租户、支付、充值和复杂权限。",
        "acceptanceCriteria": [
          "客户端只凭单一 key 的 Authorization: Bearer 或 x-api-key 通过 /v1 鉴权；错误 key 返回 401，且不影响管理端 session。",
          "数据库没有 users/roles/groups/billing 表；管理端只有 admin_credentials 单行与 admin_sessions。",
          "管理前端不出现注册、支付、充值、返利入口。"
        ]
      },
      {
        "id": "R-002",
        "statement": "管理端使用单管理员密码和可撤销 session；支持登录、登出、改密和 CSRF 防护。",
        "acceptanceCriteria": [
          "密码只以 PBKDF2-HMAC-SHA256 哈希存储；数据库与日志无明文。",
          "session cookie 为 HttpOnly/SameSite=Lax；登出、改密或显式撤销后旧 token 失效。",
          "跨源写请求被拒绝；同源管理写请求成功。"
        ]
      },
      {
        "id": "R-003",
        "statement": "渠道以 base URL、上游 key、协议、优先级、并发上限和启停状态管理；上游 key 加密存储，列表脱敏，点击显示明文且不要求二次密码。",
        "acceptanceCriteria": [
          "channels.key_ciphertext 只含 AES-256-GCM 密文；列表 API 不返回 key。",
          "揭示接口返回完整明文并带 no-store；错误或篡改密文读取失败。",
          "base URL 只允许 http/https；并发上限 1..64；name 唯一。"
        ]
      },
      {
        "id": "R-004",
        "statement": "模型到渠道的映射由管理员手工配置，包含模型名、渠道、上游模型、优先级、启停；不做自动发现。",
        "acceptanceCriteria": [
          "model_routes 可 CRUD；同一客户端模型+渠道唯一。",
          "禁用映射/渠道不进入候选；/v1/models 只列有启用映射的模型。",
          "映射不存在或只有不匹配协议渠道时返回 model_not_available，不自动创建、不调用上游。"
        ]
      },
      {
        "id": "R-005",
        "statement": "同一模型可对应多个渠道，按优先级、健康和容量调度；首 token 前失败切换到下一个渠道，重试有上限。",
        "acceptanceCriteria": [
          "按 route.priority、channel.priority、last_used、id 排序；禁用/熔断/满载渠道被过滤。",
          "上游 503/429/网络错误会尝试下一个渠道；400 不重试；最多 3 个渠道，同一渠道不重试。",
          "每次尝试写入 request_attempts；最终错误按入站协议返回。"
        ]
      },
      {
        "id": "R-006",
        "statement": "渠道有进程内并发上限；满载时优先切换其他渠道，全部满载时短暂排队后返回 429。",
        "acceptanceCriteria": [
          "并发计数不超过 max_concurrency，流式/非流式/取消路径都释放槽位。",
          "全部候选满载时最多等待 2s，然后返回 429 + code=channel_capacity_exhausted + Retry-After。",
          "不存在 Redis 依赖。"
        ]
      },
      {
        "id": "R-007",
        "statement": "流式响应在首 token 前可切换；已输出语义内容后不得拼接第二个上游，必须按协议终止并让客户端重试。",
        "acceptanceCriteria": [
          "首内容前上游断流/错误可换渠道；客户端未收到语义字节。",
          "首内容后错误只产生一次 response.failed（Responses）或 error+DONE（Chat），不发起第二个上游；response.failed 含 type、sequence_number、response.id/object/created_at/model/status/output/error。",
          "客户端断开取消上游并记录 499，不重试。"
        ]
      },
      {
        "id": "R-008",
        "statement": "Codex 0.164.0 作为第一版核心客户端，使用 Responses HTTP SSE，支持文本、图片输入、tool calling、流式输出和完整 ModelInfo 模型发现，并通过真实 CLI E2E。",
        "acceptanceCriteria": [
          "codex exec 配置 wire_api=responses、supports_websockets=false 后可完成文本、图片、工具调用。",
          "请求命中 /v1/responses；模型发现返回包含 supported_reasoning_levels/context_window/input_modalities 等完整字段的 Codex manifest；不出现 WebSocket 升级。",
          "真实 Codex E2E 在 codex-cli 0.164.x 下通过；版本不匹配时测试失败。"
        ]
      },
      {
        "id": "R-009",
        "statement": "ZCode 延后至 MVP+1；第一版只保留 adapter 边界并等待协议证据（用户已批准延后）。",
        "acceptanceCriteria": [
          "第一版不注册 ZCode 入站 endpoint、不实现 ZCode adapter，只在 gateway/upstream 预留协议边界。",
          "MVP+1 启动前记录 ZCode 版本、endpoint、method、headers、body、SSE 事件、图片与工具字段，并以取证结果驱动实现。",
          "无证据时 T28 保持 needs-info，不以 Chat/Responses/Anthropic 猜测替代；ZCode 不阻塞第一版验收。"
        ]
      },
      {
        "id": "R-010",
        "statement": "第一版只提供文本、SSE、tool calling、图片输入；不支持 embeddings、图片生成、音频、视频、rerank。",
        "acceptanceCriteria": [
          "Responses/Chat 请求的 tools、input_image、reasoning 等字段原样到达上游。",
          "未实现端点返回 404；不注册 embeddings/images/audio/video 路由。",
          "流式事件与终止事件按协议保留。"
        ]
      },
      {
        "id": "R-011",
        "statement": "请求日志默认只保存元数据、token、延迟和错误；统计包含请求数、输入/输出 token、延迟、错误率和渠道维度。",
        "acceptanceCriteria": [
          "request_logs 不含 header/body/prompt；默认无 request_captures 行。",
          "能从 Responses completed 与 Chat usage 提取 token；缺失时为空，不估算。",
          "统计 API/页面支持时间、模型、渠道筛选，展示请求数、token、平均/P95 延迟和错误率。"
        ]
      },
      {
        "id": "R-012",
        "statement": "完整请求/响应 body 仅在管理员按需保存；默认不落库，保存前提示风险。",
        "acceptanceCriteria": [
          "默认请求只写元数据；内存诊断环有 5 分钟/50 请求/32MB 上限。",
          "管理员确认后保存的 body 为 AES-GCM 密文，TTL 24h，可删除；不保存 header；读取响应带 no-store。",
          "超过 8MB 的单侧 body 标记 truncated，不截断流式转发。"
        ]
      },
      {
        "id": "R-013",
        "statement": "配置校验、数据库迁移、备份/恢复、优雅退出、健康检查、上游超时和重试上限必须可用。",
        "acceptanceCriteria": [
          "缺主密钥/客户端 key/非法监听地址启动失败；/readyz 在 DB 或迁移未就绪时 503。",
          "迁移有版本/checksum 与迁移前备份；迁移失败不改变原库；删除渠道不破坏历史日志。",
          "SIGTERM 后 readiness 503，30s 内完成或取消；上游超时/重试上限可配置且有默认值。"
        ]
      },
      {
        "id": "R-014",
        "statement": "管理端使用 React/TypeScript，包含登录 + 6 个受保护页（共 7 页），布局完整但不堆砌多租户/支付。",
        "acceptanceCriteria": [
          "未登录访问重定向登录；登录后侧边栏显示 6 个受保护页；登出清除 session。",
          "渠道页初始只显示脱敏 key，点击后才显示完整 key；复制可用。",
          "请求页默认只显示元数据；抓取按钮仅在内存诊断环可用时出现。",
          "前端测试覆盖登录、渠道、映射、总览、请求、调试、设置；无支付/注册入口。"
        ]
      },
      {
        "id": "R-015",
        "statement": "Provider Adapter 以原生协议为边界；渠道协议与入站协议不匹配时不调度，不做隐式跨协议转换。",
        "acceptanceCriteria": [
          "Responses 请求只选 openai_responses 渠道，Chat 只选 openai_chat 渠道。",
          "协议不匹配返回 model_not_available；不调用转换器，上游调用次数为 0。",
          "请求中的 tools/input_image/未知字段在直通后保留。"
        ]
      },
      {
        "id": "R-016",
        "statement": "技术栈固定 Go + React/TypeScript + SQLite；单进程、无 Redis/MQ/微服务；可本机运行并迁移 Linux/Docker；只做 clean-room，不复制不兼容代码。",
        "acceptanceCriteria": [
          "go.mod/go.sum 无 Redis/MQ/多租户依赖；package.json/package-lock 为 React/TS；SQLite 是唯一存储。",
          "单二进制通过 internal/webassets 嵌入前端；Dockerfile 使用非 root、/data 卷并可配置监听地址。",
          "LICENSES.md 记录参考项目与直接依赖许可证；仓库无参考项目代码片段，compliance 测试可验证。"
        ]
      },
      {
        "id": "R-017",
        "statement": "上游错误归一化为入站协议对应的错误体，客户端不看到上游 key、URL 或原始敏感 body。",
        "acceptanceCriteria": [
          "上游 401/403 → 502 upstream_auth_failed；429 → 429 upstream_rate_limited；5xx → 502/503；超时 → 504。",
          "OpenAI/Responses 返回 {error:{...}}；Anthropic（如实现）返回顶层 type=error。",
          "错误日志/响应不含 Authorization、x-api-key、上游 base_url 查询串。"
        ]
      }
    ],
    "architecture": {
      "overview": "单进程 Go 服务 + SQLite WAL + 内嵌 React 管理端。公开面为 /v1（Codex Responses、OpenAI Chat、模型列表/Codex manifest）和 /backend-api/codex 别名；管理面为 /admin + /admin/api；运维面为 /healthz 与 /readyz。请求流水线：客户端 Key 鉴权 → 入站协议识别 → 手工模型映射 → 候选渠道调度（优先级/健康/容量）→ 进程内并发槽位 → 原生上游 adapter → 首内容提交门控 → 请求日志/统计/诊断环。运行态并发与熔断状态放内存；渠道、映射、日志、捕获与迁移元数据放 SQLite。第一版不引入 Redis、消息队列、微服务或多实例共享状态。Codex 以 0.164.0 的 Responses HTTP SSE 为基准；ZCode 经用户批准延后至 MVP+1，第一版只保留 adapter 边界。前端构建产物输出到 internal/webassets/dist，由 internal/webassets 的唯一 embed 包嵌入，禁止从包目录外 ../web/dist 取文件。",
      "boundaries": [
        "cmd/gateway：单进程入口与 serve/migrate/backup/restore/keygen/version 子命令；不承载业务规则。",
        "internal/config：环境变量解析、默认值、启动校验、secret 脱敏；包括 GATEWAY_CODEX_CONTEXT_WINDOW。",
        "internal/store：SQLite 打开（WAL、busy_timeout、foreign_keys、单写连接）、版本化迁移、备份/恢复、渠道/映射/日志/捕获仓储；channels.last_used_at 与删除渠道 FK 策略在 0001 定义。",
        "internal/secret：AES-256-GCM v1 信封、主密钥解析、上游 Key 脱敏、日志/错误脱敏。",
        "internal/adminauth：PBKDF2 密码、opaque session、登录限流；不依赖 gateway 数据面。",
        "internal/adminapi：管理 REST API、Origin/CSRF 校验、渠道/映射/统计/请求/调试/捕获端点；只依赖 store、adminauth、upstream probe、observability。",
        "internal/gateway：客户端鉴权、路由表、错误 envelope、请求编排、首内容提交门控；不直接访问 SQLite 细节。",
        "internal/routing：候选查询、过滤、排序、进程内每渠道信号量、有限等待、熔断状态；更新 channels.last_used_at；不依赖 adminapi。",
        "internal/upstream：HTTP client、Responses/Chat 原生 adapter、SSE 解析、超时、重定向限制、连通性探针；不做跨协议转换。",
        "internal/observability：请求日志、usage 提取、统计查询、诊断环形缓冲、捕获加密持久化与 TTL。",
        "internal/webassets：唯一 Go embed 包；`//go:embed dist/*` 只引用包目录内的 internal/webassets/dist；T05 放占位 index.html，T10 Vite 输出到该目录，T29 验收前用 npm run build 重新生成真实产物。",
        "web：React 19 + TypeScript + Vite + TanStack Router/Query + 纯 CSS；只通过 /admin/api 访问后端；Vite outDir=../internal/webassets/dist。",
        "e2e：真实 Codex CLI 跨进程测试；普通测试包，不加 build tag；ZCode 延后至 MVP+1，证据到位后再新增同层测试。",
        "依赖规则：adminapi → store/upstream/observability/adminauth；gateway → routing/upstream/observability/store；routing → store；upstream 不反向依赖 gateway/adminapi；server → webassets。"
      ],
      "securityPrivacy": [
        "上游 Key 只在创建/更新时接收明文，立即 AES-256-GCM 加密；SQLite 仅存 ciphertext 和 mask；管理列表永不返回明文。",
        "主密钥来自 GATEWAY_MASTER_KEY（base64 32 字节），启动时校验；缺失/格式错误拒绝启动；错误信息不回显密钥值。",
        "Key 揭示接口要求有效 admin session，返回 no-store，不做二次密码；不写请求日志；前端仅在用户点击后显示。",
        "管理员密码 PBKDF2-HMAC-SHA256 600k 迭代、随机 salt；session token 只存 SHA-256；cookie HttpOnly/SameSite=Lax/Secure 可配；改密/登出撤销。",
        "所有管理写操作校验同源 Origin；管理响应 no-store；SPA 使用 CSP default-src 'self'，不使用 dangerouslySetInnerHTML。",
        "默认只记元数据、token、延迟、错误；不记 header、prompt、response body；诊断环仅在内存，5 分钟/50 请求/32MB 上限。",
        "按需保存 body 需管理员风险确认，使用同一 AES-GCM 加密，TTL 24h，可删除；超过 8MB 标记 truncated；不保存 Authorization/Cookie；status/read 响应 no-store。",
        "上游 base_url 只允许 http/https；HTTP client 拒绝跨 host 重定向；不转发客户端 Authorization/Cookie 到上游；响应头过滤 hop-by-hop 和 Set-Cookie。",
        "默认监听 127.0.0.1:8080；上服务器由 TLS 反向代理暴露；Docker 显式 0.0.0.0:8080 并挂载 /data；进程内不终止 TLS。",
        "SQLite 全部参数化查询；日志统一 Redact；错误响应不回显上游 base_url 查询串、key 或原始敏感 body。"
      ],
      "dataMigration": [
        "绿地项目，不从 juhe-ai/sub2api/new-api/claude-code-hub 迁移任何数据；渠道、Key、模型映射全部重新配置。",
        "初始迁移 0001 建立 schema_migrations、admin_credentials、admin_sessions、channels（含 last_used_at）、model_routes、request_logs、request_attempts、request_captures。",
        "迁移只前进、不自动回滚；每个 SQL 文件一个事务；schema_migrations 记录 version/name/checksum/applied_at；checksum 不匹配拒绝启动。",
        "每次迁移前用 VACUUM INTO 生成 GATEWAY_DATA_DIR/backups/pre-migrate-v<from>-<timestamp>.db；失败保留备份并停止启动。",
        "restore 子命令先校验备份可打开且 schema 版本兼容，创建 pre-restore 备份后再替换；文档要求先停进程并清理 -wal/-shm。",
        "SQLite 使用 WAL、busy_timeout=5000、foreign_keys=ON、synchronous=NORMAL、SetMaxOpenConns(1)；日志/捕获写入短事务，不占用流式连接。",
        "model_routes.channel_id FK ON DELETE CASCADE；request_logs/request_attempts.channel_id FK ON DELETE SET NULL，channel_name 快照保留；删除渠道不破坏历史统计。",
        "主密钥不随数据库自动轮换；MVP 要求 GATEWAY_MASTER_KEY 稳定并离线备份；v1 信封预留版本字段，轮换作为后续独立迁移。"
      ]
    },
    "technicalDecisions": [
      {
        "area": "路线与许可证",
        "recommendation": "clean-room 重写，不 fork 任何参考仓库；只借鉴协议、交互和运维模式。",
        "evidence": [
          "reference/sub2api/LICENSE:1 为 GNU LGPL v3；reference/sub2api/backend/go.mod:32/35 使用 lib/pq 与 Redis，前端为 Vue 3。",
          "reference/new-api/LICENSE:1 为 AGPLv3；reference/new-api/NOTICE:6-25 要求保留前端署名、原项目链接并标记修改。",
          "reference/juhe-ai/LICENSE:1 为 Apache-2.0；README.md:151 声明 SQLite 已退役，运行时为 PG+Redis，两个常驻进程 + maintenance CLI。",
          "reference/claude-code-hub/LICENSE:1 为 MIT；但运行时为 Next.js + Hono + PostgreSQL/Redis。"
        ],
        "rationale": "四项参考的许可证与固定技术栈均不完全兼容；fork 的许可证义务、UI/DB 重写和长期 rebase 成本高于 clean-room。",
        "alternatives": [
          "fork new-api（React+SQLite 最近，但 AGPL + 多租户/支付/任务插件）",
          "fork sub2api（LGPL + Vue + PostgreSQL/Redis + 70 万行）",
          "裁剪 juhe-ai（Apache，但 Vue + PG/Redis + 两个常驻进程 + maintenance CLI）",
          "fork claude-code-hub（MIT，但 Next.js + PG/Redis + 严格同格式）"
        ],
        "tradeoffs": "clean-room 需要自行实现管理端、迁移、调度与协议适配，初期功能少于参考；换取许可证清晰、依赖少、长期可维护。",
        "risks": [
          "低估 Codex/管理端实现量",
          "误复制参考代码造成 AGPL/LGPL 污染",
          "参考项目迭代快，协议事实会漂移"
        ]
      },
      {
        "area": "后端运行时与数据库",
        "recommendation": "Go 1.26 + 标准库 net/http + database/sql + modernc.org/sqlite（CGO_ENABLED=0）；前端构建输出 internal/webassets/dist，由 internal/webassets 唯一 embed；不使用 Gin/Ent/GORM。",
        "evidence": [
          "本机 go version go1.26.5 windows/amd64；npm 11.16.0 / Node v24.18.0。",
          "reference/sub2api/backend/go.mod:64 使用 modernc.org/sqlite v1.44.3；reference/new-api/go.mod:165 使用 modernc.org/sqlite v1.40.1（间接）。",
          "Go embed 的 pattern 相对包目录：internal/webassets 不能引用 ../../web/dist，因此必须让 Vite 输出到 internal/webassets/dist。",
          "reference/new-api/common/database.go:64 使用 WAL/busy_timeout；本计划沿用同等 SQLite 约束。"
        ],
        "rationale": "纯 Go SQLite 驱动可在 Windows/Linux 无 CGO 构建；标准库 HTTP 足够覆盖少量路由；唯一 embed 包避免路径不合法。",
        "alternatives": [
          "Gin + GORM（参考项目做法）",
          "Ent + PostgreSQL（sub2api）",
          "Chi + sqlc（仍增加生成链）"
        ],
        "tradeoffs": "手写路由与 SQL 仓储需要更多测试；单写者限制多实例。",
        "risks": [
          "SQLite 文件锁/写放大",
          "手写 HTTP 路由遗漏边界",
          "modernc 版本与 Go 1.26 兼容性"
        ]
      },
      {
        "area": "前端技术栈与构建产物",
        "recommendation": "React 19 + TypeScript 5.6 + Vite 5 + TanStack Router/Query + Vitest/RTL；纯 CSS 设计系统，不引入 UI 组件库；Vite outDir=../internal/webassets/dist。",
        "evidence": [
          "reference/new-api/web/package.json 使用 React 19.2.7、TanStack Router/Query、Vitest。",
          "reference/sub2api/frontend/package.json 使用 Vite 5/Vitest；证明 Vite 构建链可用。",
          "需求 docs/requirements/api-gateway.md:25,67 要求参考面板布局、不能过于简陋。"
        ],
        "rationale": "满足 React/TS 固定要求；Vite 轻量；TanStack 处理路由与请求缓存；输出到 embed 包目录解决 Go embed 相对路径限制。",
        "alternatives": [
          "Next.js/Rsbuild（claude-code-hub/new-api）",
          "Vue 3 + Ant Design Vue（juhe-ai/sub2api）",
          "shadcn/Tailwind 组件库"
        ],
        "tradeoffs": "需要自行实现表格/抽屉/表单/空态；初期开发量较大。",
        "risks": [
          "管理端视觉质量不足",
          "前端依赖膨胀",
          "构建输出目录配置错误导致 embed 仍为占位页"
        ]
      },
      {
        "area": "Codex 入站协议与 manifest",
        "recommendation": "按 Codex 0.164.0 的 Responses HTTP SSE 实现：POST /v1/responses、/backend-api/codex/responses、GET /v1/models 与完整 ModelInfo manifest；不实现 WebSocket 与 compact。",
        "evidence": [
          "本机 codex --version 输出 codex-cli 0.164.0；~/.codex/config.toml（token 已隐藏）配置 wire_api=\"responses\"、supports_websockets=false、base_url 以 /v1 结尾。",
          "本机 codex features list 输出 responses_websockets removed false、remote_compaction_v2 removed false。",
          "reference/sub2api/backend/internal/handler/openai_codex_models_handler.go:14-22 说明 Codex 从 /models?client_version= 或 /backend-api/codex/models 刷新模型；reference/sub2api/backend/internal/service/openai_codex_models_service.go:367-410 要求完整 ModelInfo，多个 nullable 字段必须存在。",
          "reference/new-api/pkg/jsplugin/routing.go:90 注册 POST /v1/responses；reference/claude-code-hub/tests/e2e/responses-ws-codex-cli-transport.test.ts:404-433（尤其 :429-433）给出 Codex CLI 配置；该 E2E 默认 skip，不能作为已通过证据。"
        ],
        "rationale": "当前安装版本与本地配置直接证明 HTTP Responses；WebSocket 特性已 removed，强行实现没有依据；最小 manifest 会被当前 parser 拒绝。",
        "alternatives": [
          "同时实现 Responses WebSocket 与 /responses/compact",
          "只做 Chat Completions",
          "代理 Codex 到 ChatGPT 上游"
        ],
        "tradeoffs": "升级 Codex 后可能重新出现 WS/compact 需求；E2E 固定版本并失败提醒；完整 descriptor 字段较多但可契约测试。",
        "risks": [
          "Codex 模型 manifest 字段缺失/值不兼容",
          "未来版本重新启用 WebSocket",
          "compact 在长会话触发 404"
        ]
      },
      {
        "area": "ZCode 入站协议",
        "recommendation": "在取得真实抓包或官方协议证据前不实现 ZCode adapter；用户已批准 ZCode 延后至 MVP+1，第一版只保留 adapter 边界；T28 保持 needs-info，禁止猜测。",
        "evidence": [
          "rg -ni 'zcode|z-code' reference/sub2api reference/new-api reference/claude-code-hub 无命中。",
          "reference/juhe-ai 中对 ZCode 的命中集中在出站身份/版本跟踪（User-Agent、HTTP-Referer、X-ZCode-App-Version、X-Title 等），见 reference/juhe-ai/docs/functions/智谱GLM账号接入.md:135；未发现入站协议证据。",
          "当前环境 where zcode 无输出，无 ZCode 可执行文件。"
        ],
        "rationale": "ZCode 可能使用 Chat、Responses、Anthropic 或私有协议；猜错会静默丢图片/tool/流式字段。用户已批准延后至 MVP+1，第一版不做实现，但 adapter 边界保留。",
        "alternatives": [
          "假设 ZCode 使用 Chat Completions",
          "假设 ZCode 使用 Anthropic Messages",
          "复用 juhe-ai 的 ZCode 出站身份作为入站协议"
        ],
        "tradeoffs": "ZCode 接入从第一版移出，第一版验收不再被阻塞；MVP+1 需要重新取证，可能推迟 ZCode 上线。",
        "risks": [
          "无法取得 ZCode 抓包导致 MVP+1 延期",
          "ZCode 动态签名字段无法伪造",
          "ZCode 版本变化"
        ]
      },
      {
        "area": "上游 Provider Adapter 边界",
        "recommendation": "只做原生协议直通：openai_responses、openai_chat；anthropic_messages 仅预留，证据后实现。入站协议必须与渠道协议匹配，不做隐式协议转换。",
        "evidence": [
          "reference/claude-code-hub/src/app/v1/_lib/proxy/provider-selector.ts:166-184 明确 response→codex、openai→openai-compatible 的严格同格式路由。",
          "reference/sub2api/backend/internal/pkg/apicompat 与 reference/new-api/relaykit 的跨协议转换规模巨大（recon 统计 10,481 行非测试代码 + 13,282 行测试），不适合 MVP。",
          "上游 new-api/sub2api 本身支持原生 /v1/responses 与 /v1/chat/completions。"
        ],
        "rationale": "原生直通保留 tools/input_image/reasoning/未知字段，避免跨协议丢字段；渠道协议可视化，错误明确。",
        "alternatives": [
          "统一转成 OpenAI Chat",
          "实现 Responses↔Chat↔Anthropic 全矩阵",
          "只代理单一上游"
        ],
        "tradeoffs": "某些只提供 Chat 的中转站无法服务 Codex Responses；需要在管理端配置协议。",
        "risks": [
          "目标上游不提供原生 Responses",
          "Anthropic 模型需另建渠道",
          "后续转换成本被低估"
        ]
      },
      {
        "area": "调度与失败切换",
        "recommendation": "候选按 route.priority ASC → channel.priority ASC → last_used_at ASC → id ASC；过滤禁用/熔断/满载；每次请求最多 3 个不同渠道；同一渠道不重试；仅重试网络/401/403/404/408/429/5xx/529。",
        "evidence": [
          "reference/sub2api/backend/internal/service/gateway_scheduling.go 使用 priority + last_used + 同优先级随机；recon 指出其同账号重试 3 次。",
          "reference/new-api/model/channel_cache.go 使用 priority 降序、同优先级 weight 随机。",
          "reference/claude-code-hub/src/app/v1/_lib/proxy/provider-selector.ts:1534-1575 使用最小 priority + costMultiplier + weight 加权随机。",
          "计划新增 channels.last_used_at，T19 在获得槽位后短 UPDATE，用于同优先级 LRU。"
        ],
        "rationale": "个人单用户更看重可预测与低重复成本；不照搬权重/同账号重试。",
        "alternatives": [
          "同账号重试 3 次（sub2api）",
          "weight 加权随机（new-api/CCH）",
          "codex session 粘性亲和（new-api）"
        ],
        "tradeoffs": "单渠道瞬时抖动会直接换渠道；不保证同渠道 session 亲和。",
        "risks": [
          "400/404 分类不当导致不必要切换",
          "多次切换放大成本",
          "last_used 写入增加 SQLite 写次数"
        ]
      },
      {
        "area": "渠道并发与满载",
        "recommendation": "进程内每渠道 chan struct{} 信号量；满载优先换渠道；全部候选满载最多等待 2s（25ms 轮询），仍无槽位返回 429 channel_capacity_exhausted + Retry-After: 1。",
        "evidence": [
          "reference/sub2api/backend/internal/handler/gateway_helper.go:121-131 使用 30s 等待、100ms 起 ×1.5、上限 2s；reference/sub2api/backend/internal/handler/concurrency_error_response.go:13-22 返回 429 gateway_queue_full。",
          "reference/juhe-ai 使用 270s 请求墙钟与队列等待；reference/claude-code-hub 在 provider 并发满时尝试下一个，最终 503；reference/new-api/model/channel.go:23-60 无渠道级并发字段。"
        ],
        "rationale": "30s/270s 对个人使用过长；2s 给瞬时释放窗口，之后快速 429 更可控。",
        "alternatives": [
          "立即 429",
          "30s 排队",
          "全部不可用返回 503"
        ],
        "tradeoffs": "高峰期部分请求会快速失败；客户端自行重试。",
        "risks": [
          "等待期间占用连接",
          "槽位泄漏",
          "429 与上游 429 混淆"
        ]
      },
      {
        "area": "流式提交边界与 response.failed",
        "recommendation": "首内容前缓冲原始 SSE，首语义内容出现后提交；提交前失败换渠道，提交后只发一次完整 response.failed，不再换渠道。",
        "evidence": [
          "reference/juhe-ai/backend-go/projects/gateway/internal/gatewayresponse/commitstate.go:35 明确 CanRetryUpstream() = !SemanticCommitted。",
          "reference/sub2api/backend/internal/handler/stream_error_event.go:13-45 定义 responsesFailedEvent/Response：sequence_number、response.created_at 等字段缺失会被严格 Rust serde 拒绝；:61-92 给出 response.failed 写出。",
          "reference/sub2api/backend/internal/handler/gateway_handler.go:1990-2018 明确提交后只能写 SSE 错误；reference/claude-code-hub/src/app/v1/_lib/proxy/forwarder.ts:2060-2066 只在首个有效内容帧前做 stream gate。"
        ],
        "rationale": "已输出内容无法无缝拼接第二个上游；完整 response.failed 是 Codex 读懂终止事件的必要条件。",
        "alternatives": [
          "拼接第二个上游实现无缝",
          "提交后静默 EOF",
          "提交后通用 error 帧"
        ],
        "tradeoffs": "用户可见中断；但协议语义正确。",
        "risks": [
          "事件分类误判导致提前/延迟提交",
          "response.failed 字段缺失导致客户端解析失败",
          "SSE 缓冲内存过大"
        ]
      },
      {
        "area": "存储、迁移与备份",
        "recommendation": "SQLite WAL + 单写连接 + 嵌入式 forward-only 迁移 + checksum + 迁移前 VACUUM INTO 备份 + restore 子命令；channels.last_used_at；model_routes CASCADE，logs/attempts SET NULL。",
        "evidence": [
          "reference/new-api/model/main.go:325-401 仅 AutoMigrate，无版本表/down migration；reference/sub2api 有 323 个 PostgreSQL 迁移，不能直接用于 SQLite。",
          "docs/requirements/api-gateway.md:76 要求迁移可恢复、备份/恢复。",
          "reference/new-api/common/database.go:64 使用 WAL/busy_timeout，可作为 SQLite 运行参数参考。"
        ],
        "rationale": "自建迁移器可控制事务、备份、checksum 与失败语义；FK 策略保证删除渠道不破坏历史日志。",
        "alternatives": [
          "GORM AutoMigrate",
          "golang-migrate",
          "Ent schema migration"
        ],
        "tradeoffs": "需要手写 SQL 与迁移测试；单写者限制。",
        "risks": [
          "迁移失败损坏库",
          "WAL 备份不一致",
          "Windows 文件锁"
        ]
      },
      {
        "area": "密钥与管理员鉴权",
        "recommendation": "上游 Key 用 AES-256-GCM v1 信封，主密钥来自环境变量；管理员密码用 PBKDF2-HMAC-SHA256；session 为 opaque token，DB 只存哈希。",
        "evidence": [
          "reference/juhe-ai/backend-go/shared/platform/accountcrypto/crypto.go:89 使用 sha256(secret) 作为 AES-256-GCM 密钥，信封 v1:{iv}:{tag}:{ciphertext}。",
          "reference/sub2api/backend/internal/repository/account_repo.go:151 将 credentials 直接写入；reference/sub2api/backend/internal/repository/aes_encryptor.go:22-35 的 AES key 取自 TOTP 配置，只用于 TOTP 等模块。",
          "reference/new-api/model/channel.go:26 的 Key 为普通 string 列，查看需二次验证；reference/claude-code-hub 使用 ADMIN_TOKEN + HMAC session。"
        ],
        "rationale": "满足环境变量主密钥、无二次密码、加密落库；PBKDF2 使用 Go 1.26 标准库 crypto/pbkdf2，减少依赖。",
        "alternatives": [
          "复用 TOTP 密钥（sub2api）",
          "明文渠道 Key + 2FA（new-api）",
          "ADMIN_TOKEN（CCH）"
        ],
        "tradeoffs": "MVP 不做主密钥轮换；密码遗忘无自动重置。",
        "risks": [
          "主密钥丢失导致不可解",
          "弱主密钥",
          "session 固定/CSRF"
        ]
      },
      {
        "area": "日志、统计与按需 body 捕获",
        "recommendation": "默认仅元数据；诊断环仅内存（5 分钟/50 请求/32MB）+ 管理员显式保存（AES-GCM、24h TTL、可删除、读取 no-store）；统计从 request_logs 聚合。",
        "evidence": [
          "reference/new-api/model/log.go:57-79 只存元数据/配额，无完整 body；reference/new-api/common/body_storage.go 是临时重放存储，请求结束清理。",
          "reference/sub2api/backend/migrations/136_remove_ops_retry_replay.sql 删除 request_body/request_headers，说明默认不存 body 的取舍。",
          "reference/claude-code-hub/.env.example:129 默认把响应正文写 Redis，与本项目隐私目标相反。"
        ],
        "rationale": "默认不持久化 prompt，降低隐私与写放大；内存环保留短期调试能力，管理员显式保存满足按需取证。",
        "alternatives": [
          "预置 capture 开关（下次请求抓取）",
          "默认全量 body",
          "只存错误 body"
        ],
        "tradeoffs": "超过 5 分钟的请求无法事后抓取；需要明确 UI 提示。",
        "risks": [
          "内存环泄漏 prompt",
          "捕获数据未清理",
          "统计 SQL 扫描慢"
        ]
      },
      {
        "area": "管理端范围与布局",
        "recommendation": "固定登录 + 6 个受保护页（共 7 页）：登录、总览、渠道、模型映射、请求观测、调试、设置；侧边栏只显示 6 个受保护页；不引入多租户/支付/运营。",
        "evidence": [
          "reference/sub2api/frontend/src/router/index.ts:402-472 管理端页面路由；AccountsView.vue 2,635 行、ChannelsView.vue 1,736 行，说明参考面板信息层级但不宜照搬规模。",
          "reference/new-api/web/src/routes/_authenticated/channels/index.tsx:36、dashboard/index.tsx:23、usage-logs/index.tsx:23 提供 React 路由与页面布局参考。",
          "find . -iname '*workbuddy*' 无输出，workbuddy2api-panel 不在工作区。"
        ],
        "rationale": "7 页覆盖需求且避免功能堆砌；登录页不在侧边栏，避免计数歧义。",
        "alternatives": [
          "照搬 sub2api 17+ 页面",
          "只做一个表格页",
          "引入完整组件库"
        ],
        "tradeoffs": "需要自行实现组件与图表；无多语言/暗色主题。",
        "risks": [
          "视觉质量不足",
          "页面状态/错误处理不完整",
          "管理端范围膨胀"
        ]
      },
      {
        "area": "部署与发布",
        "recommendation": "多阶段 Docker：Node 构建 internal/webassets/dist，Go 构建单二进制；运行镜像非 root、VOLUME /data、ENV GATEWAY_DATA_DIR=/data、EXPOSE 8080；TLS 由反向代理终止；真实 docker build 为发布前人工验收，argv 验收覆盖 Go 层与 Dockerfile 静态契约。",
        "evidence": [
          "docs/requirements/api-gateway.md:30-31 要求本机优先、可迁 Linux+Docker、无 Redis/消息队列/微服务。",
          "Go embed 限制：pattern 相对包目录，因此 Docker 构建必须先把 web 输出到 internal/webassets/dist。",
          "T29 的 Dockerfile 契约测试读取 Dockerfile，断言 USER/VOLUME/ENV/EXPOSE 且无 secret；实际 docker build 不在 strict argv 白名单内。"
        ],
        "rationale": "单二进制 + 数据卷 + 反向代理 TLS 符合个人自托管；不引入容器编排。",
        "alternatives": [
          "本地裸二进制",
          "Kubernetes",
          "进程内 TLS"
        ],
        "tradeoffs": "真实容器构建需要人工验收；Dockerfile 契约测试只能覆盖声明，不覆盖运行时。",
        "risks": [
          "镜像泄露 secret",
          "卷未持久化",
          "Linux 文件权限/路径错误"
        ]
      },
      {
        "area": "测试与验收",
        "recommendation": "Go httptest + 临时 SQLite + 假上游；React Vitest/RTL；真实 Codex CLI E2E；全部验收使用 strict 白名单内的 go test 与 npm run test:<slice>，不使用 shell/python -c。",
        "evidence": [
          "需求 docs/requirements/api-gateway.md:96-98 要求行为验收 + 真实测试、唯一 argv 命令、禁止 shell/python -c。",
          "reference/claude-code-hub/tests/e2e/responses-ws-codex-cli-transport.test.ts:404-433 提供真实 Codex CLI 配置思路，但默认 skip；本计划改为普通 e2e 包，版本不符/缺 codex 即失败。",
          "本机 codex-cli 0.164.0 可执行，满足真实 E2E 前置条件。"
        ],
        "rationale": "行为测试覆盖协议/调度/故障；真实 Codex 能发现模拟客户端看不到的模型发现/请求形状问题；T22 用 Node 脚本同时跑前后端测试。",
        "alternatives": [
          "只做单元测试",
          "只做假客户端契约测试",
          "使用 shell 脚本验收"
        ],
        "tradeoffs": "E2E 较慢且依赖本机 codex；固定版本降低漂移。",
        "risks": [
          "Codex 版本漂移",
          "Windows 进程/端口差异",
          "测试误跳过"
        ]
      }
    ],
    "tasks": [
      {
        "id": "T01",
        "title": "建立 Go 模块、配置校验与依赖许可基线",
        "requirementIds": [
          "R-013",
          "R-016"
        ],
        "files": [
          "go.mod",
          "go.sum",
          "cmd/gateway/main.go",
          "internal/config/config.go",
          "internal/config/config_test.go",
          "internal/compliance/licenses_test.go",
          "LICENSES.md"
        ],
        "steps": [
          "初始化 module（本地名 ai-router，Go 1.26.0），只加入后续任务确实使用的依赖。",
          "实现 Config：GATEWAY_LISTEN_ADDR 默认 127.0.0.1:8080、GATEWAY_DATA_DIR、GATEWAY_DB_PATH、GATEWAY_MASTER_KEY（base64 32 字节）、GATEWAY_CLIENT_KEY（>=24 字符）、GATEWAY_ADMIN_PASSWORD（首次启动）、GATEWAY_COOKIE_SECURE、GATEWAY_CODEX_CONTEXT_WINDOW、上游超时/重试/容量等待/捕获上限。",
          "实现启动校验与脱敏 String()/错误信息；缺失或非法值只引用变量名，不输出值；非法监听地址也失败。",
          "在 main 中分发 serve/version/keygen 子命令；serve 目前只完成配置加载与占位服务器。",
          "go mod tidy 生成 go.sum；LICENSES.md 增加直接依赖许可证清单并与 go.mod/go.sum 对齐；compliance 测试读取 LICENSES.md、go.mod 与 go.sum，检查四个参考许可证、直接依赖许可证清单与禁用 Redis/MQ 依赖。禁用标识/路径扫描只覆盖项目生产源码：cmd/、internal/、web/src/、e2e/ 下的 *.go、*.ts、*.tsx、*.js；排除所有 *_test.go、*.test.ts、*.test.tsx、*.test.js 以及 internal/compliance/licenses_test.go 自身；显式排除且不遍历 reference/、.zcode/、web/node_modules/、internal/webassets/dist/、LICENSES.md、docs/，扫描不因 LICENSES.md 列出 sub2api/new-api/juhe-ai/claude-code-hub 而命中。对扫描范围内的文件断言不 import reference/、不含参考项目专有标识；文件注明自动扫描不替代人工审计。"
        ],
        "behaviorAcceptance": [
          "缺少 GATEWAY_MASTER_KEY、base64 不是 32 字节、GATEWAY_CLIENT_KEY 少于 24 字符或 GATEWAY_LISTEN_ADDR 非法时启动失败，错误不含密钥值。",
          "合法配置得到默认 127.0.0.1:8080 与数据目录；CODEX_CONTEXT_WINDOW 默认 128000。",
          "配置的 String()/日志输出不包含 master key、client key、admin password。",
          "LICENSES.md 含 sub2api LGPL-3.0-or-later、new-api AGPLv3、juhe-ai Apache-2.0、claude-code-hub MIT，并列出直接依赖许可证清单；compliance 测试读取 LICENSES.md、go.mod 与 go.sum，检查四个参考许可证、直接依赖许可证与禁用 Redis/MQ 依赖均通过。",
          "禁用标识/路径扫描仅覆盖 cmd/、internal/、web/src/、e2e/ 下的 *.go、*.ts、*.tsx、*.js 生产源码，排除所有 *_test.go、*.test.ts、*.test.tsx、*.test.js 及 internal/compliance/licenses_test.go 自身；扫描范围内不出现 reference/ 导入或参考项目专有标识；显式排除且不遍历 reference/、.zcode/、web/node_modules/、internal/webassets/dist/、LICENSES.md、docs/，扫描不因 LICENSES.md 列出 sub2api/new-api/juhe-ai/claude-code-hub 而命中；文件注明自动扫描不替代人工审计。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "./internal/config",
            "./internal/compliance"
          ]
        },
        "dependsOn": [],
        "risk": "环境变量中的主密钥/客户端 Key 可能被错误信息或日志带出；弱 Key 会让加密形同虚设；许可证基线可能随依赖漂移。",
        "riskMitigation": [
          "错误信息只引用变量名",
          "Config 实现脱敏 String()",
          "主密钥强制 base64 32 字节",
          "compliance 测试同时读取 LICENSES.md、go.mod 与 go.sum 检查禁用依赖",
          "测试断言输出不含 secret",
          "禁用标识/路径扫描只能发现明显复制，自动扫描不替代人工审计"
        ],
        "status": "ready",
        "rationale": "所有后续模块需要统一配置与 secret 约束；先把许可证与依赖基线固定，避免实现中误拷参考代码。"
      },
      {
        "id": "T02",
        "title": "SQLite 初始化、版本化迁移与迁移前备份",
        "requirementIds": [
          "R-003",
          "R-011",
          "R-012",
          "R-013"
        ],
        "files": [
          "internal/store/db.go",
          "internal/store/migrate.go",
          "internal/store/backup.go",
          "internal/store/migrate_test.go",
          "internal/store/migrations/0001_init.sql",
          "cmd/gateway/main.go"
        ],
        "steps": [
          "用 modernc.org/sqlite 打开数据库，设置 WAL、busy_timeout=5000、foreign_keys=ON、synchronous=NORMAL、SetMaxOpenConns(1)。",
          "实现 go:embed 迁移加载、schema_migrations 版本/checksum 记录、每个迁移一个事务、checksum 不匹配拒绝启动。",
          "在 0001_init.sql 建立全部表与索引；channels 含 last_used_at；model_routes.channel_id ON DELETE CASCADE；request_logs/request_attempts.channel_id ON DELETE SET NULL 并保留 channel_name。",
          "实现 VACUUM INTO 备份与 restore 校验；迁移前自动备份到 filepath.Join(GATEWAY_DATA_DIR, \"backups\", \"pre-migrate-v<from>-<timestamp>.db\")。",
          "测试 fresh migrate、重复 migrate、坏 checksum、迁移失败不改库、备份可重新打开、删除 channel 的 FK 行为。",
          "在 cmd/gateway/main.go 注册 migrate/backup/restore 子命令，只做参数解析与退出码处理，调用 internal/store 导出的迁移/备份/恢复入口；不在 cmd 层复制业务逻辑。"
        ],
        "behaviorAcceptance": [
          "新库迁移后 schema_migrations 有当前版本，所有表/索引存在；第二次 migrate 不重复执行。",
          "修改已应用迁移内容导致 checksum 不匹配时启动失败，原库 schema 不变。",
          "迁移前备份文件可打开，包含迁移前已插入的数据；restore 校验失败时拒绝覆盖。",
          "删除 channel 后 model_routes 级联删除，request_logs/request_attempts.channel_id 置 NULL 且 channel_name 保留；SQLite WAL/foreign_keys/单写连接生效。",
          "cmd/gateway 的 migrate/backup/restore 子命令在 main 中注册并调用 store 导出入口；store 层在 internal/store/migrate_test.go 以导出入口覆盖迁移/备份/恢复行为；go test ./cmd/gateway 仅做编译级验证，不执行子命令。",
          "迁移后的 sqlite_master 不含 users/roles/groups/billing 表。",
          "备份文件落在 GATEWAY_DATA_DIR/backups，不依赖进程 cwd 的 data/ 目录。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "./internal/store",
            "./cmd/gateway"
          ]
        },
        "dependsOn": [
          "T01"
        ],
        "risk": "SQLite 在 Windows 文件锁、WAL 与备份并发下可能 busy 或产生不一致备份；迁移失败可能留下半成品；FK 策略错误会阻止删除或破坏历史。",
        "riskMitigation": [
          "单写连接 + busy_timeout",
          "迁移事务与 checksum",
          "VACUUM INTO 前完成写事务",
          "失败迁移测试验证 schema 不变",
          "FK ON DELETE SET NULL + channel_name 快照",
          "restore 前创建 pre-restore 备份"
        ],
        "status": "ready",
        "rationale": "数据层是所有功能的基础；先固定可恢复迁移、备份和删除渠道的数据保留语义。"
      },
      {
        "id": "T03",
        "title": "上游 Key AES-GCM 信封与脱敏",
        "requirementIds": [
          "R-003",
          "R-013"
        ],
        "files": [
          "internal/secret/envelope.go",
          "internal/secret/envelope_test.go",
          "internal/secret/redact.go",
          "internal/secret/redact_test.go"
        ],
        "steps": [
          "解析 GATEWAY_MASTER_KEY 为 32 字节；实现 AES-256-GCM，12 字节随机 nonce，AAD 绑定用途，输出 v1:<base64url(nonce)>:<base64url(ciphertext)>。",
          "实现 Encrypt/Decrypt 与版本校验；错误不返回明文。",
          "实现 Key 脱敏：长 Key 保留前缀 3 + 后缀 4，其余为 *；短 Key 全部 *。",
          "实现 Redact(map/header/value)，覆盖 authorization、x-api-key、cookie、set-cookie、proxy-authorization。",
          "测试 roundtrip、错误 key、篡改、格式错误、脱敏输出。"
        ],
        "behaviorAcceptance": [
          "同一明文加密两次得到不同 nonce/密文，Decrypt 均还原。",
          "错误主密钥或篡改密文时 Decrypt 失败，错误不含明文。",
          "列表可得到 sk-***abcd 形式 mask；Redact 后日志不含完整 key。",
          "v1 前缀/版本不匹配被拒绝。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "./internal/secret"
          ]
        },
        "dependsOn": [
          "T01"
        ],
        "risk": "自行实现加密容易在 nonce/AAD/错误处理上出错；主密钥丢失会导致所有上游 Key 不可解。",
        "riskMitigation": [
          "使用标准库 crypto/aes + cipher.NewGCM",
          "随机 nonce + AAD + 版本字段",
          "测试错误 key/篡改",
          "文档要求离线备份主密钥"
        ],
        "status": "ready",
        "rationale": "渠道 Key 加密是硬性安全要求；先独立实现并用测试锁定格式，再接入渠道仓储。"
      },
      {
        "id": "T04",
        "title": "单管理员密码、session 与登录限流",
        "requirementIds": [
          "R-001",
          "R-002"
        ],
        "files": [
          "internal/adminauth/password.go",
          "internal/adminauth/session.go",
          "internal/adminauth/limiter.go",
          "internal/adminauth/password_test.go",
          "internal/adminauth/session_test.go"
        ],
        "steps": [
          "用 crypto/pbkdf2（HMAC-SHA256，600000 迭代、16 字节 salt、32 字节 key）实现密码哈希与校验；编码为 pbkdf2-sha256$iter$salt$hash。",
          "首次启动无 admin 记录时从 GATEWAY_ADMIN_PASSWORD 创建；密码最短 12 字符；已有记录时忽略环境变量。",
          "实现 opaque session：32 字节随机 token，DB 存 SHA-256，12h 绝对过期；登录时生成新 token，登出/改密撤销。",
          "实现登录限流：同一来源 15 分钟最多 5 次失败，成功清除；使用常量时间比较避免用户枚举。",
          "测试哈希、错误密码、过期/撤销、session 轮换、限流。"
        ],
        "behaviorAcceptance": [
          "正确密码登录成功；错误密码失败；数据库不含明文密码。",
          "session 过期或被撤销后校验失败；重新登录生成不同 token。",
          "第 6 次连续失败被限流；成功登录后计数清零。",
          "改密后除当前 session 外的旧 session 全部失效。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "./internal/adminauth"
          ]
        },
        "dependsOn": [
          "T02"
        ],
        "risk": "密码哈希参数过低、session token 泄露、登录暴力破解。",
        "riskMitigation": [
          "PBKDF2 600k + 随机 salt",
          "session 只存哈希 + 绝对过期",
          "登录限流 + 常量时间比较",
          "改密撤销其他 session"
        ],
        "status": "ready",
        "rationale": "单管理员鉴权是管理端的第一道边界；先独立于 HTTP 实现，便于测试和复用。"
      },
      {
        "id": "T05",
        "title": "HTTP 服务器骨架、健康检查与优雅退出",
        "requirementIds": [
          "R-013",
          "R-014"
        ],
        "files": [
          "internal/server/server.go",
          "internal/server/health.go",
          "internal/server/server_test.go",
          "internal/webassets/assets.go",
          "internal/webassets/dist/index.html",
          "cmd/gateway/main.go"
        ],
        "steps": [
          "用 stdlib net/http ServeMux 组装服务器；加入 request ID、panic recover、security headers、管理面 no-store、请求体大小限制。",
          "实现 /healthz 与 /readyz；ready 检查 DB ping、schema_migrations 当前版本、主密钥加载状态。",
          "实现 SIGINT/SIGTERM：停止接受新连接，readiness 立即 503，http.Server.Shutdown 30s；超时取消全局 context。",
          "在 internal/webassets 包内用 //go:embed dist/* 嵌入 internal/webassets/dist；SPA fallback 到 index.html；T05 先放占位 index.html，T10 构建覆盖。",
          "测试健康状态、迁移未就绪、退出中的 readiness、嵌入资源。"
        ],
        "behaviorAcceptance": [
          "/healthz 200；DB 关闭或迁移版本落后时 /readyz 503，迁移完成后 200。",
          "Shutdown 开始后 /readyz 返回 503；进行中的请求在 30s 内完成或被取消。",
          "GET /admin 返回嵌入的 SPA；未知 /admin 路径 fallback 到 index.html。",
          "服务器默认监听 127.0.0.1:8080，不自动暴露公网。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "./internal/server"
          ]
        },
        "dependsOn": [
          "T02",
          "T04"
        ],
        "risk": "健康检查只返回静态 200 会误报；优雅退出等待过短会截断长流；embed 路径错误会导致构建失败。",
        "riskMitigation": [
          "readiness 检查 DB ping + 迁移版本",
          "退出前先置 503",
          "30s Shutdown + 超时取消",
          "embed 包只使用包内 dist",
          "测试覆盖未就绪/退出中/嵌入资源"
        ],
        "status": "ready",
        "rationale": "健康与退出语义必须先固定；唯一 embed 包避免 Go embed 不能跨包目录的硬限制。"
      },
      {
        "id": "T06",
        "title": "管理端登录/session API 与 CSRF 防护",
        "requirementIds": [
          "R-002",
          "R-014"
        ],
        "files": [
          "internal/adminapi/auth.go",
          "internal/adminapi/middleware.go",
          "internal/adminapi/auth_test.go",
          "internal/adminapi/middleware_test.go",
          "internal/server/server.go"
        ],
        "steps": [
          "实现 POST /admin/api/auth/login、POST /admin/api/auth/logout、GET /admin/api/auth/session、POST /admin/api/auth/password。",
          "设置 cookie：HttpOnly、SameSite=Lax、Path=/、Secure 可配、Max-Age 12h；响应 no-store。",
          "实现 admin session 中间件；实现 Origin 校验，所有 POST/PATCH/DELETE 要求同源。",
          "实现 session 列表/撤销接口（供设置页）；改密撤销其他 session。",
          "测试登录/登出/会话/CSRF/改密。"
        ],
        "behaviorAcceptance": [
          "登录成功设置 HttpOnly session cookie；GET session 返回管理员身份，logout 后返回 401。",
          "跨源 POST 返回 403；同源 POST 成功。",
          "错误密码返回 401，不泄露用户名/密码细节。",
          "改密后其他 session 失效，当前 session 继续可用。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "./internal/adminapi"
          ]
        },
        "dependsOn": [
          "T04",
          "T05"
        ],
        "risk": "CSRF、cookie 配置错误、session 固定。",
        "riskMitigation": [
          "SameSite=Lax + Origin 校验",
          "HttpOnly/Secure 可配",
          "登录后轮换 token",
          "改密撤销其他 session"
        ],
        "status": "ready",
        "rationale": "管理 API 的所有后续端点都依赖 session 中间件；先完成登录边界。"
      },
      {
        "id": "T07",
        "title": "渠道 CRUD、加密存储与配置校验",
        "requirementIds": [
          "R-003",
          "R-004"
        ],
        "files": [
          "internal/store/channels.go",
          "internal/store/channels_test.go",
          "internal/adminapi/channels.go",
          "internal/adminapi/channels_test.go",
          "internal/store/migrations/0001_init.sql",
          "internal/store/migrate_test.go"
        ],
        "steps": [
          "实现 channels 仓储：Create/Get/List/Update/Delete/SetEnabled/UpdateHealth/TouchLastUsed，Key 只写 key_ciphertext + key_mask。",
          "实现 /admin/api/channels CRUD；list/detail 永不返回 key；更新时只有显式提供 key 才重新加密。",
          "校验 base_url（http/https、无 userinfo、无 fragment）、name 唯一、protocol 枚举、priority 0..1000、max_concurrency 1..64。",
          "删除渠道时依赖 FK：model_routes 级联，request_logs/attempts channel_id 置 NULL 但保留 channel_name。",
          "测试 CRUD、密文落库、列表脱敏、非法输入、删除级联/历史保留、TouchLastUsed。",
          "在 0001_init.sql 的 channels 定义上增加 name UNIQUE（项目尚未发布，直接修改 0001 并同步更新迁移 checksum 基线；不新增迁移文件），并把仓储层唯一冲突映射为 409/400；迁移测试由 internal/store/migrate_test.go 覆盖。"
        ],
        "behaviorAcceptance": [
          "创建渠道后 SQLite 中 key_ciphertext 为密文，明文不可检索；列表/detail JSON 无 key。",
          "更新 key 后旧密文不可解、新 key 可解；不提供 key 时保持原 key。",
          "非法 base URL、protocol、并发上限返回 400。",
          "删除渠道后 model_routes 删除，历史 request_logs 保留 channel_name 且 channel_id 为 NULL；TouchLastUsed 更新 last_used_at。",
          "重复 channels.name 的创建或更新返回 409/400，数据库保留原记录。",
          "0001_init.sql 增加 name UNIQUE 后，internal/store 迁移测试通过（checksum 基线同步，fresh migrate 正常）。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "-count=1",
            "./internal/adminapi",
            "./internal/store"
          ]
        },
        "dependsOn": [
          "T02",
          "T03",
          "T06"
        ],
        "risk": "渠道 Key 可能在列表/错误/日志中泄漏；删除渠道破坏历史统计；last_used 写入影响性能。",
        "riskMitigation": [
          "仓储层统一加密/脱敏",
          "list/detail 结构体不含 key",
          "FK SET NULL + channel_name 快照",
          "TouchLastUsed 短 UPDATE",
          "测试断言 JSON/日志无明文"
        ],
        "status": "ready",
        "rationale": "渠道是路由和上游凭据的载体；先完成安全 CRUD 和删除语义，后续映射/调度/探针都依赖它。"
      },
      {
        "id": "T08",
        "title": "上游 Key 明文揭示接口",
        "requirementIds": [
          "R-003",
          "R-014"
        ],
        "files": [
          "internal/adminapi/channel_secrets.go",
          "internal/adminapi/channel_secrets_test.go",
          "internal/server/server.go"
        ],
        "steps": [
          "实现 GET /admin/api/channels/{id}/key：校验 admin session，读取并解密 key，返回 {key, mask}。",
          "设置 Cache-Control:no-store、Pragma:no-cache；不写请求日志、不记录 key。",
          "前端点击才调用；错误统一 404/500，不区分“渠道不存在/解密失败”的敏感细节。",
          "测试揭示明文、列表仍脱敏、no-store、日志无 key。"
        ],
        "behaviorAcceptance": [
          "揭示接口返回完整明文，且与创建时一致。",
          "响应头包含 no-store；列表接口仍只返回 mask。",
          "解密失败返回 500 且响应/日志不含密文或明文。",
          "无 admin session 返回 401。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "-v",
            "./internal/adminapi"
          ]
        },
        "dependsOn": [
          "T07"
        ],
        "risk": "揭示接口被滥用或浏览器缓存泄漏完整 Key。",
        "riskMitigation": [
          "admin session + no-store",
          "不写日志",
          "前端只在点击后调用",
          "测试缓存头/日志"
        ],
        "status": "ready",
        "rationale": "需求明确要求点击直接显示、不做二次密码；用 session + no-store 控制风险。"
      },
      {
        "id": "T09",
        "title": "模型映射 CRUD 与调度校验",
        "requirementIds": [
          "R-004",
          "R-005"
        ],
        "files": [
          "internal/store/routes.go",
          "internal/store/routes_test.go",
          "internal/adminapi/routes.go",
          "internal/adminapi/routes_test.go"
        ],
        "steps": [
          "实现 model_routes 仓储：Create/Update/Delete/List/ListByModelAndProtocol。",
          "实现 /admin/api/model-routes CRUD；字段 client_model、channel_id、upstream_model、priority、enabled。",
          "校验同一 client_model+channel_id 唯一；channel 必须存在；upstream_model 非空；priority 0..1000。",
          "提供 /admin/api/models：从启用的 model_routes 去重得到客户端模型列表。",
          "测试 CRUD、唯一约束、禁用过滤、模型列表。"
        ],
        "behaviorAcceptance": [
          "创建映射成功；重复 client_model+channel_id 返回 409/400。",
          "禁用映射不出现在模型列表/候选查询中。",
          "删除映射后模型若已无启用路由则从 /admin/api/models 消失。",
          "非法 channel_id/priority 被拒绝。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "-v",
            "-count=1",
            "./internal/adminapi"
          ]
        },
        "dependsOn": [
          "T07"
        ],
        "risk": "映射重复/漂移导致调度不可预测；禁用状态被忽略。",
        "riskMitigation": [
          "数据库唯一约束",
          "仓储查询统一过滤 enabled",
          "API 校验 channel 存在",
          "测试覆盖禁用/重复"
        ],
        "status": "ready",
        "rationale": "手工模型映射是 MVP 的唯一路由来源；必须先把数据约束固定。"
      },
      {
        "id": "T10",
        "title": "React 管理端骨架、登录页与设计系统",
        "requirementIds": [
          "R-002",
          "R-014"
        ],
        "files": [
          "web/package.json",
          "web/package-lock.json",
          "web/tsconfig.json",
          "web/vite.config.ts",
          "web/index.html",
          "web/src/main.tsx",
          "web/src/app/App.tsx",
          "web/src/app/App.test.tsx",
          "web/src/styles.css"
        ],
        "steps": [
          "创建 React 19 + TypeScript 5.6 + Vite 5 + TanStack Router/Query + Vitest/RTL 工程；npm install 生成 package-lock。",
          "package.json 定义 build（vite build）、test 以及 test:app/test:settings/test:channels/test:mappings/test:dashboard/test:requests/test:capture/test:debug 脚本，分别运行对应 Vitest 文件；test:debug 交给 T22 的 Node 脚本。",
          "Vite build.outDir=../internal/webassets/dist，emptyOutDir=true；确保 T29 构建后 embed 包拿到真实 UI。",
          "实现设计 token、6 项侧边栏 + 独立登录页、顶栏、内容区、响应式折叠；loading/empty/error/toast；禁止 dangerouslySetInnerHTML。",
          "测试未登录重定向、登录成功、6 个导航项、登出、package.json 声明 React/TS/Vite/Vitest。",
          "路由表只注册登录页与 6 个受保护页，不提供 /register、/payment、/topup、/rebate 路由或导航入口。"
        ],
        "behaviorAcceptance": [
          "未登录访问 / 显示登录页，不渲染管理导航。",
          "登录成功后侧边栏出现总览、渠道、模型映射、请求、调试、设置 6 项；登录页不在侧边栏，总计 7 页。",
          "登出后回到登录页，session 查询返回 401。",
          "package.json 声明 React/TypeScript/Vite/Vitest，并定义 build=vite build 脚本。",
          "路由与导航中不存在 /register、/payment、/topup、/rebate；直接访问这些路径显示 404 或重定向登录，不渲染管理页。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "npm",
          "args": [
            "run",
            "test:app"
          ]
        },
        "dependsOn": [
          "T06"
        ],
        "risk": "从零实现设计系统可能简陋或过度；依赖版本漂移；Vite 输出目录错误导致 embed 占位页。",
        "riskMitigation": [
          "固定 package-lock",
          "先做少量高质量基础组件",
          "Vite outDir 指向 internal/webassets/dist",
          "组件测试覆盖关键页面状态",
          "不引入 UI 组件库"
        ],
        "status": "ready",
        "rationale": "管理端是需求中的独立交付物；先建立壳、设计系统和正确的构建输出目录。"
      },
      {
        "id": "T11",
        "title": "管理端设置页：改密与会话撤销",
        "requirementIds": [
          "R-002",
          "R-014"
        ],
        "files": [
          "web/src/features/settings/SettingsPage.tsx",
          "web/src/features/settings/SettingsPage.test.tsx",
          "web/src/lib/api.ts"
        ],
        "steps": [
          "在 web/src/lib/api.ts 封装 fetch：credentials include、JSON、401 跳登录、no-store 处理。",
          "实现设置页：修改密码（当前密码/新密码/确认）、活跃 session 列表、撤销单个/全部、登出。",
          "密码规则前端提示 12 字符；错误显示服务端安全文案。",
          "测试改密、撤销 session、登出。"
        ],
        "behaviorAcceptance": [
          "输入正确当前密码与新密码后改密成功，其他 session 被撤销。",
          "session 列表显示创建/最后使用/过期时间；撤销后该行消失。",
          "登出按钮清除 cookie 并跳转登录。",
          "401 时统一跳登录，不渲染受保护内容。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "npm",
          "args": [
            "run",
            "test:settings"
          ]
        },
        "dependsOn": [
          "T06",
          "T10"
        ],
        "risk": "密码/session 管理 UI 可能泄露 token 或错误处理不一致。",
        "riskMitigation": [
          "只显示 session 元数据，不显示 token",
          "统一 API 401 处理",
          "测试撤销/改密"
        ],
        "status": "ready",
        "rationale": "需求要求管理员登录和 session 管理；设置页集中处理安全操作。"
      },
      {
        "id": "T12",
        "title": "管理端渠道页：脱敏 Key、揭示、复制与测试入口",
        "requirementIds": [
          "R-003",
          "R-014"
        ],
        "files": [
          "web/src/features/channels/ChannelsPage.tsx",
          "web/src/features/channels/ChannelsPage.test.tsx",
          "web/src/features/channels/api.ts",
          "web/src/features/channels/types.ts"
        ],
        "steps": [
          "实现渠道列表：名称、base URL、协议、优先级、并发、启停、健康状态、mask。",
          "实现创建/编辑抽屉：base URL、协议、key（编辑时可留空）、优先级、并发、启用；提交后刷新。",
          "实现点击揭示：调用 key 接口，在只读输入框显示完整 key + 复制按钮；离开页面/关闭抽屉后清除。",
          "实现删除确认、启停开关、测试按钮（调用 T22 的调试 API，失败显示脱敏错误）。",
          "测试初始 mask、揭示、复制、创建校验、启停、测试按钮失败脱敏。"
        ],
        "behaviorAcceptance": [
          "列表初始不显示完整 key；点击后只显示当前渠道完整 key，关闭后清除。",
          "创建非法 URL/并发 0 时显示字段错误，不发请求。",
          "启停后列表状态更新；删除需二次确认。",
          "测试按钮失败时显示脱敏错误，不显示上游 key。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "npm",
          "args": [
            "run",
            "test:channels"
          ]
        },
        "dependsOn": [
          "T07",
          "T08",
          "T10",
          "T11",
          "T22"
        ],
        "risk": "完整 Key 可能停留在 DOM/状态中，或被错误信息带回；测试按钮依赖 T22 的调试 API。",
        "riskMitigation": [
          "只在点击后请求",
          "关闭即清除状态",
          "错误统一脱敏",
          "T22 先完成调试 API",
          "测试断言初始 DOM 无完整 key"
        ],
        "status": "ready",
        "rationale": "渠道页是管理端最核心的运维入口；Key 展示和测试按钮必须与后端安全/调试接口一起验收。"
      },
      {
        "id": "T13",
        "title": "管理端模型映射页",
        "requirementIds": [
          "R-004",
          "R-014"
        ],
        "files": [
          "web/src/features/mappings/MappingsPage.tsx",
          "web/src/features/mappings/MappingsPage.test.tsx",
          "web/src/features/mappings/api.ts"
        ],
        "steps": [
          "实现映射表格：客户端模型、渠道、上游模型、优先级、启停、更新时间。",
          "实现创建/编辑：模型下拉来自 /admin/api/models 与渠道列表；priority/enabled；重复映射提示。",
          "按客户端模型分组展示；支持筛选渠道/模型。",
          "测试创建、重复冲突、启停、空态。"
        ],
        "behaviorAcceptance": [
          "创建映射后表格出现新行，字段与输入一致。",
          "重复 client_model+channel 显示冲突错误，不新增行。",
          "禁用映射后状态展示为禁用，模型列表刷新后不再包含无启用路由的模型。",
          "无映射时显示空态并引导创建。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "npm",
          "args": [
            "run",
            "test:mappings"
          ]
        },
        "dependsOn": [
          "T09",
          "T10",
          "T11"
        ],
        "risk": "映射表单字段多，容易出现渠道/模型不一致。",
        "riskMitigation": [
          "后端唯一约束兜底",
          "表单联动校验",
          "测试冲突/空态"
        ],
        "status": "ready",
        "rationale": "手工映射是 MVP 的路由来源；管理 UI 必须能可靠维护。"
      },
      {
        "id": "T14",
        "title": "请求日志、统计查询与管理 API",
        "requirementIds": [
          "R-011",
          "R-013"
        ],
        "files": [
          "internal/observability/record.go",
          "internal/observability/query.go",
          "internal/observability/observability_test.go",
          "internal/adminapi/observability.go",
          "internal/adminapi/observability_test.go"
        ],
        "steps": [
          "实现请求日志/attempt 写入：request_id、protocol、endpoint、model、channel_name/id、status、error、stream、attempt_count、ttft、latency、nullable tokens、usage_source。",
          "实现查询：时间范围、模型、渠道、状态、分页；详情返回 attempts 与错误。",
          "实现统计：请求数、成功/错误率、输入/输出 token、平均/P95 延迟，按小时/模型/渠道聚合。",
          "实现 90 天日志保留清理任务；索引覆盖 started_at/channel/model/status。",
          "测试只写元数据、usage 缺失为空、筛选、错误率、P95。"
        ],
        "behaviorAcceptance": [
          "请求日志不包含 header/body/Authorization/prompt。",
          "统计 API 返回请求数、错误率、输入/输出 token、平均/P95 延迟，并可按模型/渠道过滤。",
          "usage 缺失时 token 字段为 null，统计 sum 不把 null 当 0 伪装成已知值。",
          "超过保留期的日志被清理，捕获表不受影响。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "./internal/observability"
          ]
        },
        "dependsOn": [
          "T02",
          "T06"
        ],
        "risk": "SQLite 写放大/统计慢；日志误存 body 或 header。",
        "riskMitigation": [
          "短事务 + 单写连接",
          "只存元数据 + 索引",
          "nullable token",
          "测试断言无 body/header"
        ],
        "status": "ready",
        "rationale": "观测和管理端数据源依赖统一日志/统计查询；先完成存储与 API，再做页面。"
      },
      {
        "id": "T15",
        "title": "管理端总览 Dashboard",
        "requirementIds": [
          "R-011",
          "R-014"
        ],
        "files": [
          "web/src/features/dashboard/DashboardPage.tsx",
          "web/src/features/dashboard/DashboardPage.test.tsx",
          "web/src/features/dashboard/charts.tsx"
        ],
        "steps": [
          "实现时间范围选择（24h/7d/30d）、四张指标卡（请求数、token、平均/P95 延迟、错误率）。",
          "实现按小时趋势的轻量 SVG 折线/柱状图，以及按渠道/模型 Top 表。",
          "实现 loading/empty/error；错误显示重试。",
          "测试指标渲染、筛选请求、空态、错误态。"
        ],
        "behaviorAcceptance": [
          "默认 24h 数据渲染四张指标卡和趋势图。",
          "切换时间范围会带新参数请求；空数据展示空态而非 NaN。",
          "渠道/模型 Top 表可跳转请求页并带上筛选。",
          "统计错误显示可重试错误，不白屏。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "npm",
          "args": [
            "run",
            "test:dashboard"
          ]
        },
        "dependsOn": [
          "T10",
          "T11",
          "T14"
        ],
        "risk": "图表库引入过大；统计口径与后端不一致。",
        "riskMitigation": [
          "使用自绘 SVG，不加图表库",
          "API 契约测试/类型",
          "空态/错误态覆盖"
        ],
        "status": "ready",
        "rationale": "总览是管理端门面；先做轻量可维护的可视化，不引入图表依赖。"
      },
      {
        "id": "T16",
        "title": "管理端请求观测列表与详情",
        "requirementIds": [
          "R-011",
          "R-014"
        ],
        "files": [
          "web/src/features/requests/RequestsPage.tsx",
          "web/src/features/requests/RequestsPage.test.tsx",
          "web/src/features/requests/RequestDetail.tsx"
        ],
        "steps": [
          "实现筛选：时间、模型、渠道、状态、错误码；分页表格显示请求数/token/延迟/渠道/状态。",
          "实现详情抽屉：元数据、usage、attempts 列表、错误 code/message、semantic_committed、capture 可用提示。",
          "默认不展示 body；捕获按钮由 T25 接入。",
          "测试筛选参数、分页、详情 attempts、空态/错误。"
        ],
        "behaviorAcceptance": [
          "列表按时间倒序，筛选后请求带对应 query 参数。",
          "详情显示每次 attempt 的渠道、状态、延迟与错误，不显示 body/header。",
          "usage 缺失显示 —；错误请求有可读错误摘要。",
          "无数据时显示空态，不是空白表格。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "npm",
          "args": [
            "run",
            "test:requests"
          ]
        },
        "dependsOn": [
          "T10",
          "T11",
          "T14"
        ],
        "risk": "日志量大导致表格卡顿；详情误展示敏感数据。",
        "riskMitigation": [
          "服务端分页 + 索引",
          "只展示元数据",
          "body 入口只走捕获接口"
        ],
        "status": "ready",
        "rationale": "请求观测是排障核心；先完成元数据视图，body 捕获作为独立任务。"
      },
      {
        "id": "T17",
        "title": "客户端鉴权、入站路由、错误 envelope 与模型列表",
        "requirementIds": [
          "R-001",
          "R-008",
          "R-015",
          "R-017"
        ],
        "files": [
          "internal/gateway/auth.go",
          "internal/gateway/errors.go",
          "internal/gateway/router.go",
          "internal/gateway/models.go",
          "internal/gateway/router_test.go"
        ],
        "steps": [
          "注册 POST /v1/responses、/backend-api/codex/responses、POST /v1/chat/completions、GET /v1/models、/v1/models/{model}、/backend-api/codex/models。",
          "实现单客户端 Key 鉴权：Authorization Bearer / x-api-key，SHA-256 + constant-time；返回统一 401。",
          "实现入站协议识别与 endpoint 常量；不支持路径 404，不注册 embeddings/images/audio/video。",
          "实现 OpenAI/Responses 错误 envelope；未知上游错误不泄露 key/URL；无路由/协议不匹配返回 404 model_not_available。",
          "实现普通模型列表与 Codex manifest 占位（T26 完成细节），从启用映射取模型。",
          "测试鉴权、路由、404、错误形状、模型列表、无映射/协议不匹配不调用上游。"
        ],
        "behaviorAcceptance": [
          "错误/缺失客户端 Key 返回 401；正确 Key 进入 handler。",
          "/v1/responses、/v1/chat/completions、/backend-api/codex/responses 可用；/v1/embeddings 返回 404。",
          "/v1/models 只列启用的客户端模型；带 client_version 走 manifest 分支；无映射/协议不匹配返回 model_not_available。",
          "错误响应为 {error:{message,type,code}}，不含上游 key。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "./internal/gateway"
          ]
        },
        "dependsOn": [
          "T02",
          "T05",
          "T09"
        ],
        "risk": "路由/错误形状不兼容 Codex；客户端 Key 时序/泄露；无映射错误码不一致。",
        "riskMitigation": [
          "endpoint 常量集中",
          "协议专属错误构造器",
          "constant-time compare",
          "测试 401/404/错误体",
          "model_not_available 统一"
        ],
        "status": "ready",
        "rationale": "客户端入口是所有数据面请求的第一层；先固定 surface、错误契约和 model_not_available 语义。"
      },
      {
        "id": "T18",
        "title": "上游 HTTP client 与 Responses/Chat 原生 adapter",
        "requirementIds": [
          "R-008",
          "R-010",
          "R-015"
        ],
        "files": [
          "internal/upstream/client.go",
          "internal/upstream/responses.go",
          "internal/upstream/chat.go",
          "internal/upstream/probe.go",
          "internal/upstream/client_test.go"
        ],
        "steps": [
          "实现 http.Transport 超时（连接 10s、TLS 10s、响应头 180s）、流式读空闲 300s、非流式总 300s、流式总 15m；拒绝跨 host 重定向。",
          "实现 base URL 校验与路径拼接；Responses → /responses，Chat → /chat/completions；Authorization: Bearer 上游 key。",
          "请求体用 map[string]json.RawMessage 仅替换 model；保留 tools/input_image/reasoning/未知字段；过滤客户端 Authorization/Cookie/hop-by-hop。",
          "实现 probe：最小 Responses/Chat 请求，返回状态/延迟/脱敏错误；更新渠道 health。",
          "测试路径/鉴权/字段保留/超时/重定向/key 不泄漏。",
          "过滤上游响应头：只转发安全头（如 content-type、cache-control），移除 Set-Cookie 与 Connection/Transfer-Encoding/Upgrade/Keep-Alive/Trailer 等 hop-by-hop 头。"
        ],
        "behaviorAcceptance": [
          "Responses adapter 请求命中 {base_url}/responses，Chat 命中 /chat/completions。",
          "请求中的 tools、input_image、reasoning 与未知字段在发送后仍存在；仅 model 被替换。",
          "跨 host 302 被拒绝；超时返回可分类错误；错误不含上游 key。",
          "probe 成功/失败返回状态与延迟，不记录 key。",
          "上游响应的 Set-Cookie 与 hop-by-hop 头不出现在客户端响应中，content-type 正常保留。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "./internal/upstream"
          ]
        },
        "dependsOn": [
          "T01",
          "T07"
        ],
        "risk": "路径拼接错误、重定向泄漏 Authorization、超时设置不当。",
        "riskMitigation": [
          "base URL 规范校验",
          "CheckRedirect 限制",
          "字段级 passthrough 测试",
          "超时/错误分类测试"
        ],
        "status": "ready",
        "rationale": "上游 adapter 是数据面唯一出站边界；先保证原生直通不丢字段，后续调度/流式复用。"
      },
      {
        "id": "T19",
        "title": "候选调度、优先级、健康熔断与并发槽位",
        "requirementIds": [
          "R-005",
          "R-006"
        ],
        "files": [
          "internal/routing/scheduler.go",
          "internal/routing/scheduler_test.go",
          "internal/routing/limiter.go",
          "internal/routing/limiter_test.go"
        ],
        "steps": [
          "实现候选查询：client_model + 入站协议、enabled、channel enabled、circuit closed、容量可用。",
          "实现排序：route.priority ASC、channel.priority ASC、last_used_at ASC、id ASC；无权重/随机；获得槽位后调用 store.TouchLastUsed。",
          "实现每渠道信号量（chan struct{}）与 TryAcquire/Release；全部满载时 2s 等待，25ms 轮询，超时返回 ErrChannelCapacity。",
          "实现内存熔断：401/403 打开 5m，429 打开 10s，5xx 打开 20s，成功重置；记录 last_error。",
          "测试过滤/排序/并发不超限/等待 429/槽位释放/协议不匹配不选/ last_used 更新。"
        ],
        "behaviorAcceptance": [
          "禁用/channel disabled/circuit open/协议不匹配的渠道不被选中。",
          "按 route.priority 再 channel.priority 再 last_used_at 排序；TouchLastUsed 后顺序变化。",
          "并发 N 个请求不超过 max_concurrency；流式取消后槽位可再次获取。",
          "全部满载时等待约 2s 后返回 429 channel_capacity_exhausted；有槽位释放则请求继续。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "./internal/routing"
          ]
        },
        "dependsOn": [
          "T09",
          "T18"
        ],
        "risk": "并发竞态、槽位泄漏、熔断过于激进、last_used 写入增加锁竞争。",
        "riskMitigation": [
          "信号量 + defer Release",
          "表驱动排序测试",
          "并发测试",
          "熔断窗口可配置",
          "TouchLastUsed 短 UPDATE"
        ],
        "status": "ready",
        "rationale": "调度与容量是稳定性的核心；last_used_at 持久化后排序有明确落点。"
      },
      {
        "id": "T20",
        "title": "非流式失败切换、重试策略与 attempt 记录",
        "requirementIds": [
          "R-005",
          "R-017"
        ],
        "files": [
          "internal/gateway/proxy.go",
          "internal/gateway/proxy_test.go",
          "internal/gateway/attempt.go",
          "internal/gateway/attempt_test.go"
        ],
        "steps": [
          "实现非流式请求编排：取候选、TryAcquire、构建上游请求、发送、分类状态、释放、排除失败渠道。",
          "重试网络/401/403/404/408/429/5xx/529；不重试 400/413/415/422；最多 3 个不同渠道，不重同渠道。",
          "按入站协议构造错误 envelope；上游 401/403→502，429→429，5xx→502/503，超时→504；无兼容渠道返回 404 model_not_available 且上游调用 0 次。",
          "每次 attempt 写 channel、status、latency、error、semantic_committed；最终写 request_log。",
          "测试 503→200、401→200、400 不重试、全部失败、无映射/协议不匹配 0 上游调用、key 不泄漏。"
        ],
        "behaviorAcceptance": [
          "首个渠道 503、第二个 200 时客户端得 200，attempt 记录 2 次。",
          "首个渠道 400 时不尝试第二个，返回 400；首个 401 时尝试下一个。",
          "无启用映射或只有 openai_chat 渠道收到 Responses 时返回 404 model_not_available，上游调用次数为 0。",
          "所有渠道失败返回最后一次归一化错误，不含上游 key/URL；attempts 顺序/渠道/状态与真实请求一致。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "-count=1",
            "./internal/gateway"
          ]
        },
        "dependsOn": [
          "T14",
          "T17",
          "T18",
          "T19"
        ],
        "risk": "错误分类错误导致重复/不必要切换；重试放大成本；协议不匹配误调用上游。",
        "riskMitigation": [
          "状态码白名单/黑名单测试",
          "最多 3 渠道",
          "每次尝试日志",
          "协议过滤测试",
          "错误脱敏测试"
        ],
        "status": "ready",
        "rationale": "非流式路径先验证调度/重试/错误转换，再复用到流式；显式验收 model_not_available。"
      },
      {
        "id": "T21",
        "title": "SSE 解析、首内容提交门控与提交后错误",
        "requirementIds": [
          "R-007",
          "R-008",
          "R-010"
        ],
        "files": [
          "internal/gateway/stream.go",
          "internal/gateway/stream_test.go",
          "internal/upstream/sse.go",
          "internal/upstream/sse_test.go"
        ],
        "steps": [
          "实现 SSE 解析：保留 event/id/data/注释原始字节，支持多行 data、CRLF/LF；单事件大小上限；流空闲超时。",
          "实现语义分类：Responses 的 response.output_text.delta/function_call_arguments.delta/completed/incomplete；Chat 的 choices.delta.content/tool_calls/reasoning_content/finish_reason/[DONE]。",
          "实现缓冲：首语义内容前不写出；错误/EOF 前可换渠道；提交后写缓冲并 flush，后续只转发。",
          "实现提交后错误：Responses 只发一次完整 response.failed（type、sequence_number、response.id/object/created_at/model/status/output/error）；Chat 发 error + [DONE]；不发起第二个上游。",
          "测试 503→SSE、首内容前断流重试、首内容后断流终止、response.failed 字段契约、客户端取消。"
        ],
        "behaviorAcceptance": [
          "流开始后上游首个事件是 error/断开时，客户端未收到语义内容，网关可换渠道。",
          "首个文本/tool delta 后上游断开，Responses 客户端只收到一次 response.failed，且含 sequence_number 与 response.created_at，attempts 中无第二个渠道。",
          "Chat 提交后错误收到 error 对象 + [DONE]，HTTP 200。",
          "客户端断开时上游 context 取消，attempt 标记取消，不重试。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "-v",
            "./internal/gateway"
          ]
        },
        "dependsOn": [
          "T18",
          "T20"
        ],
        "risk": "事件分类误判、缓冲内存过大、response.failed 字段缺失被严格客户端拒绝。",
        "riskMitigation": [
          "协议帧分类表驱动测试",
          "事件/缓冲大小上限",
          "完整 response.failed 契约测试",
          "提交后不重试断言"
        ],
        "status": "ready",
        "rationale": "流式行为是 Codex/网关稳定性的核心；必须锁定“能否重试”和严格终止事件字段。"
      },
      {
        "id": "T22",
        "title": "调试 API 与管理端调试页",
        "requirementIds": [
          "R-003",
          "R-014",
          "R-017"
        ],
        "files": [
          "internal/adminapi/debug.go",
          "internal/adminapi/debug_test.go",
          "web/src/features/debug/DebugPage.tsx",
          "web/src/features/debug/DebugPage.test.tsx",
          "web/scripts/test-debug.mjs"
        ],
        "steps": [
          "实现 POST /admin/api/debug/channels/{id}/test，body 可选 model；选择 route/channel，调用 upstream probe，返回 status/latency/脱敏错误，更新渠道 health。",
          "实现 POST /admin/api/debug/models/{model}/test（或同一端点带 model）使用第一个候选渠道发最小请求。",
          "实现调试页：选择渠道/模型，运行测试，显示状态、延迟、错误、最近请求链接；提示会消耗上游 tokens。",
          "新增 web/scripts/test-debug.mjs：用 Node spawnSync（无 shell）依次运行 repo 根的 go test -cover ./internal/adminapi 和 web 的 vitest run src/features/debug/DebugPage.test.tsx；任一失败则非零退出。",
          "测试成功/失败/超时/错误脱敏，并用 npm run test:debug 同时验证前后端。",
          "保留 npm run test:debug 为唯一验收：它是 package.json 中的 npm 脚本，Node spawnSync 直接启动 go/npm、不经过 shell，属于 strict 白名单内的 argv 命令；不拆分任务或验收。"
        ],
        "behaviorAcceptance": [
          "渠道测试成功显示 200 与延迟；失败显示脱敏错误，不含 key。",
          "模型测试只使用该模型已启用的映射渠道；无映射时返回明确错误。",
          "测试结果更新渠道 last_health_status/latency/at；错误详情可跳转请求页。",
          "npm run test:debug 同时通过 internal/adminapi 的 Go 测试和 DebugPage 的 Vitest。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "npm",
          "args": [
            "run",
            "test:debug"
          ]
        },
        "dependsOn": [
          "T07",
          "T10",
          "T11",
          "T14",
          "T18"
        ],
        "risk": "调试请求会消耗上游额度；错误可能泄露上游信息；跨栈测试脚本复杂。",
        "riskMitigation": [
          "UI 明确提示消耗 tokens",
          "最小 probe payload",
          "错误统一脱敏",
          "Node spawnSync 无 shell",
          "测试断言无 key"
        ],
        "status": "ready",
        "rationale": "需求要求渠道连通性/模型测试和错误详情；T22 的单一 argv 同时覆盖后端 debug API 与前端调试页。npm run test:debug 是项目 npm 脚本，Node spawnSync 无 shell，属于 strict 白名单。"
      },
      {
        "id": "T23",
        "title": "usage 提取与请求日志集成",
        "requirementIds": [
          "R-011",
          "R-012"
        ],
        "files": [
          "internal/gateway/observability.go",
          "internal/gateway/observability_test.go",
          "internal/observability/usage.go",
          "internal/observability/usage_test.go"
        ],
        "steps": [
          "在非流式/流式路径解析 usage：Responses response.completed/failed 的 response.usage；Chat 非流式 usage 与流式 final chunk usage。",
          "记录 request_log：status、http_status、error_code、stream、attempt_count、retry_count、ttft、latency、input/output/total token、usage_source、semantic_committed、response_bytes。",
          "写 attempts（每次渠道尝试）；确保一次请求只写一条 request_log；客户端断开记录 499。",
          "测试 Responses/Chat usage、缺失为 null、流式错误、attempts 数量。"
        ],
        "behaviorAcceptance": [
          "Responses completed 带 usage 时 request_log token 与上游一致；缺失时 null。",
          "Chat 流式 final usage 可用时记录；没有 usage 时不估算。",
          "一次请求只生成一条 request_log，attempt 数与实际渠道尝试一致。",
          "默认日志无 header/body/prompt；semantic_committed 正确。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "-cover",
            "./internal/gateway"
          ]
        },
        "dependsOn": [
          "T14",
          "T20",
          "T21"
        ],
        "risk": "流式 usage 解析失败导致统计缺失；重复写日志。",
        "riskMitigation": [
          "协议级 usage 解析器",
          "缺失 nullable",
          "一次请求单 writer",
          "测试 completed/failed/无 usage"
        ],
        "status": "ready",
        "rationale": "观测数据必须在真实请求路径中产生；先完成 usage 与 request_log 集成，再做捕获。"
      },
      {
        "id": "T24",
        "title": "内存诊断环与按需 body 捕获后端",
        "requirementIds": [
          "R-012"
        ],
        "files": [
          "internal/observability/capture.go",
          "internal/observability/capture_test.go",
          "internal/adminapi/capture.go",
          "internal/adminapi/capture_test.go"
        ],
        "steps": [
          "实现请求/响应 body 的诊断环：按 request_id 保存最近 5 分钟/50 请求/32MB；单侧上限 8MB，超出 truncated；默认不写 SQLite。",
          "在请求结束时把 request/response 字节 tee 到诊断环；不存 header；流式保存转发后的原始 SSE 字节。",
          "实现 GET /admin/api/requests/{id}/capture/status、POST /admin/api/requests/{id}/capture（确认风险）保存、GET 读取、DELETE 删除；status/read 响应加 Cache-Control:no-store、Pragma:no-cache。",
          "保存时 AES-GCM 加密 request/response body，TTL 24h；后台每 10 分钟清理过期。",
          "测试默认无捕获、环过期/驱逐、truncated、加密、TTL、删除、no-store。"
        ],
        "behaviorAcceptance": [
          "默认请求完成不产生 request_captures 行；诊断环 5 分钟后不可用。",
          "管理员保存后 DB 只有密文，读取解密内容与原始 body 一致（<=8MB）。",
          "超过 8MB 标记 truncated，捕获读取返回 truncated 标志；status/read 响应含 no-store。",
          "删除/过期后读取 404；不保存 Authorization/Cookie header。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "-count=1",
            "./internal/observability"
          ]
        },
        "dependsOn": [
          "T03",
          "T14",
          "T23"
        ],
        "risk": "内存环保存 prompt 造成隐私暴露；捕获表长期留存敏感数据；响应缓存。",
        "riskMitigation": [
          "默认不落库 + 5 分钟 TTL/32MB 上限",
          "保存前风险确认",
          "AES-GCM 加密 + 24h TTL",
          "可删除 + 后台清理",
          "不存 header",
          "status/read no-store"
        ],
        "status": "ready",
        "rationale": "需求要求默认仅元数据、按需抓取；内存环+显式保存同时满足事后查看与不默认落库。"
      },
      {
        "id": "T25",
        "title": "请求详情中的 body 捕获 UI",
        "requirementIds": [
          "R-012",
          "R-014"
        ],
        "files": [
          "web/src/features/requests/CaptureDialog.tsx",
          "web/src/features/requests/CaptureDialog.test.tsx",
          "web/src/features/requests/RequestDetail.tsx"
        ],
        "steps": [
          "在请求详情中根据 capture/status 显示“抓取 body”按钮；不可用时说明已过期。",
          "点击弹出风险提示：包含 prompt/response 敏感信息、24h 后自动删除；确认后调用保存接口。",
          "保存后展示请求/响应 body（truncated 提示）、复制/下载、删除；不做 HTML 渲染。",
          "测试可用/过期/确认/保存/删除/truncated。"
        ],
        "behaviorAcceptance": [
          "默认详情不显示 body；只有诊断环可用时显示抓取按钮。",
          "未确认风险不调用保存接口；确认后显示内容。",
          "truncated 显示提示；删除后按钮回到不可用。",
          "body 以纯文本展示，不执行 HTML/脚本。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "npm",
          "args": [
            "run",
            "test:capture"
          ]
        },
        "dependsOn": [
          "T16",
          "T24"
        ],
        "risk": "用户误存敏感 prompt；UI 渲染 XSS。",
        "riskMitigation": [
          "显式风险确认",
          "纯文本/等宽展示",
          "TTL + 删除",
          "不 dangerouslySetInnerHTML"
        ],
        "status": "ready",
        "rationale": "捕获是敏感操作，必须由用户在请求详情中明确触发并看到风险。"
      },
      {
        "id": "T26",
        "title": "Codex 模型发现与完整 ModelInfo manifest",
        "requirementIds": [
          "R-008",
          "R-004"
        ],
        "files": [
          "internal/gateway/codex_models.go",
          "internal/gateway/codex_models_test.go",
          "internal/gateway/router.go"
        ],
        "steps": [
          "实现 GET /v1/models 普通 OpenAI list；带 client_version 查询或 /backend-api/codex/models 返回 Codex manifest。",
          "manifest 从启用的 model_routes 去重生成完整 ModelInfo descriptor：slug/display_name/description/default_reasoning_level/supported_reasoning_levels/multi_agent_reasoning_effort/shell_type/visibility/supported_in_api/priority/additional_speed_tiers/service_tiers/default_service_tier/availability_nux/upgrade/model_messages/include_*_usage_instructions/supports_reasoning_summary_parameter/default_reasoning_summary/support_verbosity/default_verbosity/apply_patch_tool_type/web_search_tool_type/truncation_policy/supports_image_detail_original/supports_parallel_tool_calls/context_window/max_context_window/auto_compact_token_limit/comp_hash/effective_context_window_percent/experimental_supported_tools/input_modalities/supports_search_tool/use_responses_lite/node_repl_*/auto_review_model_override/model_specialty/tool_mode/multi_agent_version。",
          "context_window 使用 GATEWAY_CODEX_CONTEXT_WINDOW 默认 128000；input_modalities 默认 [text,image]；no-store；不访问上游模型目录。",
          "测试普通 list、完整字段契约、空列表、禁用模型过滤、content-type/no-store。"
        ],
        "behaviorAcceptance": [
          "普通 GET /v1/models 返回 OpenAI 风格 object=list/data。",
          "带 client_version 返回 Codex manifest，slug 与启用的客户端模型一致。",
          "manifest 每个模型含完整字段（含 supported_reasoning_levels、context_window、input_modalities、model_messages 等），无缺字段；空列表合法。",
          "manifest 响应 no-store，且不包含上游模型名/渠道 key。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "-v",
            "-count=1",
            "./internal/gateway"
          ]
        },
        "dependsOn": [
          "T17"
        ],
        "risk": "Codex manifest 字段缺失/类型错误导致 0.164.0 解析失败或模型选择器为空。",
        "riskMitigation": [
          "按 sub2api/juhe-ai 的完整 descriptor 字段实现",
          "字段级 contract 测试",
          "真实 Codex E2E",
          "可配置 context window",
          "空列表返回合法结构"
        ],
        "status": "ready",
        "rationale": "Codex 模型选择器依赖完整 ModelInfo；最小 {slug,display_name} 已被参考实现注明不足。"
      },
      {
        "id": "T27",
        "title": "真实 Codex CLI 的 Responses E2E",
        "requirementIds": [
          "R-008",
          "R-010",
          "R-013"
        ],
        "files": [
          "e2e/codex_test.go",
          "e2e/harness_test.go",
          "e2e/fake_upstream_test.go",
          "e2e/testdata/codex-0.164.0/config.toml.tmpl"
        ],
        "steps": [
          "Go E2E 启动临时 SQLite、网关、假上游；写临时 CODEX_HOME/config.toml，base_url 指向网关 /v1，wire_api=responses，supports_websockets=false，requires_openai_auth=false，experimental_bearer_token 为测试 key；模板不设置 model_catalog_json，并使用不在 Codex 内置目录中的自定义模型名（由 harness seed 的 model_routes 提供）。",
          "E2E 文件是普通测试包，不加 build tag；go test ./e2e 即运行。preflight 断言 harness seed 中至少一个启用的 openai_responses 渠道；无则失败。真实上游可用性由 A-04/Q-UPSTREAM-NATIVE 覆盖，不在 E2E 内探测外网。运行真实 codex exec --json --skip-git-repo-check --ephemeral -C <temp> --sandbox read-only；断言 codex --version 为 0.164.x，否则失败。",
          "子测试覆盖：纯文本、-i 图片输入、两轮 tool calling（假上游返回工具调用，Codex 回传 tool output，再返回最终文本）、模型发现、提交后断流（网关发完整 response.failed，不换渠道）。",
          "记录网关收到的 method/path/headers，断言命中 /v1/responses 与 /v1/models，未出现 WebSocket 升级；若出现 compact/其他路径，测试失败并记录。",
          "假上游不访问外网；超时 120s。"
        ],
        "behaviorAcceptance": [
          "codex exec 文本请求成功，输出包含假上游指定的最终文本。",
          "带 -i 的请求在假上游 body 中出现 input_image。",
          "tool calling 两轮完成：第一次返回工具调用，第二次收到 tool output 并返回最终文本。",
          "模型发现请求返回完整 manifest；提交后断流只出现 response.failed；未观察到 WebSocket；harness seed 缺少 openai_responses 渠道时 preflight 失败；codex 版本不是 0.164.x 时失败而不是 skip；自定义模型名触发 /v1/models 请求，未被 model_catalog_json 绕过。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "./e2e"
          ]
        },
        "dependsOn": [
          "T18",
          "T21",
          "T26"
        ],
        "risk": "Codex 版本漂移、Windows 子进程/端口、模型发现/工具调用形状不兼容、harness seed 缺少 openai_responses 渠道。",
        "riskMitigation": [
          "固定 0.164.x 并在不匹配时失败",
          "临时 CODEX_HOME 隔离用户配置",
          "假上游本地端口 + 120s 超时",
          "E2E 记录异常 endpoint",
          "preflight 检查 harness seed 的 openai_responses 渠道",
          "普通测试包，go test ./e2e 直接运行",
          "模板不设置 model_catalog_json 且使用自定义模型名"
        ],
        "status": "ready",
        "rationale": "需求要求 Codex 实际接入验收；模拟客户端不足以证明请求形状、完整 manifest 和提交后终止事件兼容。"
      },
      {
        "id": "T28",
        "title": "ZCode 入站协议取证与适配",
        "requirementIds": [
          "R-009"
        ],
        "files": [
          "docs/protocols/zcode-capture.md",
          "internal/gateway/zcode.go",
          "internal/upstream/zcode.go",
          "e2e/zcode_test.go"
        ],
        "steps": [
          "第一版不实现本任务；以下为 MVP+1 范围，仅在取得协议证据后启动。",
          "取得 ZCode 版本、安装包/可执行文件和真实请求抓包；记录 endpoint、method、鉴权头、body、SSE 事件、图片与 tool 字段。",
          "核对 ZCode 是否使用 OpenAI Chat、Responses、Anthropic Messages 或私有协议；与现有 adapter 边界对齐。",
          "仅在证据确认后实现入站/上游 adapter；不猜测字段，不伪造动态签名/设备字段。",
          "增加真实 ZCode E2E，覆盖文本、图片、tool calling、流式终止；记录版本。",
          "若证据仍不足，保持 needs-info，不进入实现。"
        ],
        "behaviorAcceptance": [],
        "acceptanceKind": "none",
        "acceptance": null,
        "acceptanceCommand": null,
        "dependsOn": [
          "T17",
          "T18",
          "T21"
        ],
        "risk": "ZCode 协议未知，任何猜测都会静默丢图片/tool/流式字段；ZCode 动态签名字段可能无法伪造。",
        "riskMitigation": [
          "先抓包/官方协议，再实现",
          "版本固定并记录",
          "真实 E2E 覆盖四类能力",
          "无证据不写 adapter"
        ],
        "status": "needs-info",
        "rationale": "用户已批准 ZCode 延后至 MVP+1；四个参考仓库未发现 ZCode 入站协议证据，juhe-ai 的命中集中在出站身份/版本跟踪。T28 保持 needs-info，MVP+1 取证后再实现，不得猜测。"
      },
      {
        "id": "T29",
        "title": "Linux/Docker 打包与可迁移数据目录",
        "requirementIds": [
          "R-013",
          "R-016"
        ],
        "files": [
          "Dockerfile",
          ".dockerignore",
          "docs/deploy.md",
          "internal/server/embed_test.go",
          "internal/server/dockerfile_test.go"
        ],
        "steps": [
          "验收前置：在 web 目录执行 npm run build，生成真实的 internal/webassets/dist（含 hashed assets）；随后运行唯一 argv 验收，避免只验证 T05 占位页。",
          "多阶段构建：Node 构建 web 并输出 internal/webassets/dist，Go 构建单二进制并嵌入；运行镜像含 ca-certificates、非 root 用户。",
          "挂载 /data 卷并设置 ENV GATEWAY_DATA_DIR=/data；默认监听 0.0.0.0:8080；环境变量注入 master key/client key/admin password；不把 secret 写入镜像。",
          "docs/deploy.md 写明本机运行、Docker 运行、反向代理 TLS、备份/恢复、迁移失败处理；真实 docker build 为发布前人工验收。",
          "Go 测试验证嵌入 SPA 可服务、GATEWAY_DATA_DIR 可重定位；dockerfile_test 读取 Dockerfile 断言 USER 非 root、VOLUME /data、ENV GATEWAY_DATA_DIR=/data、EXPOSE 8080、无 secret 字面量；embed_test 断言 index.html 含 Vite hashed asset 引用；若仍是 T05 静态占位页（无 hashed asset 引用），测试以明确错误要求先执行 npm run build。",
          "确保 T29 依赖全部 UI 任务，Docker 构建时 web 源码完整。"
        ],
        "behaviorAcceptance": [
          "嵌入 SPA 可服务；GATEWAY_DATA_DIR 指向临时目录时数据库/备份都落在该目录。",
          "embed_test 断言 internal/webassets/dist/index.html 是 npm run build 的真实产物（引用 /assets/index-<hash>.js）；若仍是 T05 静态占位页（无 hashed asset 引用），测试以明确错误要求先执行 npm run build。",
          "Dockerfile 契约测试断言非 root、VOLUME /data、ENV GATEWAY_DATA_DIR=/data、EXPOSE 8080，且不含 GATEWAY_MASTER_KEY/客户端 Key/管理员密码字面量。",
          "服务默认监听可在 127.0.0.1 与 0.0.0.0 间配置。",
          "文档中的环境变量与代码默认一致；真实 docker build 明确为人工验收，不声称已运行。"
        ],
        "acceptanceKind": "argv",
        "acceptance": null,
        "acceptanceCommand": {
          "program": "go",
          "args": [
            "test",
            "-v",
            "./internal/server"
          ]
        },
        "dependsOn": [
          "T05",
          "T07",
          "T10",
          "T11",
          "T12",
          "T13",
          "T15",
          "T16",
          "T22",
          "T25"
        ],
        "risk": "Windows 路径/文件权限在 Linux 失效；镜像泄露 secret；容器数据未持久化；Docker 构建未在 argv 中真实执行。嵌入资源可能仍是占位页。",
        "riskMitigation": [
          "路径使用 filepath.Join + GATEWAY_DATA_DIR",
          "多阶段构建不复制 .env",
          "文档挂载卷",
          "非 root 运行",
          "Dockerfile 静态契约测试",
          "真实 docker build 发布前人工验收",
          "验收前 npm run build，embed_test 断言真实构建资产而非占位页"
        ],
        "status": "ready",
        "rationale": "需求要求本机优先但可迁 Linux+Docker；唯一 embed 包和数据目录边界先固定，Docker 运行时验证明确为人工验收。"
      }
    ],
    "notDoing": [
      "不 fork 或复制 sub2api/new-api/juhe-ai/claude-code-hub 的任何代码；只读参考协议与设计。",
      "不做多租户、注册、支付、充值、返利、订阅、邀请、OAuth/SSO、Passkey、2FA、RBAC。",
      "不做 embeddings、图片生成/编辑、音频转写/TTS、视频、rerank、Realtime。",
      "不做 Gemini /v1beta 原生入口。",
      "不做 Responses↔Chat↔Anthropic 跨协议转换；渠道必须原生支持入站协议。",
      "不做 Codex Responses WebSocket；0.164.0 的 responses_websockets 已 removed，当前配置 supports_websockets=false。",
      "不做 /v1/responses/compact 与 /alpha/search；若固定 Codex 版本 E2E 出现，另行立项。",
      "不做 Redis、消息队列、微服务、多实例共享限流。",
      "不做自动模型发现/自动映射。",
      "不做完整费用账单、对账、实际扣费同步。",
      "不做自动数据库回滚/down migration；只做迁移前备份与显式 restore。",
      "不做上游 Key 自动轮换；MVP 要求主密钥稳定并离线备份。",
      "不做 Key 揭示二次密码（需求明确要求点击直接显示）。",
      "不做默认 body 落库；捕获必须管理员显式保存。",
      "不做移动端、多语言、插件系统、暗色主题。",
      "不在本计划 argv 验收中真实运行 docker build；只做 Go 层与 Dockerfile 静态契约，真实构建发布前人工验收。",
      "不把 workbuddy2api-panel 当现有参考；当前工作区没有该目录。",
      "第一版不实现 ZCode adapter 或入站协议；ZCode 经用户批准延后至 MVP+1，取得协议证据后再立项。"
    ],
    "openQuestions": [
      {
        "id": "Q-ZCODE",
        "question": "ZCode 实际使用哪个入站 endpoint、method、鉴权头、body 和 SSE 事件？用户已批准延后至 MVP+1；MVP+1 启动前需提供 ZCode 版本、真实抓包或官方协议文档，T28 在此之前保持 needs-info。已记录 resolution：decision=延后到 MVP+1；evidence=“zcode版本我不知道，批准延后 其他你帮我做决定。你继续吧。”",
        "blocking": false,
        "relatedTaskIds": [
          "T28"
        ]
      },
      {
        "id": "Q-CODEX-PIN",
        "question": "是否确认以当前本机 codex-cli 0.164.0（wire_api=responses、supports_websockets=false）作为第一版验收版本？未来升级会重新评估 WS/compact。",
        "blocking": false,
        "relatedTaskIds": [
          "T26",
          "T27"
        ]
      },
      {
        "id": "Q-CODEX-COMPACT",
        "question": "固定 Codex 版本若在长会话中实际请求 /v1/responses/compact，是否把 compact 透传提升为 MVP？当前证据未显示必需。",
        "blocking": false,
        "relatedTaskIds": [
          "T27"
        ]
      },
      {
        "id": "Q-UPSTREAM-NATIVE",
        "question": "目标中转站是否都提供原生 /v1/responses？T27 使用 harness seed 的 openai_responses 渠道；真实上游若只有 Chat，是否接受后续单独做 Responses→Chat 转换？",
        "blocking": false,
        "relatedTaskIds": [
          "T18",
          "T27"
        ]
      },
      {
        "id": "Q-CAPTURE",
        "question": "按需 body 捕获采用“默认不落库 + 5 分钟内存诊断环 + 管理员显式保存”是否满足使用习惯？若要求任意历史请求事后抓取，需要重新设计缓冲窗口。",
        "blocking": false,
        "relatedTaskIds": [
          "T24",
          "T25"
        ]
      },
      {
        "id": "Q-WORKBUDDY",
        "question": "workbuddy2api-panel 不在工作区；是否接受仅以 sub2api/new-api 作为布局参考？",
        "blocking": false,
        "relatedTaskIds": [
          "T10",
          "T12",
          "T13",
          "T15",
          "T16",
          "T22"
        ]
      },
      {
        "id": "Q-PROJECT-LICENSE",
        "question": "项目是保持个人私有，还是未来公开？公开前需要选择许可证并审计直接依赖；当前 LICENSES.md 只记录参考项目许可证。",
        "blocking": false,
        "relatedTaskIds": [
          "T01"
        ]
      },
      {
        "id": "Q-FULL-POLICY",
        "question": "渠道全部满载时采用“换渠道 → 等待 2s → 429”的策略是否确认？若希望更长排队，可调整 GATEWAY_CAPACITY_WAIT，但需重新验收客户端超时。",
        "blocking": false,
        "relatedTaskIds": [
          "T19"
        ]
      },
      {
        "id": "Q-DOCKER-VERIFY",
        "question": "真实 docker build/容器启动不在 strict argv 白名单内；是否接受 T29 用 Go 测试覆盖嵌入资源/GATEWAY_DATA_DIR/Dockerfile 契约，并在发布前人工执行 docker build？",
        "blocking": false,
        "relatedTaskIds": [
          "T29"
        ]
      }
    ],
    "assumptions": [
      "A-01：绿地项目，无历史数据迁移；参考项目数据不导入。",
      "A-02：单进程、单用户、SQLite 单写者可接受；未来多实例再评估。",
      "A-03：Codex 目标版本为当前环境的 0.164.0；wire_api=responses、supports_websockets=false。",
      "A-04：至少有一个上游原生 Responses 渠道供 Codex 使用；T27 preflight 检查 harness seed 的 openai_responses 渠道，真实上游由本条与 Q-UPSTREAM-NATIVE 覆盖，若只有 Chat 上游需先决定转换方案。",
      "A-05：body 捕获采用默认不落库 + 5 分钟内存诊断环 + 管理员显式保存，不预先武装抓取。",
      "A-06：渠道满载策略为优先换渠道、全满等待 2s、然后 429。",
      "A-07：workbuddy2api-panel 不在工作区，布局参考 sub2api/new-api。",
      "A-08：项目暂按个人私有使用，不决定公开许可证；公开前单独选许可证并做依赖审计。",
      "A-09：管理员密码首次从 GATEWAY_ADMIN_PASSWORD 创建，之后只在 UI 修改；不提供自动忘记密码重置。",
      "A-10：不自动发现模型；模型列表完全来自手工映射。",
      "A-11：管理端前端用 npm + Vite；本机 Node v24.18.0 / npm 11.16.0 可运行。",
      "A-12：不实现 Anthropic Messages，直到 ZCode 证据或明确需求出现；预留 adapter 边界。",
      "A-13：Codex manifest 的 context_window 默认 128000，可由 GATEWAY_CODEX_CONTEXT_WINDOW 覆盖；不按模型自动推断，避免过度承诺。",
      "A-14：真实 docker build 不在 strict argv 验收内，T29 以 Go 测试 + Dockerfile 契约测试 + 人工发布验收覆盖。"
    ]
  }
}
<!-- TASK_RUN_CONTRACT_END -->
