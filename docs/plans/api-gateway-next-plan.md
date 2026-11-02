# 实施方案：模型能力真值化与有状态 Responses 边界

> 状态：**决定已确认，可实施**（D1–D4 见第 8 节；仍不是可直接交给 task-run 的 v3 合同包）
> 生成方式：从已通过确定性校验的方案草稿恢复；生成时间 2026-11-01
> 证据性质：只读源码与公开资料分析；本轮未运行测试、构建、服务或真实客户端。

## 1. 结论

在不扩展平台的前提下，先让 Codex manifest 与路由对模型能力的承诺由逐路由声明和同一别名多候选的共同能力决定，再为 previous_response_id、conversation、background 等有状态 Responses 请求建立明确、可验收、默认不跨渠道的边界；attempt 统计、孤儿清理、配置历史与真实客户端 E2E 按证据延期或单独立项。

基线（只读）：`git rev-parse --abbrev-ref HEAD` → main；`git rev-parse HEAD` → a202725f6ea0dce3f845c5565db226ccf7e56f1e；`git status --porcelain` → `?? nul`（未跟踪，未触碰；与 analysis-index 的 clean 记录不同，如实记录）；`ls internal/store/migrations` → 0001_init.sql、0002_channel_health_revision.sql；`ls docs/plans` → 仅 api-gateway-plan.md；`ls docs/plans/api-gateway-next-plan.md` → 不存在；`git ls-files e2e | wc -l` → 0；`ls -A e2e | wc -l` → 0；`ls docs/clients` → missing。未联系远端，不假定 origin/main 状态。ZCode 延后记录已在 docs/plans/api-gateway-plan.md:920 核实，不把延后本身报为当前违约。

逐项对照原始需求：1）固定版本客户端契约与真实验收：manifest 契约测试可修正，但真实 Codex/ZCode E2E 本批不做（D4）；2）逐模型能力与 Codex manifest 夸大、同一别名多候选共同能力：本批核心；3）有状态 Responses 边界：本批核心；4）attempt 统计、usage 完整性、孤儿清理、配置历史：按 A7/A8/A9 证据延期；5）残余可靠性/安全：不扩平台，只处理与两项核心相关的迁移/错误/文档边界。

当前确认缺口（均回读源码）：① 能力声明不是证据：internal/gateway/codex_models.go:215-221 对所有模型统一声明 context_window=max_context_window=全局值、input_modalities=[text,image]；:175-178 reasoning 恒为 [none]；manifest 读取链只拿到 []string（internal/gateway/models.go:124-162、internal/store/routes.go:239-263），model_routes 无能力列（internal/store/migrations/0001_init.sql:52-62）。A1-F1/F2/F3。② 同一别名多候选没有共同能力：候选只按 enabled+协议+熔断/容量过滤（internal/routing/scheduler.go:227-262、internal/store/routes.go:209-226）；请求体只替换 model（internal/upstream/client.go:463-489），图片/上下文类 4xx 是 request scope、不切换（internal/gateway/attempt.go:146-153、internal/gateway/proxy.go:306-317、internal/gateway/stream.go:744-749）。A3-F1/F3。③ 有状态 Responses 无边界：路由表只有 POST（internal/gateway/router.go:153-170），background 被当普通 2xx 透传，GET/DELETE/cancel 全 404；previous_response_id/conversation 未解析，失败重试与 LRU 可换渠道（internal/gateway/proxy.go:306-317、internal/gateway/stream.go:744-749、internal/routing/scheduler.go:464-477）。A4-F1/F2、A5-F1/F2/F3、A10-F5。④ 契约测试自造：internal/gateway/codex_models_test.go:14-66 把 43 个顶层键和 9 个 model_messages 键全列为必填，包含 0.164 中不存在的 supports_parallel_tool_calls/instructions_variables；注释 internal/gateway/codex_models.go:9-12,76-79 把“可空字段必须显式出现”当客户端契约。A1-F4/F5/F7、A6-F7。⑤ 管理端/存储缺口：PATCH 全量合并在 internal/adminapi/routes.go:176-203，新增字段漏拷贝会被启停清空（A2-F2）；CreateWithModels 在 internal/store/channels.go:267-280 直接 insert，绕过 normalize（A2-F3 为 unconfirmed 风险，按设计防护）；前端 ModelRoute 类型在 web/src/features/mappings/api.ts:14-23，表格 5 列/colSpan=5 在 web/src/features/channels/ChannelModelsTab.tsx:436-505（A2-F8）。

方案（最小高价值）：S1 能力数据与声明面：v3 迁移新增 input_modalities/context_window/reasoning_levels，store 校验/聚合/ListByProtocol，admin API PATCH 合并，渠道模型 Tab/映射页提示。S2 manifest 与图片准入：按别名全部启用同协议候选取模态最窄、窗口最小、reasoning 交集；未声明按 D1；图片请求命中仅文本别名时 400；manifest 契约按 0.164 serde 必需字段分层并修正 legacy 键/常量命名。S3 有状态边界：background=true 明确 400 unsupported_parameter；previous_response_id/conversation/encrypted_content/item_reference 仅单候选别名放行并 AcquirePinned+maxAttempts=1，多候选 400 stateful_route_unavailable；有状态 404/未知 SSE 失败用 request scope，不误冷却；不建 response_owners，不代理 GET/DELETE/cancel/Conversations。S4 最终验证与交接（只保留 final checks，不单设阶段）。D1（未声明模态默认）、D2（有状态策略）、D3（能力真值来源/是否含 reasoning）、D4（真实客户端 E2E 延期）需用户确认；未确认前按推荐实现，不代批准。

延期（证据）：attempt 聚合/usage 覆盖率（A7-F1/F2/F3）、孤儿 attempts/admin session 清理（A7-F5/A8-F1/F2）、配置变更历史（A9-F3）、真实 Codex/ZCode E2E（A10-F1；e2e 已由 526237e 删除，需新入口与授权，单独立项）、response 归属表/GET/DELETE/cancel（A5-F1/F3）、自动能力探测（A6-F9）、能力类 4xx 自动重试（A3-F3 的上游码未取证）、CI/Docker/race/真实上游（A10-F3/F4）。不扩成平台重构。

关键证据缺口（供复核阻塞）：Codex 0.164 在 input_modalities=[text] 时是否静默丢弃图片/禁用 view_image，以及 reasoning 档位收窄在子代理路径的硬报错，均来自本机二进制字符串与官方 models.json，未取得源码/未运行客户端（A6-F1/F2/F3 为 unconfirmed）；Codex 0.164 是否实际发送 previous_response_id/conversation/background 未核实（A4/A5 gaps）；OpenAI 官方 stateful 页面本轮 403，Azure 文档不能等同 OpenAI 保证；v3 迁移未在 SQLite 3.51.3 上执行；未运行任何测试、构建、服务、真实上游、Docker 或 race。

未执行：本轮未运行 go test、go vet、npm、go build、docker、race、真实客户端或真实上游；未读取 gateway.env、数据目录、数据库或任何凭据；未重启 8080/8095-8097；未提交/推送；本轮仅按授权重新生成 draft JSON（计划数据），未修改业务源码、配置、历史计划或用户既有改动。方案按 planPath 交付，不覆盖 docs/plans/api-gateway-plan.md。所有 checks 均为后续验收命令，本轮未执行，不能视为已通过。

## 2. 优先做的三组工作

### S1 · 能力数据与声明面

把能力声明落到 model_routes（v3）、store 校验/聚合/读取、admin API 与前端编辑；旧行默认 unknown，PATCH 不丢字段，自动建路由不落空值。

#### S1-T1 · 能力字段、v3 迁移与 store 读写/聚合

- 依赖：无
- 文件范围：internal/store/migrations/0003_model_capabilities.sql、internal/store/capabilities.go、internal/store/routes.go、internal/store/routes_test.go、internal/store/channels.go、internal/store/channels_test.go、internal/store/migrate_test.go
- 依据：选型依据：能力必须落在 model_routes 而不是 channels（0001_init.sql:52-62 渠道 1:N 映射；A2-F1），因为候选查询本来就是 route+channel 成对读取（scheduler.go:227、routes.go:209-226），渠道级字段无法区分同渠道的纯文本与视觉模型。v3 只能是新文件：启动时逐条比对 name+checksum（migrate.go:232-263），改 0001/0002 会让已升级库直接拒绝启动（A2-F4）；每文件单事务 + 迁移前 VACUUM INTO 备份（migrate.go:144-157、266-296）正好给出回滚点（A2-F5），加列方案不需要动 requiredBackupTables（A2-F6、backup.go:19-28）。默认值放在 normalizeModelRouteInput 并由 CreateWithModels 显式调用，是因为该路径原本直接 insert 绕过校验（channels.go:267-280），而 insert 显式列值时 SQLite 不应用 DEFAULT，会把 Go 零值写成空串（A2 分片未确认风险，按设计防护）。聚合放在 store 纯函数（skeleton interfaces）而不是 SQL：规则可表驱动、不引入 join/索引，个人规模下按协议读全量可接受（skeleton assumptions 已标注未测）。ListModelsByProtocol 改为复用同一查询，是为了让 manifest 与模型列表共享唯一过滤/排序实现，避免两处列清单与协议条件漂移。

需要复核的原始需求遗漏与不确定（不代用户决定）：① 原始需求正文（input.json:3）没有点名 input_modalities/context_window/reasoning_levels 三个字段、10000000 窗口上限与 none..ultra 枚举；这些来自 skeleton decisions D3、data/interfaces 段与 analysis A1/A6 的 0.164 证据，D3 尚未获用户确认，本任务按推荐实现，若改选只影响 capabilities.go 常量与 0003 列集合。② 未声明模态的默认（D1：text+image vs text）不影响本任务的存储与聚合，但决定 S2 的 manifest/准入输出；本任务只保证 unknown 可被无损读出。③ reasoning_levels 用 JSON 文本列并按枚举序规范化，是实现选择（需求只写“NULL 或枚举数组”）；若复核要求保留管理员录入顺序，应改规范化策略与断言。④ ModelRouteInput.ContextWindow 用 0 表示未声明，意味着 store 无法表达“0 窗口”，这是有意的（窗口必须为正整数）；API 侧“0 清空”由 S1-T2 落地。⑤ NB-R1 中 min(GATEWAY_CODEX_CONTEXT_WINDOW, 声明值) 与 manifest 字段精确值不在本任务验收范围，本任务只提供 ContextWindow/HasUndeclaredWindow/ReasoningLevels 输入，S2-T1 负责收口——复核时不要把 S2 的验收记到 S1-T1 头上。⑥ 种子 findingIds 未包含 A2-F3（自动建路由默认值），但本任务步骤 5/7 正是该风险的防护；保留种子字段不变，在此明示以免覆盖检查时漏审。⑦ 本轮为只读细化：未运行 go test/go vet/npm/迁移/服务，未读取实际数据库或凭据，chk-s1t1-store 未执行，所有结论均来自上列文件的静态阅读。
- 步骤：
  - 0) 前置核对（只读）：确认 `internal/store/migrations/` 仅 0001_init.sql、0002_channel_health_revision.sql（已核对），工作树仅未跟踪 `nul`；不改 0001/0002。若复核把 D1/D3 改选（未声明模态默认、reasoning 是否首批），只调整 capabilities.go 常量与 0003 列集合，不动本任务边界。
  - 1) 新增 `internal/store/migrations/0003_model_capabilities.sql`，只含三条 ALTER TABLE model_routes ADD COLUMN，不建表不建索引：`input_modalities TEXT NOT NULL DEFAULT 'unknown' CHECK (input_modalities IN ('unknown','text','text_image'))`；`context_window INTEGER CHECK (context_window IS NULL OR (context_window BETWEEN 1 AND 10000000))`；`reasoning_levels TEXT`（JSON 数组文本，NULL=未声明，不加 CHECK；跨字段/枚举规则放 Go，理由见 A2 分片：ADD COLUMN 的 CHECK 会校验全部旧行，只能做单列枚举）。文件名须匹配 migrate.go:60 的 `^(\\d{4})_[a-z0-9][a-z0-9_-]*\\.sql$`，内容一次性由 applyMigration 单事务执行（migrate.go:266-296）。
  - 2) 新增 `internal/store/capabilities.go`（纯逻辑，不碰 DB、不读 env）：模态常量 ModelModalitiesUnknown/Text/TextImage（unknown/text/text_image）；reasoning 枚举 none,low,medium,high,xhigh,max,ultra（D3 取值，来自 0.164 models.json 证据）与窗口边界 1..10000000；normalizeCapabilities(input) 负责 \"\"→unknown、0→nil 窗口、空/nil 档位→nil、未知档位或重复档位→invalidModelRoute 错误、通过后按枚举序规范化档位；`type EffectiveRouteCapabilities struct { RouteCount int; InputModalities string; ContextWindow *int64; HasUndeclaredWindow bool; ReasoningLevels []string }` 与 AggregateRouteCapabilities(routes []ModelRoute) EffectiveRouteCapabilities：模态任一 text→text，否则任一 unknown→unknown，否则 text_image；窗口取已声明候选最小值，任一候选 NULL 置 HasUndeclaredWindow（全局 GATEWAY_CODEX_CONTEXT_WINDOW 的收口留给 S2）；档位取全部候选交集按枚举序，空/任一未声明→空切片；入参空集合返回 RouteCount=0、unknown、nil、空，且不 panic。
  - 3) 改 `internal/store/routes.go`：modelRouteColumns（:25-26）追加 input_modalities, context_window, reasoning_levels，同步 scanModelRoute（:434-466，用 sql.NullInt64/sql.NullString 与 json.Unmarshal 解析档位，坏 JSON 直接报错不静默）；ModelRoute（:69-79）增 InputModalities string、ContextWindow *int64、ReasoningLevels []string；ModelRouteInput（:82-88）增 InputModalities string、ContextWindow int64（0=未声明）、ReasoningLevels []string（nil/空=未声明）；insertModelRoute（:130-155）与 Update（:299-312）写三列（0→SQL NULL、空档位→NULL、模态写规范化值）；normalizeModelRouteInput（:356-383）调用 normalizeCapabilities。
  - 4) 在 `routes.go` 合并查询：把 ListByModelAndProtocol（:196-226）里硬编码的 `r.id, r.client_model, ...`（:210-211）与 ORDER BY 抽成私有 listRoutes(ctx, clientModel, protocol)，空 clientModel 表示全模型，排序为 `r.client_model ASC, r.priority ASC, c.priority ASC, julianday(c.last_used_at) ASC, r.id ASC`（单模型调用顺序与现状完全一致）；新增导出 ListByProtocol(ctx, protocol string) ([]ModelRoute, error)（空 protocol→空切片）；ListModelsByProtocol（:239-244）改为调用 listRoutes 后按已排序结果去重 client_model，保留原错误文案“列出客户端模型失败”，删除重复的 listModels SQL 分支（:246-284），避免两处列清单漂移。
  - 5) 改 `internal/store/channels.go`：CreateWithModels（:216-285）的循环（:267-280）在 insertModelRoute 前调用 normalizeModelRouteInput，使注释承诺的“复用 ModelRouteInput 校验规则”成立；默认值因此落到 unknown/NULL，任一失败仍回滚整个渠道事务。不依赖 SQL DEFAULT 兜底（insert 显式列值时 SQLite 不应用 DEFAULT）。
  - 6) `internal/store/routes_test.go` 增：TestModelRouteCapabilitiesRoundTrip（text/32000/[high,low] 写入后读回 text/32000/[low,high]，Get 与 ListByModelAndProtocol 一致）、TestModelRouteCapabilitiesClearSemantics（Update 传 unknown/0/[] 后读回 unknown/nil/nil，并断言这是 store 全量更新契约）、TestModelRouteCapabilitiesValidation（表驱动：非法模态、窗口 -1/0/10000001、未知档位、重复档位→ErrInvalidModelRoute 且 modelRouteRowCount 不变；直连 UPDATE 非法模态与越界窗口被 SQLite CHECK 拒绝；Go 常量全部值可写入）、TestAggregateRouteCapabilities（表驱动覆盖单候选 32000→32000、两候选 32000/200000→32000、text+text_image→text、未声明+200000→200000 且 HasUndeclaredWindow、全未声明→unknown/nil、全 text_image→text_image、档位交集空与任一未声明→空）、TestModelRoutesListByProtocol（启用映射+启用渠道+协议匹配、跨模型排序稳定、ListModelsByProtocol 与旧用例 TestModelRoutesListModels 期望一致）。
  - 7) `internal/store/channels_test.go` 增 TestCreateWithModelsCapabilityDefaults：CreateWithModels 带 2 个模型后逐条 Get 断言 unknown/nil/nil，并直接查 `SELECT input_modalities, context_window, reasoning_levels FROM model_routes` 断言原始列为 'unknown'/NULL/NULL（防空串落库，A2-F3 的失败形态）。
  - 8) `internal/store/migrate_test.go` 增：TestCapabilityMigrationV2ToV3（MigrateWith(ctx, migrations[:2]) 建 v2 → insertChannel + 旧列 INSERT model_routes → Migrate(ctx)，断言 FromVersion=2/ToVersion=3/Applied=[3]、BackupPath 前缀 pre-migrate-v2-、旧行读回 unknown/nil/nil、原始列同样为 unknown/NULL）、TestRestoreV2BackupThenMigrate（v2 库 BackupTo → 关闭 → 对现有 v3 库 Restore 成功且 BackupVersion=2、pre-restore 备份存在、restore 后 SchemaVersion 仍为 2，随后 Migrate 升 3 且旧行能力仍 unknown）、TestEmbeddedMigrationsFrozen（断言内嵌迁移版本序列 [1,2,3]，并用黄金 SHA-256 冻结 0001=c80b53716dbf50e65a14ac8290aa62e8791a79562fa61b5700b6eae2d3c65476、0002=358bb6a0e21431319c8af5dbed7f1d91f9ce34eb8c044fa82d9928dfe82bf6c0，作为 NB-R5“不改 0001/0002”的机器证据）。不改既有迁移/备份用例断言（它们用 result.ToVersion/latestVersion，随 0003 自动跟进）。
  - 9) 自查并记录：运行 `go test -count=1 ./internal/store`（即 chk-s1t1-store）与 `go vet ./internal/store`；失败回退到步骤 1-4 修正。交付说明写明未执行层级：未在真实数据目录/服务上执行 v3、未验证旧二进制拒绝启动、未运行前端、未接触真实上游或客户端（本轮约束禁止执行）。
- 行为验收：
  - 现有 v2 库升级后，管理员看到每条旧映射的能力都是“未声明”（模态 unknown、窗口空、档位空），schema 版本为 3，且迁移前备份以 pre-migrate-v2- 开头可直接用于回滚。机器：chk-s1t1-store 内 TestCapabilityMigrationV2ToV3、TestMigrateFreshAndIdempotent。人工/未覆盖：真实数据目录与服务启动不在本任务（输入约束禁止），只能由后续发布步骤验证。
  - 新建渠道并勾选模型后，自动生成的映射读回为 unknown/空窗口/空档位，数据库原始列是 'unknown'/NULL/NULL 而不是空串或 0（旧行为下 A2-F3 会把零值写进新列）。机器：chk-s1t1-store 内 TestCreateWithModelsCapabilityDefaults。
  - 管理员声明 text/32000/[high,low] 后再次读取得到 text/32000/[low,high]（档位按枚举序规范化），Get、List、候选查询三条读路径一致；把同一路由全量更新为 unknown/0/[] 后读回 unknown/空/空（清空语义）。机器：chk-s1t1-store 内 TestModelRouteCapabilitiesRoundTrip、TestModelRouteCapabilitiesClearSemantics。人工：管理端保存/刷新与“只启停不清空”属 S1-T2/S1-T3，不在本任务。
  - 非法能力值在写库前被拒绝（ErrInvalidModelRoute，行数不变）；即使绕过 Go 直接 UPDATE，SQLite CHECK 也拒绝非法模态与 0/负数/超 10000000 的窗口；Go 侧枚举的全部模态与档位值均能被数据库接受。机器：chk-s1t1-store 内 TestModelRouteCapabilitiesValidation。
  - 同一别名多候选的共同能力由纯函数给出可复核结果：窗口取已声明候选最小值并标记“存在未声明候选”，模态只要有一个候选为 text 就收窄为 text，档位交集为空或任一候选未声明就是空（manifest 层据此回退 [none]）。机器：chk-s1t1-store 内 TestAggregateRouteCapabilities。人工：min(GATEWAY_CODEX_CONTEXT_WINDOW, 声明值) 与 manifest 字段值属 S2-T1，本任务只提供输入。
  - manifest 的数据源按协议取值：只包含启用映射 + 启用渠道 + 协议匹配的行，跨模型按 client_model/priority/id 稳定排序；ListModelsByProtocol 输出与旧实现等价（去重、升序、空集合非 nil），既有 TestModelRoutesListModels 断言不需要修改即可通过。机器：chk-s1t1-store 内 TestModelRoutesListByProtocol 与既有 TestModelRoutesListModels。
  - 用 v2 备份恢复被接受（BackupVersion=2，pre-restore 备份生成），恢复本身不迁移、SchemaVersion 仍为 2；随后一次 Migrate 升到 3，旧行能力仍为未声明。机器：chk-s1t1-store 内 TestRestoreV2BackupThenMigrate。人工/未覆盖：真实 /readyz 与发布流程属 S4 与 docs/deploy.md（S3-T3），本任务不启动服务。
  - 0001/0002 两个历史迁移字节未变：内嵌迁移版本为 1/2/3，且 0001/0002 的 SHA-256 等于冻结黄金值；改动任一历史文件会直接使该测试失败。机器：chk-s1t1-store 内 TestEmbeddedMigrationsFrozen。人工：`git diff --exit-code HEAD -- internal/store/migrations/0001_init.sql internal/store/migrations/0002_channel_health_revision.sql` 可作为第二证据，不单列为检查。
  - 未执行层级如实记录且不被当作通过：本任务只覆盖 Go 单元/集成级（临时库）证据；未在真实 SQLite 数据目录执行 0003、未验证旧 v2 二进制打开 v3 库被拒绝、未运行前端、未涉及真实 Codex 客户端或上游。人工：交付说明列出这些层级与原因（NB-R6）。

#### S1-T2 · 管理 API 能力字段与 PATCH 合并

- 依赖：S1-T1
- 文件范围：internal/adminapi/routes.go、internal/adminapi/routes_test.go
- 依据：保留种子标识不变：id=S1-T2、stageId=S1、requirementIds=[NB-R3]、findingIds=[A2-F2]。改动面限定 adminapi 的请求体/视图/合并与测试：store 与 0003 迁移归 S1-T1，前端类型与 Tab 归 S1-T3，因此本任务不碰 store/web/docs，也不恢复 526237e 删除的 e2e。

为什么在 adminapi 合并而不是改 store：骨架已把 ModelRouteInput 固化为值语义（ContextWindow int64、ReasoningLevels []string），T1 的 Update 是 normalize 后单条 UPDATE（internal/store/routes.go:283-321），T2 只需在 handleUpdateModelRoute 里先用 existing 铺满输入再按请求覆盖（routes.go:182-203）。A2-F2 的确认同时指出读—改—写无事务/版本谓词、以及 updated_at 不是能力变更时间；前者要 store 层 patch API 或版本列，超出本任务文件范围，列为已知残余而非已修。

兼容性依据：decodeJSONBody 未启用 DisallowUnknownFields（internal/adminapi/middleware.go:412-424），新增请求字段对旧调用方无影响；现有前端的 PATCH 载荷只发既有字段（ChannelModelsTab.tsx:146、298-302，MappingsPage.tsx:351-357、399），所以「启停/行内编辑不丢能力」完全依赖本任务的合并，不是可选项。

需求与骨架未定、需要复核的点：(1) JSON null 语义——需求只写「省略保持、0/[]/unknown 清空」，本任务把 null 当省略（*int64/*[]string 解码为 nil），清空必须用 0/[]/unknown；若要 null=清空需自定义 UnmarshalJSON，属契约变更。(2) reasoning_levels 的顺序与去重——需求只说枚举数组，T2 只断言集合相等，不承诺返回顺序。(3) 空串/大小写/空白归一未定——不用这些值做断言，但 T1 必须处理 internal/store/channels.go:312 的内部调用（该处构造 ModelRouteInput 时不带能力字段），否则渠道创建路径会被新校验拒绝，这是 T1/T2 的接口缝，需在 T1 复核时确认。(4) 更换 channel_id 时能力是否保留——本任务按 PATCH 语义保留，但 (client_model, channel_id) 变了以后旧声明可能不再成立，需求未定，需产品决定；若要求重置，T2 需加分支并改测试。(5) 无 capability_updated_at（骨架未列），任何 PATCH 都刷新 updated_at（store/routes.go:310），故 updated_at 不能当能力审计，属 A9 主题。(6) 这是需求扩展而非复述：docs/requirements/api-gateway.md:61 的映射字段只有「模型名、渠道、优先级、启停」，新增三项能力实质扩展已批准需求，应由方案文档显式声明，T2 不改需求文档。

检查覆盖说明：chk-s1t2-admin 是骨架已有命名，命令未改。未新增 -run 定向检查，因为 go test -run <name> 在无匹配用例时仍退出 0，会制造假通过；新增用例的存在性由 diff 复核与包级断言保证。NB-R3 的 store/迁移条款由 chk-s1t1-store 覆盖，人工条款（浏览器保存/刷新、重启后仍在、渠道自动建映射为未声明）本批未执行——其中自动建映射的落库默认由 T1 的 store 测试覆盖，浏览器与重启部分交给 S1-T3 组件测试与 S4 人工清单，不能当作已通过。

未执行：本轮只读源码与计划文件（git rev-parse HEAD→a202725、git status→`?? nul`、ls internal/store/migrations 仅 0001/0002，说明 0003 尚不存在、T2 依赖 T1），未运行 go test / go vet / npm / build / 迁移 / 服务，未读取数据目录、gateway.env 或任何凭据；上述行为验收全部是待执行计划，不是结果。
- 步骤：
  - 核对 S1-T1 已落地契约后再动手：store.ModelRoute 的 InputModalities/ContextWindow *int64/ReasoningLevels []string，ModelRouteInput 的 ContextWindow int64（0=未声明）与 ReasoningLevels nil/空=未声明，normalizeModelRouteInput 对三项的枚举/范围/去重规则，以及 Update 先 normalize 再单条 UPDATE 写 NULL 的行为（internal/store/routes.go:283-321）。与上述不符即停下对齐 T1，不在 T2 里改 store。
  - 扩请求体：createModelRouteRequest 增加 InputModalities *string、ContextWindow *int64、ReasoningLevels *[]string；updateModelRouteRequest 用同名的三个指针，靠 nil 区分「省略」与「显式 0/[]/unknown」。
  - 扩视图：modelRouteView 增加 InputModalities string、ContextWindow *int64、ReasoningLevels []string；newModelRouteView 中窗口直接透传指针（未声明序列化为 null），档位在 nil 时归一为 []string{}，保证 reasoning_levels 永远是数组而不是 null。
  - handleCreateModelRoute：省略能力时显式落 unknown/0/nil 再构造 store.ModelRouteInput（不依赖 SQL DEFAULT，配合 A2-F3 的 insert 风险）；显式值原样透传，非法值交给 store 归一返回 400。
  - handleUpdateModelRoute：先按现有逻辑 Get（保持 404 优先），把 existing 的三项铺进 input（窗口 if existing.ContextWindow != nil 解引用；档位 append([]string(nil), existing.ReasoningLevels...) 深拷贝），再按 req 指针覆盖——非 nil 即写，因此 {context_window:0}、{reasoning_levels:[]}、{input_modalities:"unknown"} 即清空，未提供字段保持不变。
  - 在 internal/adminapi/routes_test.go 新增 TestModelRouteCapabilitiesAPI：覆盖创建默认值、创建显式声明、GET/列表三字段的 JSON 键存在性（map 解码断言 context_window 为 null、reasoning_levels 为 []）、{enabled} 启停不清空、行内编辑载荷不清空、空 PATCH 不变、单项更新、三种清空互不影响、POST/PATCH 非法值 400 且 store.Get 前后全字段一致、类型错误走 invalid_request、不存在 id 的 404 优先；同时扩展 TestModelRoutesAPIValidation 的 POST 表。复用 newModelRoutesAPI/apiRequest/decodeBody，新增 patch/get/assertCapabilities 辅助函数，持久化断言用 store.NewModelRoutes(st.DB()).Get，不只信 HTTP 响应。
  - 运行 go test -count=1 ./internal/adminapi（chk-s1t2-admin）并保存命令与输出；不启动服务、不跑前端、不跑 store 包（阶段检查 chk-s1-go 会覆盖）。
  - 自查 git diff --stat 只含 routes.go 与 routes_test.go、git status --porcelain 无新增未跟踪产物；确认未改 store/web/docs/e2e、未恢复 526237e 删除的文件、未提交未推送。
- 行为验收：
  - 创建映射省略能力：POST 返回 201，响应体 input_modalities="unknown"、context_window=null、reasoning_levels=[]；GET /admin/api/model-routes/{id} 与列表同值；store.Get 读回窗口 nil、档位为空。（机器：chk-s1t2-admin / TestModelRouteCapabilitiesAPI「创建默认」；断言响应字段值而非仅字段存在）
  - 创建映射显式声明：POST {"input_modalities":"text_image","context_window":32000,"reasoning_levels":["low","high"]} → 201，回读窗口 32000、模态 text_image、档位集合 {low,high}；档位按集合比较，不锁顺序。（机器：同上）
  - GET 返回三字段是契约而非零值巧合：对未声明行把响应解码成 map[string]json.RawMessage，断言 input_modalities/context_window/reasoning_levels 三个键都存在，context_window 为 JSON null，reasoning_levels 为 JSON []（不是缺失、不是 null）。（机器：同上）
  - 启停不清空：对已声明能力的映射 PATCH {"enabled":false} → 200，响应与随后的 GET 中三项能力及 channel_id/upstream_model/priority 全部不变；再 PATCH {"enabled":true} 仍不变。这是 ChannelModelsTab.tsx:146 的真实载荷，对应 A2-F2 的触发路径。（机器：同上）
  - 行内编辑不清空：PATCH {"client_model","upstream_model","priority"}（ChannelModelsTab.tsx:298-302 的真实载荷）→ 200，三项能力保持；MappingsPage.tsx:351-357 的全量载荷（不含能力字段）同样保持。（机器：同上）
  - 空 PATCH 不发生隐式重置：PATCH {} → 200，所有字段与 PATCH 前一致（不因零值写回而把能力清成 unknown/NULL）。（机器：同上）
  - 能力可单项更新：已声明三项的映射 PATCH {"context_window":64000} → 窗口变 64000，模态与档位不变；再 PATCH {"reasoning_levels":["low"]}，窗口与前次值保持。（机器：同上）
  - 清空语义：分别 PATCH {"context_window":0} → 200 且 context_window=null，{"reasoning_levels":[]} → 200 且 []，{"input_modalities":"unknown"} → 200 且 unknown；每次其余两项与 channel/upstream/priority 不变，store.Get 复核落库为 nil/空。（机器：同上）
  - 非法值不写库：POST 与 PATCH 的 input_modalities=audio / text,image、context_window=-1 / 10000001、reasoning_levels=["extreme"] / ["low","low"] 均返回 400 + error.code=invalid_model_route（writeModelRouteError 的校验分支），且 PATCH 前后 store.Get 的全字段完全一致，证明无部分写入。（机器：同上）
  - 类型错误分层不混淆：PATCH {"reasoning_levels":"low"} 或 {"context_window":"32000"} 返回 400 + error.code=invalid_request（decodeJSONBody/writeDecodeError 的既有契约），同样不写库；不得把它们报成 invalid_model_route。（机器：同上）
  - 既有契约不回退：PATCH 不存在 id 同时带非法能力仍返回 404 model_route_not_found（保持 routes.go:177 先 Get 的顺序）；未登录 401、跨源 403、重复映射 409、列表 no-store 与不泄漏 Key 的既有断言继续通过。（机器：chk-s1t2-admin 中既有 TestModelRoutesCRUDAPI / TestModelRoutesAPIValidation / TestModelRoutesRequireSession 原样通过）
  - 人工（本任务不执行）：浏览器中在渠道模型 Tab 保存能力、只启停一次后能力仍在、重启服务后仍在。方法：S1-T3 的组件测试覆盖保存/清空/启停展示，S4 的人工清单在真实服务上复验；本批无浏览器与运行服务授权，不得报为已通过。（人工：S1-T3 组件测试 + S4 人工清单，本轮未执行）

#### S1-T3 · 渠道模型 Tab 能力编辑与映射页候选提示

- 依赖：S1-T2
- 文件范围：web/src/features/mappings/api.ts、web/src/features/mappings/capability.ts、web/src/features/channels/ChannelModelsTab.tsx、web/src/features/mappings/MappingsPage.tsx、web/src/features/channels/ChannelsPage.test.tsx、web/src/features/mappings/MappingsPage.test.tsx
- 依据：依据（本轮只读，未运行任何命令）：A2-F8 的静态结论与源码一致——ModelRoute 定义在 web/src/features/mappings/api.ts:14-23（8 字段）、ModelRouteInput 在 :52-58（全必填）、ModelRoutePatch 在 :61；web/src/features/channels/types.ts:21-72 只有 Channel/ChannelInput/ChannelPatch 等渠道级类型，无路由或能力类型，所以“只改 channels/types.ts”是改错文件。ChannelModelsTab.tsx:440-444 恰 5 个 th、:490 为 colSpan={5}，两者当前一致但无任何测试校验其相关性；:69-71/:267-274 是编辑态与预填、:288-291 是校验、:298-302 是 PATCH 体、:200-206 与 :244-250 是两处 createRoute。MappingsPage.tsx:838-847 已经算出 availableRoutes 与 protocols，可直接复用为 Responses 候选集；:389-390 的 h3 列表断言、:394-395 与 :468-470 的渠道徽章精确文本、:251-257/:563-569 的 POST/PATCH 精确 body 都是既有护栏，因此方案要求摘要区不加 heading、不改徽章文本、不改五字段请求体。web/package.json:15-16 证明 test:channels/test:mappings 各只跑一个文件，所以新用例必须写进这两个既有文件，否则命名检查不会执行它们——这也是不新增测试文件与 npm script 的原因。服务端字段名来自骨架 architecture.interfaces 与 internal/adminapi/routes.go:60-69（modelRouteView 将被 S1-T2 扩展），清空语义（0/[]/unknown）来自 NB-R3。

需复核的遗漏与不确定：① 原始需求（input.json 第 3 段）只要求评估“逐模型能力与 Codex manifest 是否夸大、同一别名多候选的共同能力”，并未要求管理端能力编辑 UI；编辑入口来自骨架 NB-R3 与 NB-R2 的人工验收项。本任务选择“只在渠道模型 Tab 编辑、映射页只读提示”，若复核认为 UI 范围偏大，可只保留 T1/T2 与 S2 的 manifest/准入，把 T3 降为只读展示。② D1 未确认（未声明默认 text+image 还是 text），能力列与提示里的“未声明按默认处理”依赖该决策，已把文案收敛到 capability.ts 常量。③ NB-R2 要求“管理员能看出哪个候选拉低共同能力”，而架构 boundaries 明确 web 不复制 manifest 交集/回退策略；本任务只描述已声明值的差异并标记收窄候选，不做有效值预览；若复核要求预览有效窗口/模态，需要服务端在 model-routes 视图暴露聚合结果（新增接口面，超出本任务文件范围）。④ 混合“已声明/未声明”不标记收窄候选，因为那等于把 D1 的默认值写进前端；只给“部分候选未声明”提示。⑤ reasoning 档位枚举 none/low/medium/high/xhigh/max/ultra 按 D3 与 A1 对 0.164 models.json 的证据硬编码（当前 internal/gateway/codex_models.go:175-177 只发 [none]），store 校验（S1-T1）没有枚举下发接口，两边需同步，新增档位要改两处。⑥ 窗口上限 1..10000000 取自骨架 architecture.data 的 CHECK 约定，若 T1 采用不同上限，UI 校验需同步。⑦ 新增 web/src/features/mappings/capability.ts 不在 taskSeeds 的 files 清单内：两个页面需要同一套标签与比较，内联会重复且易漂移，纯函数也便于在既有测试文件中断言；不新增依赖、样式或脚本。

未执行/未覆盖：本轮按“现在只读”要求未运行 npm.cmd、tsc、vitest，也未启动服务，文中所有 checkIds 均为待执行，不存在“已通过”。任务级检查只覆盖 jsdom 组件行为，不覆盖真实 adminapi 契约（由 chk-s1t2-admin 与 S2-T3 生产装配用例证明）、真实浏览器布局与无障碍（人工），也不覆盖 manifest 聚合（S2-T1）。
- 步骤：
  - web/src/features/mappings/api.ts：新增 `export type InputModalities = 'unknown' | 'text' | 'text_image'`；ModelRoute（当前 :14-23，8 字段）追加可选 `input_modalities?: InputModalities`、`context_window?: number | null`、`reasoning_levels?: string[] | null`；ModelRouteInput（:52-58）追加同名可选字段，ModelRoutePatch 仍为 `Partial<ModelRouteInput>`（:61）。保持可选是骨架 S1-T3 的明确要求，也让既有 3 处 createRoute 调用（ChannelModelsTab.tsx:200-206、:244-250、MappingsPage.tsx:351-365）与 4 处 fixture 无需改动即可编译。不改 web/src/features/channels/types.ts：那里只有 Channel/ChannelInput/ChannelPatch 等渠道级类型（:21-72），本任务没有渠道级能力字段。
  - 新增 web/src/features/mappings/capability.ts（纯函数、无 JSX、无新依赖）：MODALITY_LABELS（'unknown'→未声明、'text'→仅文本、'text_image'→文本+图片）；REASONING_LEVELS 常量按 D3 顺序 none/low/medium/high/xhigh/max/ultra；normalizeCapability(route) 把 undefined/'unknown'/null/[] 统一为“未声明”；describeCapability(route) 生成固定三段 `模态 · 窗口 · 档位`，缺失段写“未声明”，窗口输出原始整数（无千分位）；responsesCandidates(routes, channelByID) 过滤 route.enabled && channel 存在 && channel.enabled && channel.protocol==='openai_responses'；capabilityConflicts(candidates) 只比较已声明值（模态去重>1、窗口去重>1、档位集合不相等），返回 {dimensions, narrowingIDs, hasUndeclared}。此文件不得计算有效窗口、不得套用全局回退或 [none] 默认——聚合只由 S2-T1 的 internal/gateway 决定（骨架 architecture.boundaries：web 不复制 manifest 交集/回退策略）。
  - web/src/features/channels/ChannelModelsTab.tsx：表头在“上游模型”后插入“能力”（当前 :440-444 为 5 个 th），行内按 describeCapability 输出文本（当前 :453-456 列顺序），检查结果行 colSpan 由 5 改 6（当前 :490），保证表头列数与结果行跨列数同步。编辑表单（:405-427）在优先级字段旁新增三个 .field：模态 select（id=edit-modalities，label“输入模态”，三选项）、窗口 input type=number min=0 max=10000000 step=1（id=edit-window，label“上下文窗口”）、档位 checkbox 组（fieldset/legend“推理档位”，7 个档位）；startEdit（:267-274）预填三项，校验放在 :288-291 同处：空或 0 视为未声明，非整数/负数/>10000000 报行内错误且不发请求；handleEditSave（:298-302）构造 `ModelRoutePatch`，只把与 editing 路由归一化值不同的能力字段写入——只改优先级时 PATCH 体仍是 {client_model, upstream_model, priority}，显式清空时写 input_modalities:'unknown'、context_window:0、reasoning_levels:[]。新增一条 form-note：“能力声明影响 Codex manifest 与图片准入；未声明项按默认假设（text+image、全局窗口）处理”，对应 NB-R2 的人工可读性要求。同步添加/手动添加（:200-206、:244-250）保持五字段、不携带能力。
  - web/src/features/mappings/MappingsPage.tsx：在 groups（:273-291）之外增加 `routesByModel`（来自未经筛选的 routes），便于按 client_model 取该别名的完整候选集；复用 :838-847 已有的 availableRoutes/protocols 判定思路，但候选判定改调 capability.ts 的 responsesCandidates。组头（summary，:871-905）在“可用协议”徽章之后插入两个 span：冲突时显示 `.badge` 文本“候选能力不一致”，以及 `.form-note` 说明文本（点名每个维度的收窄候选渠道名，结尾固定写“以服务端聚合结果为准”）；存在未声明候选时显示独立文案“部分候选未声明……”。明细行（:922-929 之后）对 narrowingIDs 命中的路由加 `.badge`“收窄共同能力（维度）”。约束：summary 内不得新增 heading（MappingsPage.test.tsx:389-390 精确断言 h3 列表），不得改动承载渠道徽章文本（:394-395、:468-470 精确文本断言），不新增 CSS 类（复用 .badge/.badge--muted/.form-note，styles.css 不在文件范围）。提示必须按未筛选候选计算，避免渠道筛选把收窄候选隐藏后提示静默消失。
  - web/src/features/channels/ChannelsPage.test.tsx：保持 :488 启停断言 toEqual({enabled:false}) 与 :489 编辑断言 toEqual({client_model, upstream_model, priority}) 不变（新增能力为“按需发送”，这两条正是“不误清空/不误带字段”的护栏）。fixture helper modelRoute（:167-179）保持不填能力，用于断言“未声明”默认与向后兼容。新增用例：能力列三段文本（未声明/已声明/部分声明）；只改优先级 PATCH 无能力键；改能力时 PATCH 精确含被改字段并在回显后更新列；清空语义（unknown/0/[]）；窗口非法值不发请求、0 与 32000 可保存；启停后能力列文本不变；手动添加 POST 体仍五字段；6 个 columnheader 且结果行 td colSpan="6"。
  - web/src/features/mappings/MappingsPage.test.tsx：既有精确断言（:251-257 POST 五字段、:563-569 PATCH 五字段、:389-390 heading 列表、:394-395/:468-470 渠道徽章文本）全部保持。新增路由 fixture 带能力字段，用例覆盖：两个启用 Responses 候选声明冲突→徽章+点名文案+仅收窄行有“收窄共同能力”；声明完全一致→无任何提示；另一候选为 openai_chat 或已停用→无提示（证明按协议/启停过滤）；部分未声明→只有“部分候选未声明”、无收窄徽章；按渠道筛选到非收窄渠道后提示仍在且含被筛掉的收窄候选渠道名；映射页编辑 PATCH 仍五字段（不携带能力键，证明不会清空）。
  - 自检与交付：新用例全部写入上述两个既有 .test.tsx（web/package.json:15-16 的 test:channels/test:mappings 各只跑这一个文件，放到新文件不会被命名检查执行）；不新增 npm script、不改 web/src/styles.css、不动 internal/、docs/、web/src/features/channels/types.ts。检查命令由控制器在实现完成后执行，本任务不预跑、不改命令。
- 行为验收：
  - 渠道模型 Tab 表格出现第 6 列“能力”：未声明路由单元格为“未声明 · 未声明 · 未声明”，三项齐全的路由为“文本+图片 · 128000 · low/high”。机器：chk-s1t3-channels（columnheader 文本 + 行内文本断言）。
  - 只改优先级并保存时，PATCH 体严格等于 {client_model, upstream_model, priority}，不含任何能力键——即“省略保持”不被 UI 破坏，既有断言 ChannelsPage.test.tsx:489 不改。机器：chk-s1t3-channels。
  - 显式修改能力时，PATCH 体只包含被改动的能力字段（例如仅 {input_modalities:'text'}），保存后用服务端回显刷新行内能力列。机器：chk-s1t3-channels。
  - 清空能力：模态选“未声明”、窗口清空、取消全部档位后保存，PATCH 体含 input_modalities:'unknown'、context_window:0、reasoning_levels:[]，回显后该行显示三段“未声明”。机器：chk-s1t3-channels。
  - 窗口校验：'abc'、'-1'、'10000001' 触发行内错误且不发送 PATCH；'0' 与 '32000' 可保存。机器：chk-s1t3-channels（断言 fetch 调用次数为 0 与错误文本）。
  - 启停开关仍只发 {enabled:false}，且切换后行内能力列文本不变（证明启停不清空能力）。机器：chk-s1t3-channels。
  - 手动添加与“从上游同步”添加的 POST 体仍为五字段（client_model/channel_id/upstream_model/priority/enabled），不含能力键。机器：chk-s1t3-channels。
  - 表格有 6 个 columnheader，展开的单次检查结果行 td 的 colSpan 属性为 '6'（加列漏改会被断言拦住，而不是静默错位）。机器：chk-s1t3-channels。
  - 映射页同一 client_model 有两个启用 Responses 候选且声明不一致时，组头出现“候选能力不一致”徽章和点名文案（含各维度收窄候选的渠道名，结尾“以服务端聚合结果为准”），收窄行出现“收窄共同能力（维度）”，另一行不出现。机器：chk-s1t3-mappings。
  - 声明完全一致、只有一个启用 Responses 候选、或另一候选为 openai_chat 时，均不出现任何提示与徽章；不带能力字段的旧 fixture 正常渲染（无异常、无提示）。机器：chk-s1t3-mappings。
  - 部分候选未声明时只出现“部分候选未声明”提示，不出现“候选能力不一致”徽章，也不给收窄标记（未声明默认值取决于 D1，不在前端固化）。机器：chk-s1t3-mappings。
  - 提示按未筛选候选计算：把渠道筛选切到非收窄渠道后，提示仍在且文案包含被筛掉的收窄候选渠道名。机器：chk-s1t3-mappings。
  - 映射页编辑保存的 PATCH 体仍为五字段、不携带能力键（映射页无第二条能力写路径，不会清空能力）。机器：chk-s1t3-mappings（既有 toEqual 断言保持）。
  - 类型层：能力字段在 ModelRoute/ModelRouteInput 上均可选，既有 3 处 fixture 与 3 处 createRoute 调用无须改动即可编译。机器：chk-s1t3-typecheck（任务级新增）+ chk-s1-web-typecheck（阶段）。
  - 人工（本批不执行，需浏览器）：打开渠道页→管理模型→编辑模型→声明模态/窗口/档位→保存→刷新后仍在；映射页展开后徽章与文案位置合理、键盘可达、窄屏不溢出。jsdom 只证明文本与属性，不证明布局与视觉。
  - 人工复核交付记录：确认没有以 vitest 通过替代真实 adminapi 契约与 manifest 行为——这两层分别由 chk-s1t2-admin（S1-T2）与 S2-T3 的生产装配用例证明，本任务未执行。

### S2 · Manifest 与图片准入

让 Codex manifest 与图片准入使用别名共同能力；修正 43 键契约、legacy 键与常量命名；用生产装配测试锁定值。

#### S2-T1 · Manifest 使用别名共同能力并修正契约

- 依赖：S1-T1、S1-T3
- 文件范围：internal/gateway/capability.go、internal/gateway/models.go、internal/gateway/codex_models.go、internal/gateway/codex_models_test.go
- 依据：S2 的第一个任务：把 S1 落库的逐路由能力真正变成 Codex manifest 的逐模型声明，并把自造的“43 键必填”契约改成按 0.164 serde 必需字段分层。证据：codex_models.go:215-216 对每个模型写死同一全局窗口，:221 写死 [text,image]，:175-178 写死 [none]（A1-F1/F2/F3）；manifest 读取链只拿 []string（models.go:124-162、routes.go:239-263），builder 类型在 models.go:48，没有逐模型能力入口（A2-F7）；契约测试把 43 个顶层键与 9 个 model_messages 键全列必填（codex_models_test.go:14-72，已核对条目数为 43/9），其中 supports_parallel_tool_calls、instructions_variables 在 0.164 中不存在（A1-F4/F5、A6-F7）；codexToolOutputMaxTokens 实际是字节上限（codex_models.go:26,209-212，A1-F9）；仓库没有能力数据源，成本在存储与管理端（A6-F9）。生产装配确实传 *store.ModelRoutes（dataplane.go:119-124），S1-T1 加上 ListByProtocol 后 gateway 侧用可选接口断言即可命中，无需改 router.go/dataplane.go，因此本任务能严格限制在 4 个文件内。

实现取舍：聚合规则留在 store（S1-T1 的 AggregateRouteCapabilities），gateway 只做“回退应用（D1 + 全局窗口上限）”，避免两处各写一套交集；据此 manifestModel 只携带 slug 与聚合结果，newCodexModelInfo 是唯一的能力回退点。窗口公式取 min(全局回退, 已声明最小值)：既满足“单候选 32000→32000”“两候选 32000/200000→32000”“全未声明→全局”，也满足“一候选未声明 + 一候选 200000 → min(全局,200000)”。

待复核、不自行假定的点：
1) NB-R1 把“0.164 serde 必需字段”列为 10 项，但 A1-E11 实际列出 15 个无 serde default 的字段（另有 description、availability_nux、upgrade、default_verbosity、apply_patch_tool_type）。本任务按 NB-R1 只把 10 项当必需，删掉这 5 个字段不会被测试发现；若意图是“无 default 即必需”，必需集应改为 15 项，请复核。
2) NB-R1 第 4 条人工验收要求 docs/clients/codex-0.164.md 写明“仅当 provider 配置远端模型目录 URL 才请求该 manifest”，但该文件不在本任务 files，S3-T3 的 requirementIds 只有 NB-R4/NB-R6。现计划里这条无人承接，建议把 NB-R1 补进 S3-T3（或 S2-T3）的 requirementIds，否则 NB-R1 的文档部分会漏验。
3) NB-R1 第 2 条（cmd/gateway 生产装配断言精确字段值）由 S2-T3 承接，本任务不产出、不得记完成。
4) 窗口回退语义有分歧：需求公式是 min(全局,200000)，即已声明值仍参与取最小；A1-E19 引用的参考实现在任一候选未知时把窗口归零（reference/sub2api/backend/internal/service/openai_codex_model_metadata.go:365-397 已只读核对）。本任务按需求公式实现并用配置 300000 的用例钉住；若 S1-T1 按参考实现归零，该用例会失败，须回 S1-T1 补“已声明最小值”，不能静默改成只看全局。
5) max_context_window 继续等于生效窗口（NB-R1 明确要求）。A1-F1 的原建议是把 max 当作可上调的覆盖上限；本批实际效果是“声明值只能把窗口压到全局值以下、不能上调”，这一点保留为已知限制，未擅自改语义。
6) reasoning 档位的 description 由网关自拟（客户端只展示文案），不是上游证据；测试只断言非空。0.164 收窄档位在 subagent spawn 路径的行为是 A6-F3 未确认项（仅二进制字符串），本任务不声称已验证。
7) 未声明回退按 D1 推荐（text+image、全局窗口、[none]）实现；D1 尚未获用户确认。
8) 本轮只读：已核对分支 main、HEAD a202725f6ea0dce3f845c5565db226ccf7e56f1e、工作树仅未跟踪 nul；未运行 go test、go vet、gofmt、构建或服务，未读取数据目录/gateway.env/凭据，未修改任何文件。上述所有 check 均为后续验收命令，现在不能算通过。
- 步骤：
  - 依赖核对（只读，先做）：读 S1-T1 落地的 internal/store/capabilities.go、routes.go、0003_model_capabilities.sql，确认 (a) store.ModelRoute 暴露 InputModalities/ContextWindow/ReasoningLevels；(b) AggregateRouteCapabilities 返回「模态最窄、已声明窗口最小值（0=全部未声明）、reasoning 规范顺序交集 + 是否全部声明/交集为空标志」；(c) ListByProtocol(ctx, protocol) 只返回启用映射+启用渠道+协议匹配，按 client_model/priority/id 排序；(d) *store.ModelRoutes 同时满足 gateway 侧可选接口。若 (b) 的窗口字段在任一候选未声明时直接归零（丢失已声明最小值），先补 S1-T1 的聚合接口，不在 gateway 复制聚合规则。
  - 新建 internal/gateway/capability.go：定义 manifestModel{Slug string; Capabilities store.EffectiveRouteCapabilities}、protocolRouteLister 接口（ListByProtocol(ctx, protocol) ([]store.ModelRoute, error)）、与 store 枚举一致的模态常量（unknown/text/text_image）以及 buildManifestModels(routes []store.ModelRoute) []manifestModel：按输入顺序遍历，TrimSpace 后跳过空 slug、按首次出现去重，对每个 slug 的候选调用 store.AggregateRouteCapabilities 填入 Capabilities；不做任何回退或取最窄决策。
  - 改造 internal/gateway/codex_models.go，把能力策略收到一处：buildCodexManifest(models []manifestModel, contextWindow int)；newCodexModelInfo(m manifestModel, contextWindow int) 内计算 window=min(全局回退, 已声明最小值)（已声明最小值为 0 时用全局回退）、模态 unknown→[text,image]、text→[text]、text_image→[text,image]、levels 为空→[none] 否则用聚合交集（规范档位顺序）、default_reasoning_level=levels[0]，并为 none/low/medium/high/xhigh/max/ultra 提供非空 description；同步 newCodexManifestBuilder 闭包签名；把 codexToolOutputMaxTokens 重命名为 codexToolOutputMaxBytes（值 10000、mode 仍为 bytes）；把文件头注释改为「客户端必需字段 / 有 serde default 的可选字段 / legacy 键」三层并删掉“可空字段必须显式出现、否则 serde 失败”的表述；为 supports_parallel_tool_calls 与 instructions_variables 加 legacy 注释（保留输出、不作契约）。
  - 改造 internal/gateway/models.go：codexManifestBuilder 改为 func([]manifestModel) any；writeCodexManifest 先取协议路由再折叠——优先 h.routes.(protocolRouteLister).ListByProtocol，其次退回 ListModelsByProtocol/ListModels 生成只含 ClientModel 的 []store.ModelRoute（能力全未声明），然后 builder(buildManifestModels(routes))；仓储错误仍 writeInternalError，不回显；buildCodexManifestPlaceholder 适配新签名。
  - 重构 internal/gateway/codex_models_test.go：用 codexModelInfoRequiredFields（slug、display_name、supported_reasoning_levels、shell_type、visibility、supported_in_api、priority、support_verbosity、truncation_policy、experimental_supported_tools）与 codexModelMessageRequiredFields（instructions_template）替换 43/9 键清单，另设 legacy/稳定输出清单；把 assertCompleteCodexModel 改成「必需集 + 值域 + 期望能力值」三个助手；新增实现 ListByProtocol 的 protocolRoutesCatalog 测试替身；新增表驱动 TestCodexManifestSharedCapabilities 覆盖单候选、多候选最小窗口、未声明与配置窗口、模态收窄、全 text_image、reasoning 交集与空交集、大窗口封顶、全部未声明，并新增 TestBuildManifestModelsGrouping、TestCodexManifestContractLayering、TestCodexManifestLegacyKeysStableOutput；保留并适配既有的 TestCodexManifestFullModelInfo、BackendAliasMatchesClientVersion、FiltersModelsByResponsesProtocol、PlainListShape、ContextWindowConfig、DeduplicatesAndAllowsEmpty、OnlyEnabledRoutes。
  - 自检与取证：对 4 个文件跑 gofmt -l（应为空）；运行 chk-s2t1-manifest（go test -count=1 ./internal/gateway）与 chk-s2t1-manifest-cases（go test -count=1 -v -run TestCodexManifestSharedCapabilities ./internal/gateway），记录命令与原始输出；用 git diff --stat 与 git diff -- go.mod go.sum 确认只改 4 个列出的文件、无新增依赖、未触碰 0001/0002 与 store 代码。
  - 交接记录：列出 manifest 逐模型字段与回退规则、S1-T1 聚合接口的实际形状、未覆盖层级（S2-T3 生产装配、S3-T3 文档、真实 Codex/ZCode 客户端），并把“NB-R1 的 10 个必需字段 vs A1-E11 的 15 个无 serde default 字段”与“docs/clients/codex-0.164.md 无人承接”标为待复核项。
- 行为验收：
  - 【机器 chk-s2t1-manifest、chk-s2t1-manifest-cases】单候选声明生效：同一 client_model 只有一条 openai_responses 候选并声明 context_window=32000、reasoning_levels=[low,high] 时，GET /backend-api/codex/models 与 GET /v1/models?client_version=0.164.0 中该 slug 的 context_window 与 max_context_window 都为 32000，supported_reasoning_levels 恰为 ["low","high"]（按规范档位顺序），default_reasoning_level="low"，即使 GATEWAY_CODEX_CONTEXT_WINDOW=128000 也不被抬回全局值。
  - 【机器 chk-s2t1-manifest、chk-s2t1-manifest-cases】多候选取共同能力：同一别名两条 Responses 候选声明 32000/200000 → 32000；一条声明 200000、另一条未声明 → min(全局回退,200000)；GATEWAY_CODEX_CONTEXT_WINDOW=300000 且候选为 200000+未声明时仍为 200000（证明未声明候选不会抹掉已声明最小值）；全部未声明 → 全局值 128000。
  - 【机器 chk-s2t1-manifest、chk-s2t1-manifest-cases】模态收窄：两候选 text-only/text_image → input_modalities 恰为 ["text"] 且顺序固定；全部 text_image → ["text","image"]；全部未声明 → ["text","image"]（D1 现状）。
  - 【机器 chk-s2t1-manifest、chk-s2t1-manifest-cases】reasoning 交集：候选 [low,high] 与 [low,medium] → ["low"] 且 default="low"；[low] 与 [high]（交集为空）→ ["none"] 且 default="none"；任一候选未声明 → ["none"]；单候选 [high] → ["high"] 且 default="high"；levels 非空且 default 始终属于 levels。
  - 【机器 chk-s2t1-manifest】未声明别名与旧目录回退保持不变：没有 ListByProtocol 实现的旧 RouteCatalog、或数据库里能力全为 unknown/NULL 的路由，manifest 仍输出全局窗口、[text,image]、[none]；仍只列 openai_responses 模型、按 client_model 升序、TrimSpace 后去重、空集合输出 "models":[]；普通 /v1/models 列表与 404 语义不变。
  - 【机器 chk-s2t1-manifest】契约分层落地：测试的必需集合恰为 10 个顶层字段加 model_messages.instructions_template；supports_parallel_tool_calls 与 instructions_variables 只出现在 legacy/稳定输出断言里，不在必需集合；代码中不再存在把 43 个顶层键或 9 个 model_messages 键整体当必填的循环。
  - 【机器 chk-s2t1-manifest】值域与截断命名：truncation_policy.mode="bytes" 且 limit=codexToolOutputMaxBytes=10000；slug 唯一非空、context_window>0、input_modalities 只含 text/image；删除一个必需字段会让契约测试失败，而不是只靠“键齐全”。
  - 【人工】测试可见性：`go test -count=1 -v -run TestCodexManifestSharedCapabilities ./internal/gateway` 的输出必须逐条列出上述子用例名；若出现 “no tests to run”，说明用例未写或改名（-run 无匹配时退出码仍为 0，因此这一点只能人工确认，不能当作自动门禁）。
  - 【人工】文件与依赖范围：`git status --porcelain` 与 `git diff --stat` 只含 capability.go、models.go、codex_models.go、codex_models_test.go；`git diff -- go.mod go.sum` 为空；`git diff -- internal/store/migrations` 为空。生产装配（cmd/gateway 临时 SQLite + 假上游断言精确字段）不属本任务，由 S2-T3 的 chk-s2t3-assembly 承接，本任务不得记为本项已验收。

#### S2-T2 · 图片能力准入

- 依赖：S2-T1、S1-T1
- 文件范围：internal/gateway/request_shape.go、internal/gateway/router.go、internal/gateway/router_test.go、internal/gateway/errors.go
- 依据：范围与依据：seed 的 goal 是解析 Responses input_image 与 Chat image_url（仅已知路径、避免 tools 误判），按别名共同模态在 router 拒绝仅文本别名（400 model_does_not_support_image、上游 0 次），并设置 RequestInfo.HasImage；这正是 NB-R2 的前两条机器判据。基线在本会话只读核对：git rev-parse --abbrev-ref HEAD 得到 main，git rev-parse HEAD 得到 a202725f6ea0dce3f845c5565db226ccf7e56f1e，git status --porcelain 为 ?? nul（未跟踪，未触碰）。为什么放在 router 而不是 proxy：serveData 在调用 Forwarder 前已拿到鉴权后的 body 与已按入站协议过滤的 []store.ModelRoute 候选（internal/gateway/router.go:192-228、internal/store/routes.go:196-226），在此拒绝可直接证明 Forwarder 0 次，不必改 proxy/stream 的尝试循环；若放到 proxy，会先取调度槽位，且 400 在现有分类是 Retry=false 加 request scope（attempt.go:146-153、classification.go:93-99、proxy.go:306-317），既难给出干净的 0 次证明，也容易和延期的 A3-F3 纠缠。finding 状态：A3-F1、A3-F3、A1-F2 在独立复核分片（A3-confirmed、A1-confirmed）中均为 static-confirmed，支撑事实是候选只按 enabled 加协议过滤、无能力维度；manifest 对每个 slug 无条件声明 [text,image]（codex_models.go:221，源于计划期决定 api-gateway-plan.md:818）；能力类 4xx 是 request scope 且不重试。关键限定，供复核：(1) A3-F3 只被部分收敛。本任务挡的是「已声明 text」的别名在发出请求前 400；A3-F3 原始 fixSuggestion 的「候选级失败加允许切换」没有实现，notDoing 明确写「能力类 4xx 自动重试：A3-F3，本批用声明加准入降低风险，不自动重试」。所以声明为 text_image 而上游实际拒绝图片时，仍是 400 upstream_bad_request、request scope、不换渠道。findingIds 保留 A3-F3 是溯源，不代表关闭该 finding。(2) HasImage 本批无生产消费者：proxy.go:181 与 stream.go:610 只读 Protocol/Endpoint/Model，新增字段只有测试（recordingForwarder，router_test.go:60-79）与后续 S3 会读；保留它是 seed 决定与预留 seam，但属死字段风险，删除成本极低。(3) 形状契约无当前仓库证据：仓库里只有历史 E2E（526237e^ 的 e2e/codex_test.go 断言 body 含 input_image 与 data:image/png;base64,，该文件已由 526237e 删除、未运行，不构成当前证据）和计划中的「tools/input_image 原样透传」（api-gateway-plan.md:71,99）。「input 顶层 input_image」按 NB-R2 原文实现，若真实客户端只发 message.content part，该分支不命中（无害）；若图片落在白名单外路径则漏判并继续转发，这是刻意的 fail-open，不会造成新的误拒。(4) 拒绝日志归因仍是已知缺口：ObservedGateway 对未进入代理的 400 统一写 error_code=invalid_request（observability.go:312、335-355），且 model 为空（observability.go:306-319 未填 Model），所以 request_logs 里看不到 model_does_not_support_image；A3-F5 在 notDoing 中，本任务不修也不把它作为验收门槛。(5) 与 S3-T1 的插入顺序未由骨架规定：本计划把图片准入放在 404 与 503 之后、构造 RequestInfo 之前，保证未知或禁用别名 404 语义不变；background 与图片同时出现时谁先 400 需在 S3-T1 落地时确认，本计划建议图片优先（能力不匹配先于参数不支持），属跨任务约定、需复核。其他取舍：模态判定复用 S2-T1 在 capability.go 引入的折叠（manifest 用的是同一个值），拒绝条件为「探测到图片且折叠出的 input_modalities 不含 image」；若该折叠目前只内嵌在 buildManifestModels 内，则同文件提取内部函数共用，禁止在 router 复制收窄规则，这样 manifest 声明与请求准入不可能漂移。D1 的未声明默认 text_image 由折叠实现，本任务不再另设默认，因此 v3 迁移后的 unknown 旧行在管理员声明前零变化。测试放 router_test.go 而非新建 request_shape_test.go，是因为 NB-R6 要求文件范围符合 taskSeeds（除 npm build 产物外不得有未列文件），seed 的 files 只有四个。未执行：本会话未运行 go test、go vet、npm 或构建，未起服务、未访问真实上游或数据目录；chk-s2t2-admission 为骨架已有项，chk-s2t2-image-shape 为本次新增的定向检查，均留给控制器后续执行。
- 步骤：
  - 先读 S2-T1 落地结果：internal/gateway/capability.go 的折叠函数与 manifestModel、internal/gateway/models.go 的 builder 接线、internal/store/capabilities.go 的 EffectiveRouteCapabilities。确认「别名共同模态」只有一个实现入口；若折叠目前只内嵌在 buildManifestModels 里，则在 capability.go 提取一个接收 []store.ModelRoute 的内部函数，让 manifest 与准入两处调用。禁止在 router.go 里另写一份 text/text_image 收窄判断。自检：grep -n text_image internal/gateway 只应命中折叠实现与测试期望，不出现第二份收窄逻辑。
  - 新建 internal/gateway/request_shape.go，按协议做白名单式窄形状探测，只返回「是否含图片」，不返回错误。Responses：读顶层 input；input 为数组时，元素 type==input_image 直接命中；元素 content 为数组时 part type==input_image 命中；content 为字符串、input 为字符串或对象一律不命中。Chat：读 messages 数组；message.content 为数组时 part type==image_url 命中；content 为字符串不命中。不递归扫描 tools、tool_choice、functions、tool_calls[].function.arguments、function_call_output.output 等任意嵌套值，不使用「整段 body 包含 input_image/image_url 字样」的启发式。JSON 形状不符或未知协议一律返回 false（fail-open），不新增 4xx 类别；不做 base64 解码；不改 32MiB body 上限。文件注释写明：S3-T1 会在此扩展状态字段，本任务不预置 background/previous_response_id 等。
  - internal/gateway/errors.go：新增 errorCodeModelDoesNotSupportImage = model_does_not_support_image 与 writeModelDoesNotSupportImage(w)（400、type=invalid_request_error、固定 message、走 writeAPIError 以获得 no-store）。不改动现有 error code 的语义、状态码或 envelope 形状。
  - internal/gateway/router.go：RequestInfo 增加 HasImage bool（注释说明本批只作准入结果与后续 seam）。serveData 在 len(candidates)==0 的 404 与 h.forwarder==nil 的 503 之后、构造 RequestInfo 之前插入准入：取 S2-T1 折叠出的该别名在本入站协议的 input_modalities；若探测到图片且该集合不含 image 则 writeModelDoesNotSupportImage 并 return（不构造 RequestInfo、不调用 Forwarder）。其余情况构造 RequestInfo{Protocol, Endpoint, Model, HasImage: hasImage}。不改 readRequestBody/decodeModel 的 body 缓存与转发语义（router.go:238-259），保证 /v1/responses 与 /backend-api/codex/responses 共用同一分支。
  - internal/gateway/router_test.go 增加 TestRequestImageDetection（纯解析表，不经过 Handler）：Responses 顶层 input_image；message.content part input_image；content 为字符串；input 字符串里出现 input_image 字样；input_text 文本里出现 image_url 字样；function_call_output.output 内嵌 input_image 形状（不得命中）；input 非数组或形状非法。Chat：image_url part；content 字符串；tools[].function.parameters 出现 image_url；tool_calls[].function.arguments 出现 image_url；messages 非数组。断言 true/false 精确匹配，并覆盖两种协议互不串用（Responses 不认 image_url part，Chat 不认 input_image）。
  - internal/gateway/router_test.go 增加 TestImageAdmission（Handler 表，复用 fakeRouteCatalog 与 recordingForwarder，router_test.go:60-117，按 protocol+model 布置候选）：①单候选 text 加 Responses 图片，断言 400、decodeError 的 type/code、Forwarder.calls==0；②两候选 text 与 text_image，共同模态 text，断言同 400 与 0 次；③单候选 text_image 加图片，断言 200、calls==1、info.HasImage==true、forwarder.body 与请求字节逐字节相等；④单候选 unknown 加图片，断言 200（D1 默认 text_image）、HasImage==true；⑤单候选 text 加纯文本，断言 200、HasImage==false；⑥text-only 别名加仅 tools 中出现 image_url/input_image 字样的纯文本，断言 200；⑦同别名分协议：openai_responses 为 text、openai_chat 为 text_image 时，Responses 图片 400 而 Chat 图片 200；Chat text-only 加 image_url 断言 400。本层不写 request_logs/request_attempts，不在本任务断言日志行。
  - 交付时本地执行 go test -count=1 -run 'TestRequestImageDetection|TestImageAdmission' ./internal/gateway 与 go test -count=1 ./internal/gateway，记录命令与输出；不跑 e2e、不跑 docker、不访问真实上游。
  - 自检文件范围：仅新增 request_shape.go 并改动 router.go、router_test.go、errors.go 四个 seed 文件；不为解析测试新建 request_shape_test.go（NB-R6 要求文件范围符合 taskSeeds）；不动 store/adminapi/web/docs，不恢复 526237e 删除的 e2e，不触碰未跟踪的 nul。
- 行为验收：
  - 机器（chk-s2t2-admission、chk-s2t2-image-shape）：/v1/responses 与 /backend-api/codex/responses 的 body 在 input 数组顶层或 message.content part 含 type=input_image，且该别名在 openai_responses 下的启用候选共同模态为 text，响应 400，error.type=invalid_request_error、error.code=model_does_not_support_image、Cache-Control=no-store、顶层仅一个 error 对象；Forwarder 调用 0 次。
  - 机器（chk-s2t2-admission、chk-s2t2-image-shape）：/v1/chat/completions 的 messages[].content[] 含 type=image_url，且该别名在 openai_chat 下共同模态为 text，返回同一 400 与 code，Forwarder 0 次。
  - 机器（chk-s2t2-admission、chk-s2t2-image-shape）：共同模态为 text_image（全部候选显式声明）或全部未声明（D1 视为 text_image）时，图片请求放行，Forwarder 1 次，RequestInfo.HasImage=true，且 Forwarder 收到的 body 与客户端请求字节完全一致（探测不改写、不重编码 body）。
  - 机器（chk-s2t2-admission、chk-s2t2-image-shape）：纯文本请求（body 中无图片形状）放行，HasImage=false，状态码与 Forwarder 调用次数和改动前一致（text-only 别名也照常转发文本）。
  - 机器（chk-s2t2-admission、chk-s2t2-image-shape）：tools schema、tool_choice、tool_calls[].function.arguments、function_call_output.output 中出现 input_image 或 image_url 字样时不触发拒绝；text-only 别名的这类纯文本请求仍放行（防误判）。
  - 机器（chk-s2t2-admission、chk-s2t2-image-shape）：同一 client_model 两候选分别为 text 与 text_image 时，图片 400、纯文本 200，说明收窄只作用于图片判定；未声明（unknown）候选不受影响，即 v3 迁移后的现有别名在管理员声明前图片行为零变化。
  - 机器（chk-s2t2-admission、chk-s2t2-image-shape）：协议隔离正确——同一 client_model 在 openai_responses 声明 text、在 openai_chat 声明 text_image 时，Responses 图片 400 而 Chat 图片 200；共同模态按本入站协议下的启用候选计算。
  - 机器（chk-s2t2-admission、chk-s2t2-image-shape）：未知或禁用别名仍返回 404 model_not_available（不是新的图片错误），证明准入插入点在候选检查之后；图片拒绝不发生在上游调用与调度之后（本层用 Forwarder 计数证明，真实上游 0 次的装配级证据归 S2-T3）。
  - 人工：交付后在临时实例用 curl 对已声明 text 的别名发含 input_image 的 /v1/responses，确认 400 加 code=model_does_not_support_image 且渠道侧无上游请求；把该别名改为 text_image 后同一请求转发成功。真实 Codex 客户端用 -i 附图不在本批（D4/NB-R6），不得用 curl 结果替代客户端证据。
  - 人工：审阅 diff 确认无第二套模态收窄规则、无新增 body 重编码路径、未把 request_logs 的 model/error_code 改动混入本任务（A3-F5 仍延期），且变更文件仅 seed 的四个。

#### S2-T3 · 生产装配 manifest/图片契约

- 依赖：S2-T2、S1-T1、S2-T1
- 文件范围：cmd/gateway/http_contract_test.go
- 依据：任务必要性（源码证据）：生产装配目前只证明 manifest 端点“能返回 200”。cmd/gateway/dataplane_test.go:74-78 对 GET /backend-api/codex/models 只断状态码，cmd/gateway/http_contract_test.go:110,195 两个装配用例都只发 POST /v1/responses，没有任何装配级断言检查 manifest 里的能力值或图片准入（A10-F2 的 confirmation 记录同一结论：cmd/gateway 的 4 个用例只覆盖 6 个客户端端点中的 3 个）。单元层无法替代：internal/gateway/codex_models_test.go:74-85 直接 New(Options{CodexContextWindow: ...}) 构造 Handler，proxy/router 用例也自建依赖图，因此“dataplane.Build 把哪个窗口传下去、ListByProtocol 是否真被接到 store”这类装配漂移在单元测试里不可见（A10-F2 的 confirmation 举了 3 个静态可证的漂移例子）。A2-F7 指出的正是这条链路：能力数据要到达 manifest 需要新的 store 读路径与 builder 签名（internal/gateway/models.go:46-48,124-162；internal/store/routes.go:239-263），而当前 builder 只接收 []string。本任务用 buildServer + 临时 SQLite + 假上游把 store→gateway→HTTP JSON 整条链路钉死，与 NB-R1 标准 2、NB-R2 标准 3 直接对应。

为什么断言值这样选：internal/gateway/codex_models.go:237-247 的 resolveCodexContextWindow 优先级是 configured>0 → GATEWAY_CODEX_CONTEXT_WINDOW → 128000（internal/config/config.go:28-29,68,107），而生产链路 dataplane.go:119-124 传的是 cfg.CodexContextWindow。若测试不显式注入，任何窗口断言都会与内置常量 128000 不可区分；注入 64000 后，“未声明回退全局”的断言才真正验证接线。窗口取最小值、模态取最窄、reasoning 取交集并回退 [none]，是骨架 NB-R1 标准 1 与 architecture.data 给出的规则，本任务只做端到端复核，不重新定义规则（定义在 S2-T1）。

范围与取舍：只写 cmd/gateway/http_contract_test.go，不改生产代码。A10-F2 的 fixSuggestion 还提到 chat 端点、/v1/models/{model}、404/405、405 Allow 等，那些与本批 NB-R1/NB-R2 无关，若塞进本任务会让文件扩大而不增加本批需求的覆盖率；因此本任务只关闭 A10-F2 中“manifest 字段值 + 客户端图片准入”这一部分，finding 不能整体标记为已修复，其余端点仍为未覆盖项，需在交付记录中明示（不让 skill/复核误以为 A10-F2 已清零）。同理，真实 Codex 0.164 客户端解析仍无证据（A10-F1，D4 决定单独立项）；本任务的证据层级是 hermetic 生产装配，交付说明不得表述为“客户端契约已验证”，NB-R6 标准 2 要求的“未执行层级如实列出”在这里同样适用。

需要复核的规格空隙（不自行假定）：① NB-R1 标准 1 写“levels=[low,high]”，隐含 supported_reasoning_levels 按枚举序输出（none<low<medium<high<xhigh<max<ultra）而非管理员声明顺序；骨架没有一句话规定排序，本任务按有序序列断言，若 S2-T1 实现为“保持声明顺序且管理员可能倒序录入”，该断言需要与 S2-T1 对齐后确定（断言本身不应是随机的）。② “default 取 none 或最小档”里的“最小”同样依赖枚举序；本任务只在 [low,high] 上断言 default=low（两种定义一致），避免依赖更细的排序假设。③ D1 尚未确认：cap-undeclared/cap-partial-declared 断言未声明模态为 [text,image]、窗口回退全局 64000，这是骨架推荐值而非用户决定；若用户选择 D1=text，两条断言与 manifest 实现都要改，属于影响实现的关键不确定，已按输入要求在此明示供复核。④ 骨架把 model_does_not_support_image、stateful_route_unavailable、unsupported_parameter 列为新错误码（architecture.interfaces），但 internal/gateway/errors.go:24-37 的现有常量和 observability.go:335-356 的预检映射都没有它们；本任务断言响应体里的精确 code，同时明确不断言 request_logs 的 error_code（当前预检按状态码映射为 invalid_request），这是对“可追踪”的收窄而非放弃。⑤ 原始需求要求“固定版本客户端契约及真实验收”，本任务只提供装配级证据，不覆盖真实 Codex/ZCode（A10-F1 维持延期），这是本批已知缺口，不能因本任务通过而关闭。

执行方式：staged。改动只有一个测试文件、使用 t.TempDir() 临时库与进程内假上游，无生成器、无共享可变产物、无并发写同一文件，因此可与其他已合入任务并行暂存；唯一硬依赖是 S2-T2（图片准入）与间接的 S2-T1/S1-T1（能力字段与聚合）。

本轮为只读规划：allowShell=false，未运行 go test、go vet、go build、npm、docker、race，未启动服务或假上游，未读取 gateway.env、数据目录或任何凭据；因此本任务列出的 chk-s2t3-assembly（骨架既有）与 chk-s2t3-contract（新增定向用例）均为待执行检查，任何“通过”都不存在。
- 步骤：
  - 先读已落地的 S1-T1/S2-T1/S2-T2 代码并记录实际标识符：store.ModelRouteInput 的三个能力字段与模态常量名、store.ModelRoutes.ListByProtocol 签名、gateway 侧聚合函数与 manifest 字段（context_window/max_context_window/input_modalities/supported_reasoning_levels/default_reasoning_level）、错误码 model_does_not_support_image 与图片解析入口（internal/gateway/router.go:192-227 处 serveData 的读 body/取候选之后）。只允许按实际命名调整标识符，不得改动断言中的业务值。
  - 在同文件做行为不变的 fixture 抽取：新增 contractChannelFor(t, st, envelope, name, url, protocol, priority)（只建渠道），把现有 contractChannel 改为调用它后继续建 'contract-model' 路由（cmd/gateway/http_contract_test.go:47-68 语义不变）；新增 contractRoute(t, st, store.ModelRouteInput)、contractPost(t, client, url, key, body)（contractRequest 改为调用 contractPost，body 仍为 '{"model":"contract-model","input":"hello"}'）。现有两个用例 TestProductionHTTPStreamFallbackAndRuntime、TestProductionHTTPBudgetDelivers504AndReleasesSlot 的断言一字不改。
  - 新增 TestProductionCodexManifestCapabilities：contractStore 后设 cfg.CodexContextWindow = 64000（显式注入，见 internal/dataplane/dataplane.go:119-124 链路），不依赖 GATEWAY_CODEX_CONTEXT_WINDOW 环境变量；用一个共享的 httptest 假上游统计计数，所有渠道 base URL 指向它；种 4 个 openai_responses 别名：cap-declared（1 路由，text_image、32000、[low,high]）、cap-window-min（2 路由，32000 与 200000，均 text_image、[none]）、cap-undeclared（1 路由，全未声明）、cap-partial-declared（1 路由声明 200000+text_image+[low]，1 路由未声明）；另种 1 个仅 Chat 协议渠道上的别名 chat-only。
  - 在 TestProductionCodexManifestCapabilities 中用 buildServer(cfg, st) + httptest TCP 服务，GET /backend-api/codex/models（Authorization: Bearer cfg.ClientKey），把 models 解码为带 json tag 的强类型切片（slug、context_window、max_context_window、input_modalities、supported_reasoning_levels[].effort、default_reasoning_level），按 slug 建 map 后逐别名断言精确值：cap-declared → 32000/32000、[text,image]、[low,high]、default low；cap-window-min → 32000/32000；cap-partial-declared → 64000/64000、[text,image]、[none]、default none；cap-undeclared → 64000/64000、[text,image]、[none]、default none；slug 集合恰为这 4 个（chat-only 不出现）；最后断言共享假上游计数为 0。assert 失败信息打印完整 body。
  - 新增 TestProductionImageAdmission：同一别名 img-alias 两条 Responses 路由——ch-text（input_modalities=text、32000、[none]）与 ch-vision（text_image、200000、[none]），各自独立 httptest 假上游并带 atomic 计数，假上游返回 200 + application/json + {"id":"resp_text_ok","object":"response","status":"completed","output":[]}。先 GET manifest 断言 img-alias 的 input_modalities 恰为 ["text"]（收窄生效）。
  - 在同一用例内 POST Responses 图片请求（body: {"model":"img-alias","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}]}），断言 400、解码 error.type=invalid_request_error 且 error.code=model_does_not_support_image、两个假上游计数均为 0；用响应头 X-Request-ID 走 contractLog，断言 Request.HTTPStatus=400、Request.Status=error、len(Attempts)==0，且不断言 ErrorCode。
  - 同一用例内 POST 纯文本请求到 img-alias，断言 200、body 含 resp_text_ok、两假上游计数之和恰为 1，并用 plane.Runtime 断言两条渠道 state=ready、in_use=0（拒绝未占槽位、未罚渠道）。
  - 本地执行 go test -count=1 -run '^(TestProductionCodexManifestCapabilities|TestProductionImageAdmission)$' -v ./cmd/gateway 与 go test -count=1 ./cmd/gateway，记录命令与输出；确认 -v 输出中两个用例均为 PASS（防止用例名拼错导致定向检查空跑）；记录 git status --porcelain 与 git ls-files e2e（仍为 0 行）作为范围守卫。
  - 若断言与实现冲突（manifest 未按共同能力聚合、图片请求仍到上游、窗口取到 128000 而非注入的 64000），停止并作为阻塞 finding 上报，附命令与输出；不改 internal/ 生产代码、不降低断言、不新建或恢复 e2e 文件。
- 行为验收：
  - 机器（chk-s2t3-assembly、chk-s2t3-contract）：生产装配下 GET /backend-api/codex/models 返回 cap-declared 的 context_window 与 max_context_window 均恰为 32000，input_modalities 恰为 ["text","image"]，supported_reasoning_levels 的 effort 序列恰为 ["low","high"]，default_reasoning_level 恰为 "low"——比对的是具体 JSON 值，不是 200 或键存在。
  - 机器（chk-s2t3-assembly、chk-s2t3-contract）：两候选声明 32000/200000 的 cap-window-min 取 32000；一候选声明 200000、一候选未声明的 cap-partial-declared 取 64000，即 min(GATEWAY_CODEX_CONTEXT_WINDOW=64000, 200000)；若装配没有把 cfg.CodexContextWindow 传进聚合，断言会得到内置默认 128000 而失败。
  - 机器（chk-s2t3-assembly、chk-s2t3-contract）：全未声明的 cap-undeclared 取 64000/[text,image]/[none]/default none；cap-partial-declared 的 reasoning 因存在未声明候选回退为 [none]、default none（不是声明的 [low]）。
  - 机器（chk-s2t3-assembly、chk-s2t3-contract）：manifest 的 slug 集合恰为 4 个 openai_responses 别名；仅 Chat 协议的 chat-only 不出现；整个 manifest GET 期间共享假上游计数为 0，说明清单来自 model_routes 而非上游探测。
  - 机器（chk-s2t3-assembly、chk-s2t3-contract）：同一别名两候选（仅文本 + 文本图片）时 manifest 的 input_modalities 恰为 ["text"]；携带 Responses input_image 的 POST 返回 400、error.type=invalid_request_error、error.code=model_does_not_support_image，两个假上游计数均为 0（请求未离开网关）。
  - 机器（chk-s2t3-assembly、chk-s2t3-contract）：同一别名纯文本 POST 返回 200、body 含上游标记 resp_text_ok、两假上游命中数之和恰为 1，且两条渠道 Runtime 仍为 state=ready、in_use=0（拒绝未泄漏槽位、未改动渠道健康状态）。
  - 机器（chk-s2t3-assembly、chk-s2t3-contract）：图片拒绝响应带 X-Request-ID，request_logs 对应行 http_status=400、status=error、attempts 为 0 条，用户可据此追踪；不校验 error_code（预检拒绝日志只按状态码映射为 invalid_request，属 A3-F5/F6 已知缺口，见 rationale 的收窄说明）。
  - 人工（范围守卫，非行为证据）：交付 diff 只含 cmd/gateway/http_contract_test.go；git ls-files e2e 输出仍为 0 行，git status --porcelain 无生产代码与 e2e 变更，未恢复 526237e 删除的文件。

### S3 · 有状态 Responses 边界

给 background、conversation、previous_response_id 等有状态请求明确边界：background 拒绝，状态字段仅单候选钉住，不换渠道、不误冷却；文档写清未实现资源面。

#### S3-T1 · 有状态 Responses 入口边界

- 依赖：S2-T3、S1-T1、S2-T1、S2-T2
- 文件范围：internal/gateway/request_shape.go、internal/gateway/router.go、internal/gateway/router_test.go、internal/gateway/errors.go
- 依据：任务必要性（源码证据，本机 HEAD=a202725f6ea0dce3f845c5565db226ccf7e56f1e，main）：当前网关对 Responses 的有状态/后台语义零处理。本轮 grep -rn 'encrypted_content|previous_response_id|item_reference|conversation' --include=*.go --include=*.ts --include=*.tsx internal cmd web/src 输出为空，grep -n 'background' --include=*.go internal cmd 同样为空，与 analysis-index 的 0 命中结论一致。请求体只在 internal/gateway/router.go:192-196 读一次、internal/gateway/router.go:262-286 只解析 model，随后 internal/gateway/router.go:201-215 取候选、internal/gateway/router.go:221-227 直接交给 Forwarder；错误码表 internal/gateway/errors.go:24-37 没有 unsupported_parameter 与 stateful_route_unavailable，也没有对应 writer。结果是 background:true 被当普通请求发上游、2xx（含 202）按成功写回（A4-F2/A10-F5 的静态链路），而客户端唯一的取回手段 GET /v1/responses/{id} 落在 internal/gateway/router.go:153-170 的 default 分支 404（docs/plans/api-gateway-plan.md:183 也写明第一版只实现 POST /v1/responses）；带 previous_response_id/conversation 的请求与普通请求一样在候选间按 priority→last_used_at 轮转（internal/store/routes.go:209-225），失败时复用同一 body 换渠道（internal/gateway/proxy.go:213-250、internal/gateway/stream.go:640-649；body 复用与 excluded 逻辑见 A4-F1/A5-F2 的 confirmation），上游作用域 id 跨渠道必然失效（A5-F2）。本任务把这两类语义挡在入口：background 一律 400，状态字段只在别名恰有 1 条启用同协议路由时放行并在 RequestInfo 钉住该渠道。

为什么判定放在 router 而不是 proxy/upstream：候选集只在 internal/gateway/router.go:201-215 可得，proxy 层再查一次会重复并与图片准入（S2-T2）分叉；RequestInfo 是既有的路由→代理 seam（internal/gateway/router.go:74-79），proxy 与 stream 都从 context 取（internal/gateway/proxy.go:181-186、internal/gateway/stream.go:610-614），所以在 router 写入 Stateful/PinnedChannelID 就能同时覆盖流式与非流式，S3-T2 不需要重新解析 body。upstream 侧只应替换 model（A5-F2/A10-F5 的 confirmation 记录 internal/upstream/client.go:468-489 用 map[string]json.RawMessage 仅覆盖顶层 model），不能塞协议语义，也不能污染探针。

业务后果：background=true 在入口 400 后客户端拿不到 queued/id，也不会在上游留下既不能轮询也不能取消的任务（A4-F2 影响链在 POST 侧被切断）；多候选别名上的有状态请求从『间歇 4xx + 跨提供方发送不透明 id』变成确定性的 400 stateful_route_unavailable，用户按提示拆成单候选别名即可（D2 推荐值）。单候选放行时本任务只负责钉渠道与置位，真正的 AcquirePinned、maxAttempts=1 与失败 scope 在 S3-T2（dependsOn 已写明），因此本任务通过不等于 A4-F1/A5-F2 已修复，也不等于 NB-R4 的『有状态 4xx/未知 SSE 失败不冷却渠道』已满足。

规格空隙与需复核项（不自行假定，逐条列出）：① NB-R4 没有规定优先级：background 不支持、model_not_available、图片不支持同时命中时返回哪个。本任务选择 background 先于候选查询（internal/gateway/router.go:196 之后、:201 之前），用『映射读取失败仍返回 400』把它固定成可观察行为；代价是『未知模型 + background=true』由 404 变 400，无客户端依赖证据。② NB-R4 没定义『有状态信号存在』的判定：空串、null、conversation 的字符串/对象、类型非法的值、include 里的 reasoning.encrypted_content、tools schema 文本、store:true 都未说明。本任务固定为：previous_response_id 非空字符串、conversation 非 null、input 数组中 type=item_reference 或非空 encrypted_content；include 与 tools 文本不扫描；store 不视为有状态（NB-R4 未列）。③ 最关键的未验证假设：Codex 0.164 是否在常规多轮里回传 reasoning.encrypted_content（D2 风险只列了 previous_response_id/conversation/background）。若会，本任务会让主客户端在多候选别名上直接 400；上线前必须取得真实客户端证据，或由用户决定加密推理项暂不视为有状态（改动点只在谓词函数与两条用例）。④ store:true 的响应在多候选别名上仍不可按 id 取回（GET 404，A5-F1 未关闭）；本任务只拒绝 background，不改变 store 透传，这与 NB-R4 的字面范围一致，但与『不要保持创建成功却无法取回』的诉求仍有距离。⑤ 单候选钉住只保证不换渠道，不保证历史 response 真在该渠道（无 response_owners，notDoing 明确不做）；别名从 1 候选变 2 候选或渠道被替换后，旧 id 仍会失败。⑥ GET/DELETE/cancel/Conversations 仍 404，本任务只加回归断言，NB-R4 的人工标准（文档写明）由 S3-T3 的 docs/clients/codex-0.164.md 满足。⑦ 预检拒绝日志：两类 400 会经 internal/gateway/observability.go:291-322 落库，但 error_code 走 internal/gateway/observability.go:335-356 的默认分支记成 invalid_request、model 为空；这属 notDoing 的 A3-F5/F6，本任务不断言精确 code 也不改 observability.go。

与相邻任务的边界：S2-T2 已占用同一组四个文件（request_shape.go/router.go/errors.go/router_test.go）建立图片准入，S3-T1 只在其结果上追加状态信号与两处准入，必须后置（dependsOn S2-T3，S2-T3 依赖 S2-T2；文件级依赖实际是 S2-T2），不得与之并行编辑同文件；S3-T2 消费 Stateful/PinnedChannelID 实现 AcquirePinned、maxAttempts=1 与失败 scope，本任务不改 proxy/stream/routing；S3-T3 负责 cmd/gateway 生产装配（真实上游 0/1 次计数）与客户端文档。因此本任务证据层级是 router 级 hermetic 单测（Forwarder 调用次数表达上游次数，internal/dataplane/dataplane.go:119-124 证明 Forwarder 即 proxy），不是生产装配级；NB-R4 标准 2、3、4 分别由 S3-T2、S3-T3 与文档承担，本任务不据其自证。

执行方式 staged：四个源码文件的常规编辑，无生成器、无共享可变产物、写文件互不重叠且可单独 review/commit；唯一约束是与 S2-T2 同文件、必须按 dependsOn 顺序执行。

本轮为只读规划：未运行 go test/go vet/go build/npm/docker/race，未启动服务或假上游，未读取 gateway.env、数据目录或凭据；git rev-parse HEAD→a202725f6ea0dce3f845c5565db226ccf7e56f1e（main），git status --porcelain→'?? nul'（与本任务无关的未跟踪文件，未触碰）。chk-s3t1-stateful（骨架既有）与 chk-s3t1-stateful-cases（本任务新增）均为待执行检查，本轮不存在任何通过结论。
- 步骤：
  - 先读 S2-T2 落地后的四个扩展点并记录实际标识符：internal/gateway/request_shape.go 的 shape 结构与函数名、HasImage 的判定语义；internal/gateway/router.go:201-225 中图片准入的位置；internal/gateway/errors.go 的 model_does_not_support_image 常量与 writer；internal/gateway/router_test.go:81-158 的 helper（newTestHandler/performRequest/bearerHeaders/decodeError）与新增用例名。若 request_shape.go 不存在或图片准入未接进 serveData，停止并作为阻塞 finding 上报——S3-T1 不实现、也不复制一份图片解析器。
  - 扩展 internal/gateway/request_shape.go：在既有 shape 结构上追加 Background bool 与 Stateful bool，尽量在同一次 json 解码里填充（若 S2-T2 已把图片判定写成独立函数且结构不可无痛扩展，则新增同文件的 parseStatefulSignals，并在交付记录写明多一次解码的取舍）。顶层先用 map[string]json.RawMessage 取 background/previous_response_id/conversation/input，再逐键容错解码，避免某个字段类型不符导致整体 Unmarshal 失败；不要复用 internal/gateway/proxy.go:596-605 那种整结构解码，因为它在字段类型非法时会丢掉全部信号。
  - 把『有状态信号』收敛成单一谓词函数（只此一处实现，供测试直接驱动）：background 仅在 JSON 布尔 true 时为真（false/缺失/字符串等类型一律为假，网关不新增 schema 校验，非法类型交上游 400）；previous_response_id 为 TrimSpace 后非空的字符串；conversation 存在且不是 JSON null（字符串与对象都算，类型畸形也按保守方向算有状态）；input 为 JSON 数组时逐项看 type=="item_reference" 或 encrypted_content 为非空字符串；input 为字符串或其它形态不扫描。include:["reasoning.encrypted_content"] 与 tools schema 里的同名字样不算有状态（不做整串文本匹配）。
  - 在 internal/gateway/errors.go 新增 errorCodeUnsupportedParameter="unsupported_parameter"、errorCodeStatefulRouteUnavailable="stateful_route_unavailable"，以及 writeUnsupportedParameter/writeStatefulRouteUnavailable 两个 writer：400 + errorTypeInvalidRequest + 固定 message（不含任何字段取值），沿用 writeAPIError 的 no-store envelope（internal/gateway/errors.go:40-42）。注释写明不把两个 code 加进 internal/gateway/observability.go:335-356 的 preflightErrorCode（notDoing 的 A3-F5/F6 延后项），因此 request_logs 里这两类 400 仍记 invalid_request、model 为空。
  - 在 internal/gateway/router.go:74-79 的 RequestInfo 增加 Stateful bool 与 PinnedChannelID int64，注释写明零值=无状态、PinnedChannelID 是 channels.id（不是 model_routes.id）；消费者是 S3-T2 的 proxy/stream，二者已从 context 取 RequestInfo（internal/gateway/proxy.go:181-186、internal/gateway/stream.go:610-614），所以无需改这两处。
  - 在 serveData 插入两处判定，顺序固定：(a) decodeModel 之后、h.routes 判空之前（internal/gateway/router.go:196-204）：protocol==ProtocolOpenAIResponses 且 shape.Background → 400 unsupported_parameter 并 return，不查库、不调 Forwarder；(b) S2-T2 图片准入之后、构造 RequestInfo 之前（internal/gateway/router.go:221-225）：stateful := protocol==ProtocolOpenAIResponses && shape.Stateful；stateful 且 len(candidates)>1 → 400 stateful_route_unavailable；stateful 且 len==1 → info.Stateful=true、info.PinnedChannelID=candidates[0].ChannelID；其余保持零值。不改 Chat 路径、不改候选查询、不改既有错误 envelope。
  - 在 internal/gateway/router_test.go 新增三个用例：TestParseRequestShapeStatefulSignals（纯函数表驱动，覆盖全部正反谓词形态）；TestResponsesStatefulAdmission（HTTP：background 在 0/1/2 候选下均 400、selectErr 时仍 400、未鉴权仍 401、background=false/缺失/"true" 原样转发、单候选 previous_response_id 钉住（构造 model_routes.id=7 与 channel_id=42 并断言 42）、多候选 previous_response_id/conversation 400、无状态请求 Stateful=false/Pinned=0、body 逐字节一致）；TestResponsesStatefulBoundaries（GET/DELETE /v1/responses/resp_prev_42、POST /v1/responses/resp_prev_42/cancel、POST /v1/conversations 仍 404 not_found 且 Forwarder 0 次；Chat 带同名字段仍 200 且 Stateful=false；conversation null/previous_response_id 空串容错；400 响应体不含 id 取值；图片+有状态组合按 S2-T2 的 model_does_not_support_image 优先）。每个子用例新建 handler 与 fakeRouteCatalog，不复用计数。
  - 运行 chk-s3t1-stateful-cases（-v 确认三个用例真的执行，防止用例名拼错导致空跑）与 chk-s3t1-stateful（整包不回归），记录命令、退出码与关键输出。若断言与实现冲突（判定顺序、候选计数、S2-T2 的字段名），停止并作为阻塞 finding 上报：不放宽断言、不改 S2-T2 的图片逻辑、不改 proxy/stream/routing/observability。
  - 范围自查：git status --porcelain 与 git diff --stat 只含 request_shape.go、router.go、errors.go、router_test.go；未改 observability.go/proxy.go/stream.go/attempt.go/routing/docs/e2e，未提交未推送。若 S2-T2 与本任务在同一分支并行编辑，先确认其已合入再暂存本任务。
- 行为验收：
  - 机器（chk-s3t1-stateful-cases / TestResponsesStatefulAdmission「background 拒绝」）：POST /v1/responses 与 /backend-api/codex/responses，body 为 {"model":"gpt-5","input":"hi","background":true} → 400、error.type=invalid_request_error、error.code=unsupported_parameter、Cache-Control: no-store，Forwarder 0 次、候选查询 0 次；客户端拿不到 response id，也不会在上游留下既不能轮询也不能取消的后台任务（A4-F2、A10-F5 的 POST 侧在入口被切断）。
  - 机器（同上「拒绝不依赖存储/鉴权优先级」）：fakeRouteCatalog.selectErr 非空（模拟映射读取失败）时，同一 background=true 请求仍是 400 unsupported_parameter 而不是 500；未带 Key 的 background=true 请求仍是 401 invalid_api_key（鉴权先于形状判定），两种情况 Forwarder 均为 0 次。
  - 机器（同上「background 容错」）：background 缺失、为 false、或为字符串 "true" 时请求照常 200 转发，Forwarder 收到逐字节一致的 body、调用 1 次——只有 JSON 布尔 true 触发拒绝，网关不新增请求体 schema 校验，非法类型留给上游判定。
  - 机器（同上「单候选钉住」）：别名恰有 1 条启用 Responses 路由（model_routes.id=7、channel_id=42）时，带 previous_response_id="resp_prev_42" 的请求 200、Forwarder 1 次、body 逐字节原样、RequestInfo.Stateful=true 且 RequestInfo.PinnedChannelID=42（断言不是 7、不是 0，防止把映射行 id 当成渠道 id）。
  - 机器（同上「多候选拒绝」）：同一别名有 2 条启用路由（两个渠道）时，带非空 previous_response_id 的请求 400、error.code=stateful_route_unavailable、Forwarder 0 次、候选查询恰 1 次——判定基于真实候选集，且未经由代理层向上游发出任何请求。
  - 机器（同上「conversation 两种形态」）：conversation 为 "conv_x" 或 {"id":"conv_x"} 时，2 候选 → 400 stateful_route_unavailable；1 候选 → 200 + Stateful=true + PinnedChannelID=该渠道；conversation 为 null 或字段缺失 → 视为无状态，2 候选照常 200 转发。
  - 机器（同上「item_reference 与 encrypted_content」）：input 数组含 {"type":"item_reference","id":"msg_x"} 或含非空 encrypted_content 的 reasoning 项时，2 候选 → 400 stateful_route_unavailable、1 候选 → 200+Stateful=true；input 直接是字符串时 2 候选 200 且 Stateful=false。
  - 机器（TestParseRequestShapeStatefulSignals + TestResponsesStatefulBoundaries「防误判」）：include:["reasoning.encrypted_content"]、tools 的 description/schema 中出现的 "encrypted_content"/"item_reference" 字样、以及 input_image 顶层键，都不使请求变为有状态：2 候选仍 200 转发、Stateful=false——判定只看 input 项的结构化字段，不做整串文本匹配。
  - 机器（TestResponsesStatefulBoundaries「非 Responses 协议」）：POST /v1/chat/completions 的 body 带 previous_response_id/conversation/background:true → 200 转发、body 逐字节原样、Stateful=false；Chat 协议行为零变化（R-010 的字段原样透传）。
  - 机器（同上「错误不泄漏」）：两类 400 的响应体只含固定 message，不含 previous_response_id/conversation 的取值（如 resp_prev_42、conv_x）；error envelope 仍是 message/type/code 三字段且 no-store。
  - 机器（同上「资源面仍 404」）：GET 与 DELETE /v1/responses/resp_prev_42、POST /v1/responses/resp_prev_42/cancel、POST /v1/conversations 均返回 404 not_found 且 Forwarder 0 次——本任务不新增生命周期端点，只把『不实现』固定成可回归的边界（文档与 A5-F1/A5-F3 的关闭在 S3-T3，不在本任务）。
  - 机器（同上「与图片准入的优先级」）：同一请求既有 Responses input_image（别名共同模态为 text）又有 previous_response_id 且 2 候选时，返回 400 model_does_not_support_image（S2-T2 的准入先于有状态判定），两个上游假源计数 0；该顺序被用例固定，若实现顺序不同则按实际顺序调整断言，不得删掉其中一个断言。
  - 机器（chk-s3t1-stateful 整包）：普通请求的 RequestInfo.Stateful=false、PinnedChannelID=0；既有鉴权、405、未注册端点 404、模型不可用、RequestInfo 协议/端点/模型等用例（internal/gateway/router_test.go:175-420、457-545）原样通过，证明入口新增判定没有改变既有契约。
  - 人工（交接项，不在本任务执行）：docs/clients/codex-0.164.md 的 curl 示例由 S3-T3 补齐（background→unsupported_parameter、多候选别名→stateful_route_unavailable、GET/DELETE/cancel/Conversations→404、conversation 只能在网关外创建）；真实 Codex 0.164 是否发送这些字段仍无证据（D2），不得用本任务通过推断客户端行为。

#### S3-T2 · AcquirePinned、maxAttempts=1 与失败 scope

- 依赖：S3-T1、S1-T1、S2-T2、S2-T3
- 文件范围：internal/routing/scheduler.go、internal/routing/scheduler_test.go、internal/gateway/proxy.go、internal/gateway/proxy_test.go、internal/gateway/stream.go、internal/gateway/stream_test.go、internal/gateway/attempt.go、internal/gateway/attempt_test.go
- 依据：【范围与基线】只细化种子 S3-T2：AcquirePinned、maxAttempts=1 与失败 scope，服务于 NB-R4 的第二条机器判据（“AcquirePinned 只返回指定 channel；不存在→ErrNoCandidates；熔断/满载沿用现有 503/429 语义；有状态 404/未知 SSE 失败使用 request scope，普通请求 404 仍 model scope、未知 SSE 仍 channel scope”）。本轮只读：`git rev-parse --abbrev-ref HEAD` → main；`git rev-parse HEAD` → a202725f6ea0dce3f845c5565db226ccf7e56f1e；`git status --porcelain` → `?? nul`（未跟踪，未触碰）。逐行通读 internal/routing/scheduler.go（768 行）、limiter.go（178 行）、internal/gateway/proxy.go（623 行）、stream.go（1151 行）、attempt.go（441 行）、classification.go（128 行）、router.go（286 行）、proxy_test.go（880 行）、stream_test.go（979 行）、scheduler_test.go（1298 行）、reliability_test.go（384 行），未运行 go test、go vet、npm、docker、race、服务或真实上游。

【为什么要 AcquirePinned，而不是只把 maxAttempts 改成 1】maxAttempts=1 只减少尝试次数，第一次选择仍由排序决定：scheduler.go:363 在取得槽位时就 touchLastUsed，sortCandidates（scheduler.go:464-477）按 route.priority→channel.priority→last_used_at（NULL 最先）→route.id 升序。于是同一别名两候选时，第一轮成功的 A 被记为“刚用过”，带 previous_response_id 的下一轮先选 B：B 若返回 400，attempt.go:146-153 是 Retry=false，proxy.go:315-317/stream.go:746-748 直接 break，客户端拿到终态错误；B 若返回 404，attempt.go:121-128 是 Retry=true，但先按 attempt.go:65-75 记 model scope，scheduler.go:676-683 把 model scope 强升为 server 窗口（默认 20s），健康渠道被冷却。另一条路径是竞态：router 用 ListByModelAndProtocol 计数（router.go:206、store/routes.go:196-226），proxy 再查一遍（proxy.go:230），这中间管理员新建一条映射/渠道，候选就从 1 变 2，第一次尝试可能落到新渠道（last_used 为 NULL 排最前）；AcquirePinned 在候选选择阶段就把集合限制为钉住渠道，这才是消除“换渠道”的地方，attempt 上限只是兜底。

【为什么共用 acquireFrom】AcquireExcluding 的循环（scheduler.go:310-393）包含槽位、复查配置、ConnectionRevision、confirmLimit、恢复许可票据、两次 ctx 检查、touchLastUsed、configured/capacityBlocked 的错误区分，任一处复制都会产生第二份会漂移的行为；本任务要求逐行提取后由两个入口共用，并用 `go test -count=1 ./internal/routing` 的既有用例（含 TestSchedulerAcquireExcluding、TestSchedulerCircuitWindows、TestSchedulerTouchOnlyOnAcquire、并发峰值用例）证明没有回归。

【为什么扩展现有 CandidateScheduler 而不是新接口或运行期断言】proxy.go:65-70 有 `var _ CandidateScheduler = (*routing.Scheduler)(nil)` 的编译期断言，全仓唯一实现是 *routing.Scheduler（internal/dataplane/dataplane.go 用具体类型构造，gateway 测试 proxy_test.go:163、stream_test.go:25 注入的也是同一个 *routing.Scheduler）；扩一个方法能被编译器检查到，而 `if p, ok := sch.(PinnedAcquirer); ok` 会在实现缺失时静默退回无钉住调度，正好是本次要消除的不确定性。

【A4-F3 为什么只收敛“未识别码”而不是加白名单】SSE 失败分支（stream.go:924-929）先取 upstreamStreamFailure()（stream.go:1058-1066：Status=502/Code=upstream_error/Retry=true/Scope 空/FaultStatus=0），再用 classifyStructuredFailure 细化；classification.go:93-125 的 19 个码一律设置 FaultStatus，只有未命中回退时 FaultStatus 保持 0（classification.go:127），而该分支的失败枚举固定是 502/upstream_error，所以 `Code==upstream_error && FaultStatus==0` 精确对应“未识别”。这样既不需要动 classification.go（不在 seed 的 8 个文件里），也不新增第二份错误码白名单；A4-F3 原建议的“把 previous_response_not_found/invalid_encrypted_content 加进全局白名单”被有意收窄为“仅在有状态请求上收敛”，因为 NB-R4 明确要求“普通请求...未知 SSE 仍 channel scope”，且这三条判据与 reliability_test.go:191-213（model_not_found→model scope、unknown_vendor_error→channel scope）的现有契约冲突。实际触发面不变：带这些状态字段的请求在 S3-T1 之后一律是 Stateful，所以真实场景都被覆盖。

【为什么 404 只认状态码】NB-R4 原文是“有状态 404”，不是“任何 model-scope 失败”。按 status==404 收敛可以覆盖空 body/超长 body 导致的“分类未生效但状态是 404”的情况（classification.go:47-51 在 body 读取失败或 >16KiB 时根本不调用 classifyStructuredFailure），同时保留结构化 model_not_found（上游 400 + code）的 model 冷却。备选方案（有状态请求上把 Scope==model 一律降级）只需把条件放宽一行，但会同时清掉真实模型不可用的冷却，本计划不默认采用，列为待复核决策 Q1。

【客户端可见行为的变化清单（供复核，也是 S3-T3 的文档输入）】有状态请求不再跨渠道失败切换（少一次机会，多一次显式失败）；状态类失败不再冷却渠道/模型（运维看到的渠道健康更“干净”，但上游持续状态错误不会再被自动摘除）；pinned 渠道在请求期间被停用/改配置时 ErrNoCandidates → 404 model_not_available（沿用既有映射，没有新 code）。上游 404 仍是 upstream_not_found、提交前未识别 SSE 仍是 502 upstream_error，本任务不发明新 code（A4-F1 建议的 previous_response_unavailable 属新契约，需先有客户端/上游证据）。

【依赖与未核实前提】S3-T1 尚未落地，本任务依赖它设置 RequestInfo.Stateful/PinnedChannelID（骨架 S3-T1 goal 明确写这两个名字），并依赖其放行条件为“恰有 1 条启用同协议路由”，否则 upstream_model 不稳定（见 risk①，已在第 1 步设为硬门槛）。Codex 0.164 是否真的发送 previous_response_id/conversation/background 仍未核实（analysis A4 gaps 原文），若它根本不发这些字段，本任务只是防御性加固，价值取决于 D2 的取舍；上游对未知 previous_response_id 的实际状态码同样未核实，故 acceptance 只用契约化的假上游，不宣称对真实 provider 成立。

【待复核问题（不自行假定）】Q1：有状态请求上的 model-scope 失败是否要一律降级为 request scope（当前只认 HTTP 404）？Q2：pinned 渠道消失时用 404 model_not_available 是否可接受，还是要 503/专用 400（会在 S3-T3 文档与 errors.go 引入新语义）？Q3：有状态请求是否应停止写 last_used_at（当前选择保持一致，可能扰动 LRU）？Q4：S3-T1 的放行计数单位是“路由”还是“渠道”（若是渠道，需在其任务内收紧，本任务不补）？Q5：是否为状态类失败引入专用客户端 code（本任务不做）。

【未执行】本轮未运行任何检查：chk-s3t2-pinning 是骨架已有项，chk-s3t2-pinned-scope 为本次新增的定向检查；两者都留给控制器后续执行，不能视为已通过。未读 gateway.env、数据目录、数据库或凭据；未重启 8080/8095-8097；未提交、未推送、未恢复 e2e。
- 步骤：
  - 先读 S3-T1 的落地结果，确认 RequestInfo 的新字段名与语义（骨架 S3-T1 goal 写的是 Stateful 与 PinnedChannelID）：`grep -rn "Stateful\|PinnedChannelID" internal/gateway`；同时确认本任务要新增的调度方法尚不存在：`grep -rn "AcquirePinned" internal/`（此刻应为空）。S3-T1 若用了别的字段名，本任务按实际名改名即可，但测试函数名与 check 命令保持不变。必须同时核对 S3-T1 的放行条件：只有“该别名恰有 1 条启用同协议路由”时才允许状态字段，PinnedChannelID 才有唯一 upstream_model（骨架 S3-T1 goal 原文为“恰有 1 条启用同协议路由”）。若 S3-T1 按“1 个不同渠道”放行（同渠道可挂两条不同 upstream_model 的映射），不要自行改 S3-T1，也不要在这里加补偿逻辑，直接把它写成交付说明里的需复核项并暂停第 5/6 步之外的实现。
  - internal/routing/scheduler.go：把 AcquireExcluding 里“取候选 + 去 exclude”之后的等待与取槽循环（scheduler.go:310-393）原样提取为未导出方法 `acquireFrom(ctx context.Context, protocol string, candidates []Candidate) (Candidate, *Slot, error)`，逐行保留：`!candidate.Channel.Enabled` 跳过、`breaker.allow`、`limiter.TryAcquire`、仅对拿到槽位的候选 `channels.Get` 复查、`ConnectionRevision`/协议不一致则置 `Enabled=false`、`confirmLimit`、`breaker.acquire`、两处 `ctx.Err()`、`touchLastUsed`、`slot.onRelease = breaker.cancel`、`configured`/`capacityBlocked` 推导 ErrNoCandidates/ErrAllCircuitsOpen、`noWait`/deadline/poll 的 ErrChannelCapacity。acquireFrom 使用调用方传入的 protocol 原值做复查比较（不要额外 TrimSpace），保持现有语义。AcquireExcluding 改为“取候选 → 去 exclude → acquireFrom”。
  - internal/routing/scheduler.go：新增 `AcquirePinned(ctx, clientModel, protocol string, channelID int64) (Candidate, *Slot, error)`：channelID<=0 直接返回 ErrNoCandidates（不查库、不等待）；否则 `s.candidates(ctx, clientModel, protocol, false)` 后过滤 `candidate.Channel.ID != channelID`，再交给 acquireFrom。注释必须写明三条边界：只尝试指定渠道、任何情况下都不退回其它候选（不换渠道是它的存在理由）；熔断/满载/等待与 Acquire 完全一致，错误仍是 ErrNoCandidates/ErrAllCircuitsOpen/ErrChannelCapacity；取得槽位后仍按现有逻辑写 last_used_at（有状态请求同样计一次使用）。不得改 candidates/sortCandidates/compareLastUsed、不得改默认窗口与 CapacityWait 语义。自检：`go vet ./internal/routing` 与 `go test -count=1 ./internal/routing`（既有 1298 行测试全部保持通过）。
  - internal/routing/scheduler_test.go：新增 TestSchedulerAcquirePinned（store 落地）：alpha/beta 两渠道同优先级、各一条 responses 映射；先 `channels.TouchLastUsed(alpha)` 让 alpha 有 last_used、beta 为 NULL（按 scheduler.go:464-477 此时 beta 本应优先）。断言：pin beta 返回 beta；pin alpha 返回 alpha（证明不是靠排序碰巧选中）；pin 999 返回 ErrNoCandidates；pin 一个 openai_chat 渠道而协议为 openai_responses 返回 ErrNoCandidates；把 alpha 停用后 pin alpha 返回 ErrNoCandidates；每次成功都 slot.Release 且 `scheduler.limiter.InUse(...)==0`；同渠道两条映射时仍只返回该渠道一条候选（用 Route.ID 断言选中的是排序首条，作为跨任务不变量的记录）。
  - internal/routing/scheduler_test.go：新增 TestSchedulerAcquirePinnedCircuitAndCapacity（fakeRouteCatalog/fakeChannelCatalog/fakeClock，仿 scheduler_test.go:1184-1253 与 :690-770）：①用 Acquire+RecordFailure{Scope:channel,Status:401} 打开渠道级熔断后，pin 该渠道返回 ErrAllCircuitsOpen，且同别名的另一健康渠道不会被返回（本测试只有一条路由时用第二个渠道的假目录构造两候选）；②MaxConcurrency=1、持有槽位、CapacityWait 设为 10ms、CapacityPoll 1ms 时 pin 返回 ErrChannelCapacity；③释放后 pin 成功并 `scheduler.Circuit(id).Open==false` 的恢复路径仍可走；④容量等待期间不得对其它渠道 TouchLastUsed（复用 fakeLastUsed 计数）。
  - internal/gateway/attempt.go：新增四个小件并写注释。①`statefulFailure(failure proxyFailure) proxyFailure`：强制 `Scope=routing.FailureScopeRequest`、`Retry=false`，其余字段不动（Status/Code/Message 保持客户端可见值不变）。②`unattributedFailure(failure proxyFailure) bool`：`failure.Code==errorCodeUpstreamError && failure.FaultStatus==0`，注释写清依据——classification.go:93-125 对已识别码一律设置 FaultStatus，只有未命中的 fallback 保持 0，而 SSE 失败分支的固定枚举是 502/upstream_error。③`maxAttemptsFor(info RequestInfo, configured int) int`：`info.Stateful` 返回 1，否则原样返回 configured。④`var errStatefulUnpinned = errors.New("gateway: 有状态请求缺少钉住渠道")`。不新增客户端错误码、不改 routingFailure 的既有推导表。
  - internal/gateway/attempt_test.go：新增 TestStatefulFailureScope 表驱动（纯函数，不经过 Handler）：statefulFailure 作用于 channel-scope 502 后 Scope=request、Retry=false、Status/Code 不变；unattributedFailure 对 upstreamStreamFailure()（stream.go:1058-1066）为真，对 classifyStructuredFailure(upstreamStreamFailure(), `{"error":{"code":"server_error"}}`) 为假，对 classifyUpstreamStatus(400) 与 classifyUpstreamStatus(404) 为假；maxAttemptsFor 覆盖 (stateful,3)=1、(stateful,1)=1、(非stateful,3)=3。断言必须打到具体字段值，不允许只判 true/false 存在性。
  - internal/gateway/proxy.go：①CandidateScheduler 增加 `AcquirePinned(ctx, clientModel, protocol string, channelID int64) (routing.Candidate, *routing.Slot, error)`；只扩这一个方法，编译期断言 proxy.go:65-70 保证唯一实现是 *routing.Scheduler（仓内无第三方实现，internal/dataplane/dataplane.go 直接用具体类型）。②新增未导出 `acquireForRequest(sch CandidateScheduler, ctx context.Context, info RequestInfo, excluded map[int64]struct{})`：`info.Stateful` 时先判 `PinnedChannelID<=0` 返回 errStatefulUnpinned（schedulingFailure 的 default 会映射成 500 internal_error，不静默退化为无钉住调度），否则调 AcquirePinned；非有状态照旧 AcquireExcluding。③forward（proxy.go:213-325）用 `limit := maxAttemptsFor(info, p.maxAttempts)` 替换两处 p.maxAttempts（循环条件 :223 与切片容量），取候选改走 acquireForRequest；不删 excluded（非有状态路径仍需要），不改 Record*/写日志/预算代码。
  - internal/gateway/proxy.go：runAttempt 增加末位参数 `stateful bool`（proxy.go:331-339），调用点传 `info.Stateful`。在 `failure := classifyHTTPFailure(response, p.now())`（proxy.go:441）之后、RecordFailure（:447）之前插入：当 `stateful && response.StatusCode == http.StatusNotFound` 时 `failure = statefulFailure(failure)`。只按 NB-R4 点名的 404 收敛，不要用 `failure.Scope==model` 放宽（否则结构化 model_not_found 的既有 model 冷却会被一起清掉，与 reliability_test.go:191-213 的现有契约冲突）；该备选写进 rationale 供复核。
  - internal/gateway/stream.go：①ServeHTTP 的尝试循环（stream.go:640，容量 :634）改用 `maxAttemptsFor(info, s.maxAttempts)`，取候选（:653）改走 acquireForRequest；excluded（:745）保留给非有状态路径。②runStreamAttempt 的 SSE 失败分支（stream.go:924-929）：仅在 `class.failure` 为真、分类完成之后追加 `if rt.info.Stateful && unattributedFailure(failure) { failure = statefulFailure(failure) }`；`class.invalid`（同一分支里的畸形帧）不套用该收敛——它是上游协议错误，不是“未知状态码”。不改 interruptedStreamFailure/streamReadFailure/提交后写帧路径，也不改 classification.go。
  - internal/gateway/proxy_test.go：新增 `doStateful(t, info RequestInfo, body string)` 辅助（构造带 RequestInfo 的 context 后直接调 stack.proxy.ServeHTTP，仍是 httptest + deadlineRecorder；注释写明这是绕开 router 的包内证据，生产装配级证据归 S3-T3）。两个用例：①TestProxyStatefulPinnedChannelSingleAttempt——A/B 两渠道各一条映射，TouchLastUsed(A) 让 B 成为 LRU 首选；A 返回 503、B 返回 200；注入 Stateful=true、PinnedChannelID=A；断言客户端得 503 upstream_unavailable、fakeA.hits==1、fakeB.hits==0、latestDetail 的 AttemptCount==1/RetryCount==0/ChannelID==A、A 渠道熔断打开（5xx 仍按 channel scope，本任务不缩小它）。②TestProxyStatefulNotFoundIsRequestScoped——单渠道返回 404 且 body `{"error":{"code":"previous_response_not_found"}}`，用不带注入的 stack.do 发带 previous_response_id 的 body（由 S3-T1 置 Stateful）：断言 404 upstream_not_found、AttemptCount==1、Circuit(ch).Open==false、Runtime(ch).ModelCooldowns==0；同一场景换成不含状态字段的 body 做对照，断言 ModelCooldowns==1（普通 404 仍 model scope）。
  - internal/gateway/stream_test.go：两个用例。①TestStreamStatefulPinnedChannelSingleAttempt——两渠道，A 的 SSE 在提交前发 `event: error` + 已识别码 server_error，B 为正常 SSE；TouchLastUsed(A) 让 B 成为 LRU 首选；注入 Stateful=true/PinnedChannelID=A；断言 fakeA.hits==1、fakeB.hits==0、AttemptCount==1、客户端 502 upstream_error、A 渠道熔断打开（已识别码仍 channel scope）。②TestStreamStatefulUnknownFailureIsRequestScoped——单渠道 SSE 发 `response.failed` + 未识别码 previous_response_not_found，body 带 previous_response_id：断言 1 次上游、客户端 502 upstream_error、Circuit(ch).Open==false、ModelCooldowns==0；对照组用不带状态字段的 body，断言仍按原逻辑冷却/换渠道（首渠道熔断打开或第二渠道被调用），证明确认域只影响有状态请求。
  - 交付时在仓库根本地执行并记录输出：`go test -count=1 ./internal/routing ./internal/gateway`（chk-s3t2-pinning）与 `go test -count=1 -run 'TestSchedulerAcquirePinned|TestStatefulFailureScope|TestProxyStateful|TestStreamStateful' ./internal/routing ./internal/gateway`（chk-s3t2-pinned-scope）。注意 `-run` 无匹配时 go test 仍退出 0，交付说明里必须贴出被选中的测试清单与 ok 行；不跑 e2e、docker、race、真实上游、浏览器。
  - 自检文件范围：`git status --porcelain` 只应出现 seed 的 8 个文件（外加未跟踪的 nul 原样保留）；不得新增文件、不改 classification.go/store/adminapi/web/docs、不恢复 526237e 删除的 e2e、不改 chk-s3t2-pinning 的命令。交付说明附上需要 S3-T3 写进 docs/clients/codex-0.164.md 的行为变化清单（有状态失败不切换、状态类失败不冷却、pinned 渠道消失映射 404 model_not_available、多候选别名 400 属 S3-T1）。
- 行为验收：
  - 机器（chk-s3t2-pinning、chk-s3t2-pinned-scope）：AcquirePinned 只返回钉住的渠道。构造“A 已有 last_used_at、B 为 NULL”（按 scheduler.go:464-477，B 本应优先）时，pin A 返回 A、pin B 返回 B，不出现“钉 A 得 B”；成功获取后释放槽位，`limiter.InUse(A/B)==0`（无槽位泄漏）。
  - 机器（chk-s3t2-pinning、chk-s3t2-pinned-scope）：AcquirePinned 的错误语义与既有调度一致且不退回其它候选——不存在/已停用/协议不匹配的渠道 → ErrNoCandidates；钉住渠道熔断 → ErrAllCircuitsOpen；满载等待超时 → ErrChannelCapacity；三者分别沿用 attempt.go:275-307 的 404 model_not_available、503+Retry-After:1、429+Retry-After:1 映射，不新增错误码。
  - 机器（chk-s3t2-pinning、chk-s3t2-pinned-scope）：有状态非流式请求（Stateful=true、PinnedChannelID=A，A 已 last_used、B 为 LRU 首选）只调用 A 一次：fakeA.hits==1、fakeB.hits==0；request_logs 的 attempt_count==1、retry_count==0、channel_id==A。即使 A 返回可重试的 503 也不会改打 B，即 maxAttempts 从 3 收窄为 1 对两个代理循环同时生效。
  - 机器（chk-s3t2-pinning、chk-s3t2-pinned-scope）：同一场景走流式（SSE）时同样 fakeA.hits==1、fakeB.hits==0、attempt_count==1；A 在语义提交前报已识别码 server_error 时客户端收到 502 upstream_error，且 A 的渠道熔断为打开（已识别码仍是 channel scope，本任务不缩小它的处罚）。
  - 机器（chk-s3t2-pinning、chk-s3t2-pinned-scope）：有状态非流式请求遇到上游 404（body 为未识别的 previous_response_not_found）时，客户端收到 404 upstream_not_found，attempt_count==1，`Circuit(ch).Open==false` 且 `Runtime(ch).ModelCooldowns==0`——不再像现在这样先给该渠道记 20 秒 model 冷却（scheduler.go:651-714）。
  - 机器（chk-s3t2-pinning）：对照组证明默认行为未被放宽——同一 404 由不含状态字段的普通请求发出时，`Runtime(ch).ModelCooldowns==1`（仍按 attempt.go:65-75 的 model scope 冷却），说明 404 收敛只作用于有状态请求。
  - 机器（chk-s3t2-pinning、chk-s3t2-pinned-scope）：有状态流式请求在提交前收到 response.failed + 未识别码 previous_response_not_found 时，只有 1 次上游调用、客户端收到 502 upstream_error、`Circuit(ch).Open==false`、`ModelCooldowns==0`；不含状态字段的对照请求仍打开该渠道熔断（或换到第二渠道），证明“未知 SSE 仍 channel scope”的默认语义保留。
  - 机器（chk-s3t2-pinned-scope）：纯函数表锁死语义边界——statefulFailure 把任意失败变成 request scope 且 Retry=false（Status/Code/Message 不变）；unattributedFailure 只对 (Code=upstream_error, FaultStatus=0) 为真，对 server_error、invalid_request_error、400、404 为假；maxAttemptsFor 对 (stateful,3)=1、(stateful,1)=1、(非 stateful,3)=3。
  - 机器（chk-s3t2-pinning）：防御路径不留后门——Stateful=true 但 PinnedChannelID<=0 时返回 500 internal_error 且假上游 0 次调用，不存在“静默改走无钉住调度”的分支。
  - 机器（chk-s3t2-pinning、chk-s3t2-pinned-scope）：普通请求行为零变化——TestProxyFailsOverOnRetryableStatus、TestProxyDoesNotRetryClientError、TestProxyStopsAfterThreeDistinctChannels、TestStreamFailsOverOn503ThenCompletes、TestHTTPModelErrorIsolatesOnlyModel、TestBoundedStructuredErrorScopes 的断言不被修改且全部通过；普通 404 仍 model scope、未知 SSE 仍 channel scope、结构化 4xx 仍不重试。
  - 人工：审阅 diff 确认只改 seed 的 8 个文件、无新增文件、未改 classification.go/store/adminapi/web/docs/e2e；AcquirePinned 与 AcquireExcluding 共用同一个 acquireFrom 取槽循环（没有第二份熔断/等待/复查逻辑）；sortCandidates、compareLastUsed、touchLastUsed、默认窗口与 CapacityWait 未被改动。
  - 人工：审阅确认没有发明客户端契约——上游 404 仍是 upstream_not_found、提交前未知 SSE 仍是 502 upstream_error，未新增状态专用 code；若复核要求专用 code（A4-F1 曾建议 previous_response_unavailable），走新决策而不是本任务静默加。
  - 人工：交付记录如实列出未执行层级（真实 Codex/ZCode 客户端、Docker、race、真实上游、浏览器）与未核实前提（Codex 0.164 是否真的发送 previous_response_id/conversation/background；上游对未知 previous_response_id 的状态码），并说明“有状态请求能否进入本路径取决于 S3-T1 的探测与 D2 决策”；文档要点的交接对象是 S3-T3。

#### S3-T3 · 生产装配有状态契约与边界文档

- 依赖：S3-T2、S1-T1、S2-T1、S2-T2、S2-T3、S3-T1
- 文件范围：cmd/gateway/http_contract_test.go、docs/deploy.md、docs/clients/codex-0.164.md
- 依据：任务必要性（生产装配证据缺口）：cmd/gateway 的装配级用例目前只有 http_contract_test.go:110 与 :195 两个，均只发 POST /v1/responses；dataplane_test.go:59-78 只断言 /v1/models 的 401/200 与 /backend-api/codex/models 的 200，manifest body 未断言（A10-F2 的 confirmation 记录了同一结论）。有状态边界没有任何装配级入口：internal/gateway/router.go:153-170 只注册 POST，serveData 在 :192-227 读 body、只解析 model、按 model+protocol 取候选；失败切换在 internal/gateway/proxy.go:230-231、:306-317 与 internal/gateway/stream.go:653-654、:744-748 直接换下一渠道；internal/gateway/attempt.go:121-128 把上游 404 标为可重试，:65-80 默认给 404 模型级 scope，internal/routing/scheduler.go:673-675 只有 Scope=request 才不触发熔断。S3-T1/S3-T2 会改 router/proxy/stream/scheduler，但 proxy_test/router_test 各自自建依赖图，A10-F2 的 confirmation 举了多个（4 个）同类装配漂移例子（例如 internal/dataplane/dataplane.go:119-124 的 CodexContextWindow 漏传会被 internal/gateway/codex_models.go:241-245 的同名 env 回退掩盖），因此单元层无法证明 buildServer 真把新行为接上。本任务用 buildServer（cmd/gateway/dataplane.go:15-56）+ 临时 SQLite + 假上游，一次钉死 HTTP 状态码、错误码、上游调用次数、上游收到的 body、request_logs 与 plane.Runtime 快照，对应 NB-R4 标准 3 与 NB-R6 标准 1。

断言值为何能区分实现：contractStore 已设 MaxAttempts=3（cmd/gateway/http_contract_test.go:38），因此无状态 404 的两候选用例必须产生 2 次 attempt；若有人把 maxAttempts=1 全局化，该用例会失败。单候选有状态请求无法在装配层证明“不换第二渠道”——按 NB-R4 的准入规则，多候选在到达调度前就被 400 拒绝，单候选本就没有第二渠道可换；该性质由 S3-T2 的 chk-s3t2-pinning 单元用例负责，本任务只断言“上游恰 1 次、attempts=1”，不能把这一条当成跨渠道不重试的装配级证明。有状态 404 的 request scope 在本层可观察为 plane.Runtime().ModelCooldowns 为 0（internal/routing/scheduler.go:744-767 统计 channel+protocol+upstream_model 的模型级熔断）且随后同一请求仍能到达上游；渠道级 State 在 request scope 与 model scope 下都可能是 ready，因此不用 State 作为证据。假上游 404 body 必须避免 internal/gateway/classification.go:93-126 认识的协议码（invalid_request_error/model_not_found 等），否则会被改判为 400/request 或 404/model 而掩盖 stateful 覆盖逻辑；本任务固定使用 {"error":{"message":"no such response"}}。

文档必要性：docs/deploy.md:78 仍把 GATEWAY_CODEX_CONTEXT_WINDOW 描述为“Codex 模型 manifest 的上下文窗口”，真值化后语义已变为未声明回退/上限；§4:124-126 只写到 v2；§11:327-343 的 8 项人工验收没有 v3 升级/回滚、能力声明或有状态项；docs/clients 目录不存在（本轮 ls docs 只有 deploy.md、plans、requirements、reviews）。A5-F1/F3/F5 的 confirmation 分别证明：GET/DELETE/cancel/Conversations 全 404（internal/gateway/router.go:153-170）、background 创建后无法轮询、store=false 仍进 5 分钟内存环（internal/gateway/observability.go:236,269-279；internal/observability/capture.go:26-35 默认 5 分钟/50 请求/32MiB，管理员保存后 24h）。新增 400 错误码若没有客户端文档，用户无法区分“网关边界”与“上游故障”，而 docs/requirements/api-gateway.md:21-23 把 Codex 与 ZCode 列为第一版必须实际接入验收的客户端。

需要复核的规格空隙（不自行假定）：① background 与 model_not_available 的判定先后未规定；本任务用存在的别名，不依赖顺序，文档也不写顺序。② 状态字段“存在”的语义未定义：空字符串、null、空对象是否算有状态、previous_response_id 与 conversation 是否互斥，需 S3-T1 定稿；测试只用非空值，文档必须写明实际规则（或明确写“非空”），不能默认。③ D2 尚未由用户确认：测试与文档编码的是骨架推荐（background 拒绝、状态字段仅单候选、多候选 400）；若用户选“全部拒绝”或“保持透传”，单候选 200 用例与文档有状态表都要改。④ 骨架 NB-R1 标准 4 要求文档写“Codex 0.164 仅在 provider 配置远端模型目录 URL 时才请求该 manifest”，但本轮可核对证据只支持自定义 provider + model_catalog_url → GET /v1/models?client_version=...（git show 86bc0dd:e2e/testdata/codex-0.164.0/config.toml.tmpl:13-15，该文件已由 526237e 删除，只读历史，不恢复）；/backend-api/codex/models 的客户端触发条件本轮无证据，本任务要求文档窄写并把后者标为未证实，建议复核此收窄。⑤ 预检 400 日志只写 status/endpoint/protocol，error_code 被映射为 invalid_request、model 为空（internal/gateway/observability.go:304-322、335-356；A3-F5/F6 已列为不做），所以行为验收明确不断言这两项。⑥ 有状态流式“未知 SSE 失败 request scope”本任务不做装配级覆盖（需要 SSE 时序假上游），只由 S3-T2 单测证明，文档不得暗示装配级已覆盖。⑦ 仓库没有任何文档测试（本轮 grep *_test.go 无 docs 断言），因此 docs 交付只有人工复核，chk-s3t3-contract/chk-s3t3-assembly 不能证明文档正确。⑧ 原始需求要求 Codex 与 ZCode 真实接入验收（docs/requirements/api-gateway.md:21-23），并写“请求失败时切换到下一个可用渠道”（:47）；本批证据仍为 hermetic，真实客户端 E2E 按 D4/A10-F1 延期，有状态单候选是对 :47 的有意收窄，需在 D2 决策中向用户明示。

执行方式：staged。改动是一个测试文件与两份文档，无生成器、无共享可变产物；与 S2-T3 同文件但依赖链 S3-T3→S3-T2→S3-T1→S2-T3 已串行，故不需要 serial。本轮为只读规划：allowShell=false，未运行 go test、go vet、go build、npm、docker、race，未启动服务或假上游，未读取 gateway.env、数据目录或任何凭据；只读执行了 git rev-parse/git status/git show 86bc0dd:...、ls docs、ls internal/store/migrations 与若干 grep。上面列出的 chk-s3t3-contract（新增）与 chk-s3t3-assembly（骨架既有）均为待执行检查，本轮没有任何“通过”。
- 步骤：
  - 先读已落地的 S2-T3/S3-T1/S3-T2 代码并记录实际标识符：internal/gateway/request_shape.go 的状态字段解析入口与结果结构、internal/gateway/errors.go 新增错误码常量、internal/gateway/router.go 中 background/状态字段拒绝分支的位置与判定顺序、internal/routing/scheduler.go 的 AcquirePinned 签名、internal/gateway/proxy.go 与 stream.go 设置 request scope 的位置、gateway.RequestInfo 新增字段；并读 S2-T3 在同一测试文件留下的 contractChannelFor/contractRoute/contractPost 等 fixture。只允许按实际命名调整标识符，不得改动断言中的业务值（400/200、unsupported_parameter、stateful_route_unavailable、上游调用次数、字段名与值）。
  - 在同文件补最小 fixture，能复用 S2-T3 的不重复实现：一个带原子计数并保存最近请求体的假上游构造器（body 用互斥锁或带缓冲通道保存，所有断言在测试 goroutine 内完成，不在 handler 里调用 t.Fatalf），以及一个可发送任意 JSON body 的 POST 辅助函数（沿用 contractRequest 的 Authorization/Content-Type/读 body 语义）。既有 TestProductionHTTPStreamFallbackAndRuntime、TestProductionHTTPBudgetDelivers504AndReleasesSlot 的断言一字不改。
  - 新增 TestProductionStatefulAdmission：建别名 state-bg（1 条启用 Responses 路由，独立假上游）与 state-multi（2 条启用 Responses 路由，两个独立假上游）。断言 background=true 的 POST /v1/responses 返回 400、error.type=invalid_request_error、error.code=unsupported_parameter、state-bg 假上游 0 次；同一 body 发到 /backend-api/codex/responses 同样 400 且 0 次；对 state-multi 分别发送 conversation 字符串（"conv_state_multi"）、conversation 对象（{"id":"conv_state_multi"}）、previous_response_id（"resp_state_multi"）三种 body，均返回 400、code=stateful_route_unavailable、两个假上游 0 次，且响应体不包含这些 sentinel 值。
  - 在同一用例内对 state-multi 发送无状态纯文本 body（{"model":"state-multi","input":"hello"}），断言 200、两个假上游命中数之和恰为 1，证明多候选别名只对状态字段收窄、未被整体封禁。选一个 400 响应，用响应头 X-Request-ID 走 contractLog，断言 request_logs 的 http_status=400、status=error、protocol=openai_responses、endpoint=/v1/responses、len(Attempts)==0；明确不断言 error_code 与 model（预检拒绝日志的已知缺口）。
  - 新增 TestProductionStatefulPinnedSingleCandidate：建别名 state-pin（1 条启用 Responses 路由），假上游对每次请求返回 200 + application/json + {"id":"resp_pin_ok","object":"response","status":"completed","output":[]} 并记录每次收到的 body。先发 {"model":"state-pin","input":"hi","previous_response_id":"resp_state_pin"}，断言 200、body 含 resp_pin_ok、假上游恰 1 次，且收到的 body 中 model 等于该路由声明的 upstream_model、previous_response_id 仍为 resp_state_pin；再发 {"model":"state-pin","input":"hi","conversation":{"id":"conv_state_pin"}}，断言 200、假上游累计 2 次、收到的 conversation.id 仍为 conv_state_pin；对第一次请求的 request_logs 断言 status=success、attempts=1、protocol=openai_responses、endpoint=/v1/responses，并断言 plane.Runtime(channel) 的 InUse=0。
  - 新增 TestProductionStatefulFailureScopeIsRequest：建别名 state-pin-404（1 条启用 Responses 路由），假上游第 1 次返回 404 + application/json + {"error":{"message":"no such response"}}（刻意不含 classification.go 认识的协议码），第 2 次起返回 200。发送 previous_response_id 请求，断言客户端 404、假上游恰 1 次、request_logs attempts=1 且 http_status=404；断言 plane.Runtime(channel).State=="ready" 且 ModelCooldowns==0（有状态 404 未打开模型熔断）。随后原样再发一次，断言 200、假上游累计 2 次，证明 404 未把该别名短路。
  - 在同一用例内做无状态对照：建别名 state-free（2 条启用 Responses 路由，channel priority 0/1），priority 0 的假上游固定返回 404 + 同一无协议码 body，priority 1 的假上游返回 200 + {"id":"resp_free_ok","object":"response","status":"completed","output":[]}。发送无状态纯文本请求，断言 200、body 含 resp_free_ok、两个假上游各 1 次、request_logs attempts=2、plane.Runtime(第一渠道).ModelCooldowns==1；这证明普通 404 的跨渠道切换与 model scope 未被有状态改动全局关闭。
  - 本地执行 go test -count=1 -run '^(TestProductionStatefulAdmission|TestProductionStatefulPinnedSingleCandidate|TestProductionStatefulFailureScopeIsRequest)$' -v ./cmd/gateway 与 go test -count=1 ./cmd/gateway，记录命令、退出码与关键输出；确认 -v 输出中三个新用例均为 PASS（防止用例名拼错导致定向检查空跑），并确认同包既有用例仍通过。若断言与实现冲突（例如 background 未在调用上游前 400、多候选未返回 stateful_route_unavailable、单候选状态字段被剥离、有状态 404 仍打开模型熔断、或无状态 404 不再切换），停止并作为阻塞 finding 上报，附命令与输出；不降低断言、不改 internal/ 生产代码、不新建或恢复 e2e 文件。
  - 更新 docs/deploy.md：按 S2-T1 实际实现改写 §3 环境变量表中 GATEWAY_CODEX_CONTEXT_WINDOW 的描述（当前 :78 仍写“Codex 模型 manifest 的上下文窗口”，真值化后应为未声明回退/上限语义）；新增一小节写能力三字段的声明位置、未声明默认（D1 推荐值的当前默认，明确标注待用户确认）、同一别名全部启用同协议候选取共同能力并同时约束 manifest 与图片准入；写有状态边界（background 400 unsupported_parameter、状态字段仅单候选、多候选 400 stateful_route_unavailable、GET/DELETE/cancel/Conversations 404、conversation 只能在网关外创建）并链接 docs/clients/codex-0.164.md；在 §4（:105-135）v2 段落后补 v3：只新增 model_routes 三列、旧行 unknown/NULL、pre-migrate 自动备份、旧二进制拒绝打开 v3、回滚=停服→restore pre-migrate-v2→启动旧二进制、不得手工删 schema_migrations；在 §11（:327-343）追加人工验收项（v3 升级与回滚、能力声明改变 manifest/图片准入、有状态三态与 404 资源面），并保持“自动测试不会执行以下命令”的既有口径。
  - 新建 docs/clients/codex-0.164.md，内容按证据分层：① manifest 两个入口 GET /v1/models?client_version=... 与 GET /backend-api/codex/models，并写明客户端触发证据只支持“自定义 provider 配置 model_catalog_url 时请求前者”（依据 git show 86bc0dd:e2e/testdata/codex-0.164.0/config.toml.tmpl:13-15，该文件已删除，只读历史不恢复），后者的客户端触发条件标注未取证；② 能力字段语义与未声明默认，注明 manifest 窗口是保守值、supported_reasoning_levels 不会为空（至少 [none]）；③ 错误码表（unsupported_parameter、stateful_route_unavailable、model_does_not_support_image、model_not_available、not_found）；④ 有状态字段行为表（background 一律拒绝；previous_response_id/conversation/encrypted_content/item_reference 仅单候选放行并钉住，多候选 400；有状态 404/未知 SSE 失败不冷却渠道）；⑤ GET/DELETE/cancel/Conversations 一律 404、conversation 只能在网关外创建、网关不存储 response；⑥ store=false 的两层留存（网关 5 分钟/50 请求/32MiB 诊断环与管理员保存后 24h；上游省略 store 默认保留≥30 天且只对遵循 OpenAI 语义的上游成立）；⑦ 可复制 curl 示例统一用 $GATEWAY_CLIENT_KEY 占位、不含任何真实 Key；⑧ 证据边界（未运行真实 Codex/ZCode，text-only 下客户端如何处理图片未实测，ZCode 不在本文范围）。
  - 交叉核对文档与代码：把文档中的每个状态码、错误码、默认值、404 路径逐条对照 S3-T1/S3-T2/S2 的最终实现与本次测试的断言；实现与骨架假设不同时以代码为准并写成“实现现状”，不得用文档掩盖差异，差异本身作为 finding 上报。核对完成后记录仍未覆盖的层级：真实 Codex/ZCode 客户端（A10-F1，D4 延期）、/backend-api/codex/models 的客户端触发、有状态流式的 unknown SSE 装配级行为（仅 S3-T2 单测）、D1/D2 未确认前的默认值。
  - 范围守卫：记录 git status --porcelain 与 git diff --stat，确认交付只含 cmd/gateway/http_contract_test.go、docs/deploy.md（已跟踪修改）与 docs/clients/codex-0.164.md（新增未跟踪；另保留既有未跟踪 nul）；git ls-files e2e 仍为 0 行；未恢复 526237e 删除的 e2e 文件；未修改 internal/ 生产代码、go.mod/go.sum、web/package-lock.json、Dockerfile。
- 行为验收：
  - 机器（chk-s3t3-contract、chk-s3t3-assembly）：别名 state-bg 恰有 1 条启用 Responses 路由时，带 background=true 的 POST /v1/responses 与 POST /backend-api/codex/responses 都返回 400、error.type=invalid_request_error、error.code=unsupported_parameter，两个入口的假上游计数均为 0——请求在上游调用前被拒。
  - 机器（chk-s3t3-contract、chk-s3t3-assembly）：别名 state-multi 有 2 条启用 Responses 路由时，带非空 conversation（字符串）、conversation（对象 {"id":...}）、previous_response_id 的 POST /v1/responses 都返回 400、error.type=invalid_request_error、error.code=stateful_route_unavailable，两个假上游计数保持 0；响应体不包含发送的 conv_*/resp_* sentinel 值，错误只含固定文案。
  - 机器（chk-s3t3-contract、chk-s3t3-assembly）：同一 state-multi 别名的无状态纯文本请求返回 200，两个假上游命中数之和恰为 1——多候选别名只对状态字段收窄，没有把整个别名封禁。
  - 机器（chk-s3t3-contract、chk-s3t3-assembly）：单候选别名 state-pin 上，带 previous_response_id 的请求返回 200、假上游恰 1 次，上游收到的 body 中 model 被改写为该路由的 upstream_model、previous_response_id 仍为原值；带 conversation:{"id":...} 的请求返回 200、假上游累计 2 次、conversation.id 原样保留；第一次请求的 request_logs 为 status=success、attempts=1、protocol=openai_responses、endpoint=/v1/responses，渠道 InUse=0。
  - 机器（chk-s3t3-contract、chk-s3t3-assembly）：单候选别名 state-pin-404 的上游第一次返回 404（body 不含已知协议码）时，客户端得到 404，request_logs attempts=1、http_status=404，plane.Runtime 的 State=ready 且 ModelCooldowns=0；随后同一请求再发一次（上游第二次 200）得到 200 且上游累计 2 次——有状态 404 使用 request scope，未打开模型熔断、未短路该别名。
  - 机器（chk-s3t3-contract、chk-s3t3-assembly）：无状态别名 state-free 的两条 Responses 路由中，priority 0 的候选返回 404、priority 1 的候选返回 200 时，客户端得到 200，两个假上游各 1 次，request_logs attempts=2，第一渠道 Runtime.ModelCooldowns=1——普通 404 的跨渠道切换与 model scope 未被全局改成有状态语义。
  - 机器（chk-s3t3-assembly）：同包既有 TestProductionHTTPStreamFallbackAndRuntime、TestProductionHTTPBudgetDelivers504AndReleasesSlot 与 S2-T3 新增 TestProductionCodexManifestCapabilities、TestProductionImageAdmission 全部通过，且本任务没有修改这些用例的断言。
  - 人工（文档复核，无机器 check；仓库无任何 docs 测试，grep *_test.go 无文档断言）：docs/clients/codex-0.164.md 逐条包含两个 manifest 入口与触发证据边界、能力字段与未声明默认（标注当前默认/待 D1 确认）、错误码表、有状态字段行为表、GET/DELETE/cancel/Conversations 404、conversation 只能在网关外创建、store=false 的两层留存、使用 $GATEWAY_CLIENT_KEY 占位的可复制 curl；且不写 ZCode 行为、不把 /backend-api/codex/models 的客户端触发写成已证实、不把 text-only 下客户端丢弃图片写成已实测结论。复核方法：评审者逐条对照 S3-T1/S3-T2 最终代码与 docs 文本，任一断言缺源码依据即退回。
  - 人工（文档复核）：docs/deploy.md 写明 v3 三列与旧行 unknown/NULL、pre-migrate 备份、旧二进制拒绝 v3、回滚=停服→restore pre-migrate-v2→旧二进制、不得手工删 schema_migrations；§3 环境变量表 GATEWAY_CODEX_CONTEXT_WINDOW 描述与 S2 实际语义一致；§11 增加 v3 升级/回滚、能力声明影响 manifest/图片准入、有状态三种结果（background 400、多候选 400、单候选 200）与 GET/DELETE/cancel 404 的人工步骤，且没有声称这些人工项已被自动测试覆盖。复核方法：逐条对照 S2-T1 的聚合/回退实现与 S3-T1/T2 的准入实现。
  - 人工（范围守卫，非行为证据）：git status --porcelain 只显示这三处交付（cmd/gateway/http_contract_test.go、docs/deploy.md 为已跟踪修改，docs/clients/codex-0.164.md 为新增未跟踪；另保留既有未跟踪 nul），git diff --stat 只含两个已跟踪文件；git ls-files e2e 仍为 0 行；未恢复 526237e 删除的 e2e 文件；未修改 internal/ 生产代码、go.mod/go.sum、web/package-lock.json、Dockerfile。
  - 人工（文档复核）：docs/deploy.md 增加“数据保留与恢复”小节：request_logs/attempts 90 天且无 env、backups 不自动轮转需人工保留、VACUUM 需停服、过期 admin_sessions 保留但鉴权拒绝、restore 必须停服且 checkpoint 不是跨进程锁、当前库不可读时先移动 gateway.db/-wal/-shm 再 restore、pre-migrate 备份不自动清理；每条与 A8-F2/F3/F4/F5、A2-F9 的结论一致。
  - 人工（文档复核）：docs/clients/codex-0.164.md 增加附注：请求体是语义透传而非字节级（A3-F8）；manifest 无 ETag/客户端缓存控制，验收固定 client_version 并清缓存（A6-F10）；不把这两点写成已修复。

## 3. 已核实的证据（静态）

| 编号 | 级别 | 状态 | 位置 | 结论 |
| --- | --- | --- | --- | --- |
| A1-F1 | high | 已核对 | internal/gateway/codex_models.go:141-158,215-216；internal/config/config.go:29,201 | context_window/max_context_window 是单一全局值，对每个映射无条件声明，且被当作真实上限 |
| A1-F2 | high | 已核对 | internal/gateway/codex_models.go:221；internal/gateway/router.go:49；internal/gateway/proxy.go:577-593 | input_modalities 恒为 [text,image]，是客户端默认假设而非上游能力，网关又无法转换协议 |
| A1-F3 | medium | 已核对 | internal/gateway/codex_models.go:175-178 | supported_reasoning_levels 恒为 [none]、default 恒为 none，对所有支持 reasoning 的上游都抹掉了推理档位 |
| A1-F4 | medium | 已核对 | internal/gateway/codex_models.go:67,108；internal/gateway/codex_models_test.go:62-72,137-154 | 两个字段在 Codex 0.164.0 的 ModelInfo 中不存在，声明被静默忽略，其中一个还被测试当作必填 |
| A1-F5 | medium | 已核对 | internal/gateway/codex_models.go:9-12,76-79 | 注释把“可空字段必须显式出现”当成客户端契约，实际只有 15 个字段无 serde default |
| A1-F7 | medium | 已核对 | internal/gateway/codex_models_test.go:231-443；git commit 526237e | 没有真实客户端验收，测试只证明 JSON 形状自洽；Codex E2E 已从工作树删除 |
| A1-F8 | low | 未确认 | internal/gateway/models.go:62-65 | client_version 只判断非空，manifest 不随版本裁剪 |
| A1-F9 | low | 已核对 | internal/gateway/codex_models.go:26,209-212 | truncation_policy 的字节上限常量命名为 tokens，语义误导 |
| A2-F1 | high | 已核对 | internal/store/migrations/0001_init.sql:52-62；internal/store/channels.go:209；internal/store/routes.go:69-88 | 能力字段应落在 model_routes 的 (client_model, channel_id, upstream_model) 粒度，而不是 channels |
| A2-F2 | high | 已核对 | internal/adminapi/routes.go:176-203；internal/store/routes.go:299-312 | PATCH 全量更新 + 显式字段合并：能力字段漏拷贝会被启停操作清空 |
| A2-F3 | high | 未确认 | internal/store/channels.go:267-280；internal/store/routes.go:130-155,356-383 | 自动建路由绕过 normalizeModelRouteInput，能力默认值必须落在 insert 或 DB 默认 |
| A2-F4 | high | 已核对 | internal/store/migrate.go:60,232-263,266-296 | v3 必须是新迁移文件；修改 0001/0002 会因 checksum 拒绝启动 |
| A2-F5 | medium | 已核对 | internal/store/migrate.go:240-243；cmd/gateway/main.go:93-99,218-241；internal/store/backup.go:287-298 | 升级到 v3 后旧二进制无法打开数据库，回滚只能 restore pre-migrate 备份 |
| A2-F6 | medium | 已核对 | internal/store/backup.go:19-28,259-270 | 若改用新表存能力，不要扩展 requiredBackupTables，否则旧备份不可恢复 |
| A2-F7 | medium | 已核对 | internal/gateway/models.go:46-48,136-162；internal/gateway/router.go:66-69；internal/store/routes.go:239-263 | 能力数据要到达 Codex manifest，需要新的 store 读取接口与 builder 签名变更 |
| A2-F8 | low | 已核对 | web/src/features/mappings/api.ts:14-23,52-61；web/src/features/channels/ChannelModelsTab.tsx:436-505；web/src/features/channels/types.ts:21-72 | 前端改动面被低估：路由类型在 mappings/api.ts，表格列数与请求类型需同步 |
| A2-F9 | low | 已核对 | internal/store/migrate.go:138-148；internal/store/backup.go:51-72；cmd/gateway/main.go:91-99 | 迁移失败重启会重复生成 pre-migrate 备份且无清理 |
| A3-F1 | high | 已核对 | internal/store/routes.go:196-226; internal/routing/scheduler.go:234-262; internal/store/migrations/0001_init.sql:33-62 | 同一别名多候选没有能力交集，唯一强制的共同条件是协议 |
| A3-F2 | medium | 已核对 | internal/gateway/codex_models.go:16-33,167-231; internal/gateway/models.go:120-162 | Codex manifest 对每个别名统一声明 text+image 与全局上下文窗口，与候选无关 |
| A3-F3 | high | 已核对 | internal/gateway/attempt.go:146-153; internal/gateway/classification.go:93-99; internal/gateway/proxy.go:306-317 | 图片/上下文等能力类 4xx 被归为 request scope，不切换候选也不冷却，状态码 400 |
| A3-F4 | medium | 未确认 | internal/gateway/proxy.go:343-352; internal/gateway/attempt.go:240-248,312-319 | 渠道级本地配置错误在未发出上游请求时就终止整条请求（502，Retry=false） |
| A3-F8 | low | 已核对 | internal/upstream/client.go:289,468-489; internal/gateway/proxy.go:188-210 | body 透传是语义级而非字节级：每次尝试都 decode→替换 model→re-encode |
| A4-F1 | high | 已核对 | internal/gateway/proxy.go:223-318; internal/gateway/stream.go:640-749; internal/routing/scheduler.go:363,463-496 | 有状态 Responses 请求没有渠道钉住：重试和 LRU 都可能换渠道、换上游模型 |
| A4-F2 | high | 已核对 | internal/gateway/router.go:153-170,177-228; internal/gateway/proxy.go:398-438; internal/gateway/errors.go:53-59 | background 被静默透传为成功，但网关没有 GET /v1/responses/{id}，结果不可取、不可取消 |
| A4-F3 | medium | 已核对 | internal/gateway/stream.go:924-940,1057-1066; internal/gateway/classification.go:61-128; internal/gateway/attempt.go:65-79; internal/routing/scheduler.go:673-713 | 流式 response.failed 的未知状态码被当作渠道故障并跨渠道重试，可能误开熔断 |
| A5-F1 | high | 已核对 | internal/gateway/router.go:153-170 | Responses 只暴露 POST：GET/DELETE/cancel 全 404，background 与 store=true 检索端到端不可用 |
| A5-F2 | high | 已核对 | internal/gateway/proxy.go:306-317; internal/gateway/stream.go:744-749 | 带 previous_response_id/conversation/encrypted_content 的请求会被轮询或失败切换到另一渠道 |
| A5-F3 | medium | 已核对 | internal/gateway/router.go:153-170 | conversation 只有字段透传，没有 Conversations API 资源面 |
| A5-F5 | low | 已核对 | internal/gateway/observability.go:269-279; internal/observability/capture.go:26-37 | store=false 不是端到端零留存：网关默认内存捕获 body，省略 store 时上游默认存≥30 天 |
| A6-F1 | medium | 未确认 | codex.exe @280977736（codex-api/src/endpoint/models.rs 邻接） | 客户端在目录获取阶段只校验 slug 唯一且非空，收窄能力值不会被“拒收” |
| A6-F2 | high | 未确认 | codex.exe @283493122 与 core 占位符串 | 把 input_modalities 收窄为 text 会静默丢弃历史图片，且只在 TUI 粘贴处明确拒绝 |
| A6-F3 | medium | 未确认 | codex.exe @278661513（Reasoning effort … is not supported for model …） | supported_reasoning_levels 收窄在 subagent spawn 路径是硬报错，主对话路径缺少校验证据 |
| A6-F4 | medium | 未确认 | codex.exe 内嵌 models.json（11/11 均非空）；reference/sub2api metadata.go:414-416 | 空 supported_reasoning_levels 没有官方先例，属于未验证的形态 |
| A6-F5 | medium | 未确认 | codex.exe ConfigToml/context_manager 串；internal/gateway/codex_models_test.go:180-187 | context_window 收窄只导致提前压缩，0/负值才会破坏阈值计算 |
| A6-F6 | low | 未确认 | codex.exe Responses 请求字段串 | supports_parallel_tool_calls=false 只改变出站请求形状，无拒收路径 |
| A6-F7 | high | 已核对 | internal/gateway/codex_models.go:9-12 与 codex_models_test.go:14-66；codex.exe @273486242 | “缺字段就 serde 失败”的前提与官方数据不符，43 键契约含 0.164 不存在的字段 |
| A6-F8 | high | 未确认 | codex.exe @278548083（core\src\codex_thread.rs 邻接） | 目录不可用时的失败模式是“无模型可用”或静默替换为 provider 默认模型 |
| A6-F9 | medium | 已核对 | internal/store/routes.go:25-30；internal/upstream/models.go:1-10；internal/gateway/models.go:124-134 | 仓库没有逐模型能力数据源，真值化的主要成本在存储与管理端而非客户端兼容 |
| A6-F10 | low | 已核对 | internal/gateway/errors.go:126-137；codex.exe ModelsCacheEntry 串 | manifest 响应没有 ETag/x-models-etag，真值化上线后客户端何时刷新不可控 |
| A8-F2 | low | 已核对 | internal/adminauth/session.go:38-43,200,354-377; internal/store/migrations/0001_init.sql:24-31,127-128 | 过期 admin_sessions 无物理清理；建议记为约定而非本批代码任务 |
| A8-F3 | low | 已核对 | internal/store/backup.go:150-175,179-204; docs/deploy.md:270-292 | restore 的运行中防护只有 checkpoint busy 启发式；"停服恢复"仍是文档约定（已停服场景顺序正确） |
| A8-F4 | low | 已核对 | internal/observability/record.go:34-41; cmd/gateway/main.go:115-119; docs/deploy.md:105-126,253-293 | 日志保留期、备份轮转与磁盘回收未进入部署文档 |
| A8-F5 | low | 已核对 | internal/store/backup.go:138-147,206-228 | 当前库不可读/损坏时 restore 会被 pre-restore 备份挡住，缺少手动 fallback 文档 |
| A8-F6 | low | 未确认 | internal/observability/record.go:368-408; internal/store/db.go:85-88; internal/gateway/proxy.go:35-37,500-505 | retention 清理与请求日志写入共用唯一 SQLite 连接，超时预算相同 |
| A10-F2 | medium | 已核对 | cmd/gateway/http_contract_test.go:110,195；cmd/gateway/dataplane_test.go:59-78；internal/gateway/router.go:154-171 | 生产装配 HTTP 契约测试只覆盖 6 个客户端端点中的 3 个，manifest 只断言状态码 |
| A10-F5 | medium | 已核对 | internal/upstream/client.go:468-489；internal/gateway/proxy.go:398,579-595；internal/gateway/router.go:154-171；internal/store/migrations/0001_init.sql:64-125 | 有状态/后台 Responses 字段原样透传，但网关没有对应生命周期端点，background 请求会得到无法轮询的 202 |
| A10-F6 | low | 已核对 | internal/gateway/codex_models.go:167-232；docs/reviews/2026-11-01-gateway-comparison.md:164 | Codex manifest 对所有模型声明图片/推理摘要/统一上下文，缺少逐模型能力依据（已知延期项） |

> “已核对”指独立核对者按同一证据重新确认了静态行为，不等于运行时复现。

## 4. 需要你决定的事项

### D1

未声明 input_modalities 的路由，Codex manifest 与图片准入应默认按 text+image（保持现状）还是 text（保守）？

- 推荐：默认 text+image；任一候选显式声明 text 时别名收窄为 text 并在入口拒绝图片；未声明在管理端醒目标注。理由：需求把图片列为第一版能力（docs/requirements/api-gateway.md:23,36），默认 text 会让现有图片能力回归，且 Codex 对 text-only 的图片处理可能是静默丢弃（A6-F2，未实测）；同时承认未声明时声明可能夸大。
- 备选：默认 text（更保守，但会让未声明别名全部失去图片，可能造成静默丢图）；强制声明后才在 manifest 暴露该别名（会隐藏现有模型，运维成本高）；按渠道探测图片能力（需真实上游/付费测试，不稳定）
- 风险：未声明路由仍可能把图片发到仅文本上游并得到 4xx；Codex 在 text-only 下是否隐藏/丢弃图片未实测；管理员误声明会同时改变 manifest 与准入
- 影响实施：是

### D2

previous_response_id、conversation、background 等有状态 Responses 请求应采用哪种边界？

- 推荐：background=true 一律 400 unsupported_parameter；previous_response_id、conversation、encrypted_content、item_reference 视为有状态，仅当该别名恰有 1 条启用同协议路由时放行并钉住该渠道、maxAttempts=1；多候选 400 stateful_route_unavailable；GET/DELETE/cancel/Conversations 仍 404 并在文档写明 conversation id 只能网关外创建；不建 response_owners。
- 备选：全部拒绝（含 previous_response_id，最保守但可能破坏 Codex 多轮）；保持透传并只写文档（现状，间歇失效/跨提供方发送）；实现 response_owners + GET/DELETE/cancel/Conversations（完整但本批过大）；按 last_used_at 做亲和（不可靠）
- 风险：Codex 0.164 是否发送 previous_response_id/conversation/background 未实测；若发送且别名多候选会 400；单候选钉住无法处理历史 response 创建于其它渠道；conversation 资源面缺失，客户端若经网关创建会话会 404；OpenAI 官方 stateful 语义本轮 403，Azure 文档不能等同 OpenAI 保证
- 影响实施：是

### D3

能力真值是否只由管理员逐路由声明，以及首批是否包含 reasoning levels？

- 推荐：只做管理员逐路由声明，不做自动探测/上游目录；首批含 input_modalities、context_window、reasoning_levels 三项；reasoning 允许 none/low/medium/high/xhigh/max/ultra（来自 0.164 官方 models.json 证据），未声明或交集为空回退 [none]，default 取 none 或最小档。
- 备选：只做 window+modalities，reasoning 继续 [none]（范围更小，但能力仍不一致）；自动探测/上游模型目录（无可靠来源、可能付费/不稳定）；独立 capability 表或 (channel, upstream_model) 绑定（更规范但本批不必要）
- 风险：手工声明漂移，manifest 仍可能与上游不符；reasoning 档位/子代理行为未实测；声明错误同时影响 manifest 与图片准入；单管理员录入成本
- 影响实施：是

### D4

本批是否恢复真实 Codex/ZCode 客户端 E2E？

- 推荐：不恢复；单独立项。e2e 已由 526237e 删除，当前无入口；新增真实客户端测试需新文件、环境变量与用户授权，不能悄悄恢复旧 fixture。本批用 hermetic 生产装配契约覆盖 manifest/边界，真实客户端证据留待独立项目。
- 备选：本批新增 cmd/gateway/client_contract_test.go（AIROUTER_CODEX_BIN 显式开启）；恢复 526237e 的 e2e 文件（禁止/不可取）；引用 2026-10-31 的 46.077s 历史通过记录作为当前证据（不成立）
- 风险：当前 HEAD 的客户端解析、SSE、工具/图片形状无真实证据；manifest 收窄/放宽的客户端行为未知；发布前只能人工或后续项目验证
- 影响实施：否

## 5. 本批不做

- 真实 Codex/ZCode E2E：A10-F1；e2e 0 文件、526237e 删除、需新入口与授权；按 D4 单独立项。
- attempt 级失败/重试统计与 usage 覆盖率展示：A7-F3/F1/F2；数据已在 request_attempts，可后续做只读聚合，本批不做。
- 孤儿 attempts 与 admin_sessions 物理清理：A7-F5/A8-F1/F2；量级小、属独立小改动。
- 配置变更历史/审计与 updated_at 语义：A9-F1/F2/F3/F4/F5；5 层改动，独立决策。
- response_owners、GET/DELETE/cancel、Conversations API、background 执行：A5-F1/F3；本批只做显式边界，不建响应存储。
- 自动能力探测/上游目录/独立 capability 表/(channel, upstream_model) 绑定：A6-F9/A2-F1；先用手工稀疏声明，出现漂移再评估。
- 能力类 4xx 自动重试：A3-F3；上游错误码未取证，自动重试可能放大会话/计费；本批用声明+准入降低风险，不自动重试。
- 预检拒绝日志的 model/精确 code、413 envelope、preflight protocol：A3-F5/F6、A7-F4；诊断残余，需 router 级 seam，独立处理。
- context_window 双默认合并（A1-F6）与 client_version 裁剪（A1-F8，unconfirmed）：不影响本批行为，保留现状。
- channel 级 store/background 能力位与幂等键：A5-F4/F6；background 已拒绝，stateful maxAttempts=1 已消除主要重复创建路径。
- last_used_at 同步写优化、同步故障反馈、restore --force/空备份守卫、pre-migrate 备份轮转：A4-F4/A3-F7/A8-F5/F7/A2-F9；需测量或独立设计。
- CI/workflow、版本注入、Docker build/run、race、浏览器、真实上游：A10-F3/F4；本轮无授权/无 docker，未执行。
- ZCode adapter：R-009 已批准延后到 MVP+1（docs/plans/api-gateway-plan.md:920），不报为违约。
- 多租户/计费/Redis/微服务/跨协议转换：需求明确不做。
- store=false 捕获默认变更：A5-F5；本批只在文档说明，不改捕获行为。
- 延期（static-confirmed，本批不实施）：A1-F6 context_window 双默认合并（config/gateway 两处）；后续与配置清理一起做。
- 延期：A3-F5 预检拒绝日志 model/精确 code、A3-F6 413 envelope、A3-F7 同步故障反馈占用槽位；诊断残余，需 router 级 seam 或测量。
- 延期：A4-F4 每次 Acquire 同步写 last_used_at；需基准测试后再优化。
- 延期：A5-F4 channel store/background 能力位、A5-F6 幂等键；background 已拒绝，stateful maxAttempts=1 已消除主要重复创建路径。
- 延期：A7-F1..F9 attempt 聚合、usage 覆盖率、预检日志、孤儿 attempts 展示、写延迟/内存/重命名聚合；数据在 request_attempts，后续只读聚合或独立小改。
- 延期：A8-F1 孤儿 attempts 清理、A8-F7 backup 空库守卫；独立小改动。
- 延期：A9-F1..F5 updated_at 语义、检查证据与映射比对、配置历史、SetEnabled 死代码、乐观锁；独立决策。
- 延期：A10-F1 真实 Codex/ZCode E2E、A10-F3 CI/门禁、A10-F4 版本注入；单独立项或另行授权。

## 6. 验证边界与缺口

- 本轮没有运行任何项目测试、构建、race、压测或服务；全部结论为静态阅读。
- 未访问真实上游与真实客户端；Codex 0.164 的客户端行为依据官方二进制字符串与历史记录，非本轮实测。
- 公开协议资料部分页面返回 403/超时，未取得的正文没有被当作结论依据。
- 未运行任何测试、构建、race、压测、服务或真实客户端：本轮为只读分析（输入 JSON 的 allowShell=false、约束禁止运行项目测试与启动服务），所有 finding 均为静态未复现。
- 未取得 Codex 自定义 provider 的 models endpoint client 源码，因此“/v1/models 是否携带 client_version 查询串、是否会被调用”未在 0.164.0 侧证实；该证据原由已删除的 e2e 提供（commit 526237e）。
- 未验证 Codex 在 input_modalities 为 text-only 时是否隐藏/剥离图片入口；这决定 F-02 的收窄方案会不会破坏需求的图片能力（E22）。
- 未验证 reasoning.effort=none 是否被各类上游拒绝；F-03 的“可能 400”是推断，不是观测。
- 未核实需求所列第三方上游（GLM/Qwen/DeepSeek/Claude/Gemini 经中转站）的真实上下文窗口与图片支持；只有官方 Codex 目录（11 个模型）有确切数字。
- 未确认 models.json 中的 supports_parallel_tool_calls / minimal_client_version 是否由另一个加载器读取；只确认它们不在 ModelInfo 结构内，因此 manifest 响应路径会丢弃。
- 未核对 Codex 0.164.0 之前版本的结构差异；若用户实际安装版本不是 0.164.x，F-05 的字段必需集需重新核对。
- 未读取 ~/.codex 或任何账户配置、密钥、数据库；本结论不包含用户环境中的实际 Codex 版本证据。
- 按任务约束未运行任何测试、构建、迁移或服务；迁移、备份恢复与 API 行为均为静态阅读结论，未在本仓库实际执行 0003。
- 未在 SQLite 3.51.3 上实测“ADD COLUMN + 单列 CHECK + 事务内失败回滚”，仅依据官方文档（E23）与现有 0002 写法推断。
- 未测量能力字段对 /admin/api/model-routes 与渠道 Tab 响应体积/渲染的影响；性能判断基于个人规模与 200 模型上限。
- 未读取真实数据库、数据目录、gateway.env 或任何凭据（任务禁止），因此未核对现存 model_routes 行数与历史上是否存在手工异常数据。
- manifest 字段真值、收窄策略与客户端兼容性属于 shard 0/5，本分片只在“数据如何到达 builder”的接口边界引用，未判断具体字段该取什么值。
- 调度准入与同别名共同能力的运行时规则属于 shard 2，本分片只说明聚合键需带 protocol，未设计拒绝路径与状态码。
- 未逐节通读 docs/plans/api-gateway-plan.md，仅用关键词确认其中没有逐模型能力任务；若计划另有编号约定，需在合并方案时对齐。
- 未运行任何测试、构建、go vet、race、压测或服务启动（输入约束 allowShell=false 且明确禁止），因此所有结论均为静态代码阅读，不含运行时证据
- e2e/ 目录为空（git 未跟踪任何文件），不存在客户端契约或 Codex CLI E2E 可作为能力/图片路径的证据
- 未访问真实上游，未验证真实中转站对“不支持图片/超上下文”返回的状态码与错误码形状；F3 的重试白名单是否覆盖真实错误码仍属未验证事实
- 未验证 SQLite julianday 对 RFC3339Nano（9 位小数、尾零截断）的解析；即使解析失败，最终顺序仍由 Go sortCandidates 用 time.Time 重排，仅影响 SQL 排序有效性
- 未测量 circuitBreaker/Limiter 单锁争用与单写连接串行化的实际延迟；F7 的量级评估未用基准测试支撑
- 未阅读 adminapi 的 model_routes 校验与 web 表单细节，仅确认无能力相关字段与迁移
- 未核对 8080/8095/8096/8097 上运行中的服务状态（约束禁止重启或探测）
- 未评估“能力元数据”引入后的管理端 UI、迁移回滚与配置变更审计成本，这些属于后续方案设计范围
- 官方 Responses 文档与 openai-python 类型未取得正文：WebFetch 对 developers.openai.com 连接超时、platform.openai.com 返回 403、raw.githubusercontent.com 连接超时；docs/reviews/2026-11-01-gateway-comparison.md:14,342-345 也记录 403/ECONNRESET。因此 background、previous_response_id、GET /v1/responses/{id} 的官方语义未在本轮独立核实。
- 未运行任何测试、构建、race、压测、服务或部署；所有 findings 都是静态候选，没有运行时复现或性能数据。
- Codex 0.164.0 是否实际发送 background、GET /v1/responses/{id} 或 previous_response_id 未核实；e2e/ 已由 526237e 清空，按约束未恢复旧 fixture，也没有新的真实客户端抓包。
- 上游对未知 previous_response_id 的具体状态码/错误码未核实；F1/F3 以“若上游返回 400/404 或 response.failed”表述，不能当作所有 provider 的固定行为。
- conversation 的创建、归属与跨渠道可见性未核实；网关没有 /v1/conversations 端点，conversation ID 是否由客户端在别处创建也不确定。
- input 内 item_reference、encrypted reasoning 等引用型字段是否同样绑定上游渠道未纳入本次代码策略；需要协议证据才能判断是否与 previous_response_id 同属一个边界。
- GET /v1/responses/{id} 的 404 是从 router 的 switch/default 与现有尾斜杠测试推断；没有针对非空 response id 的运行测试。
- 未读取 gateway.env、实际数据目录、数据库、账户配置或上游 Key；未验证生产配置下的实际 MaxAttempts、冷却窗口和并发值。
- reference/ 只做了 sub2api 两个文件的定向核对，未重新审查其他参考项目；本报告的选型对照以既有 docs/reviews 记录和这两处源码为依据。
- OpenAI 官方 conversation-state、background、API reference 页面本轮 WebFetch 与 curl 均 403；OpenAI 自身对 store=false+previous_response_id 的明确禁止语句、background 是否要求 store=true、store=false 响应 GET 的具体错误体未取得。Azure 文档的对应表述不能等同 OpenAI 保证。
- GET /v1/responses/{id} 对 store=false 响应只知 spec 的通用 404 'The response was not found.'；是否对所有未存储/已过期 response 都返回 404、错误 code 是什么，未取得正文。
- POST /v1/responses 携带失效 previous_response_id 的状态码未知；若为 404，现有 classifyUpstreamStatus 会把它标为可重试并跨渠道继续。
- Codex 0.164 实际是否发送 previous_response_id/conversation/background/reasoning.encrypted_content 由另一分片核实；本分片不结论，也不据此判断现有客户端是否已被影响。
- 第三方中转/聚合 openai_responses 渠道对 store/background/conversation 的行为无官方资料，未测试；渠道能力差异只用 Azure 作为已核实的边界样本。
- 未运行任何项目测试、构建、race、服务、fake upstream 或真实上游（按约束）；所有 findings 均为静态候选，不代表运行时已复现。
- OpenAPI 文件下载在 4,181,184/4,269,666 字节处超时截断；Responses/Conversations 相关段落完整，尾部 beta/websocket 未读完，不据此下结论。
- Azure Learn 页面为 2026-08-18 快照（gitcommit 50853cc，私有 azure-ai-docs-pr 仓库）；OpenAI news 页面仅取得 poll/stream 措辞，未给 store 要求或时限。
- 外网不可用：WebFetch 访问 raw.githubusercontent.com 连接超时（185.199.108.133:443），WebSearch 返回 provider 不支持；因此未能直接引用 openai/codex 的 Rust 源码，客户端侧证据来自本机 Apache-2.0 二进制字符串与内嵌目录数据，属“官方产物”而非“官方源码”。
- 未做运行时验证：受约束限制未启动任何服务/假上游，也未对真实网关发请求；codex debug models 只支持 --bundled（内置目录）或联网刷新，没有离线加载自定义目录的开关，故“窄 manifest 下客户端是否拒收”未被实测。
- 无法判定逐字段 required/optional：codex.exe 为 release 构建且无 DWARF，serde 的 Option/default 属性不可见；只能依据“官方目录自身缺键”与历史 E2E 记录推断。
- codex exec --image 路径是否按 input_modalities 拦截未证实；只确认了 TUI 粘贴、历史回放占位符、view_image 三处。
- 历史真实 E2E 通过（46.077s，codex-cli 0.164.0）只是文档记录，本次未复跑；当前 e2e/ 为空目录，不能作为现时契约证据。
- x-models-etag 的具体协商语义与客户端缓存 TTL 默认值未确认；仅确认两端存在相关字段。
- 按约束未读取 gateway.env、实际数据目录/数据库、账户配置或上游 Key，因此渠道与真实模型分布未知，报告不涉及具体部署现状。
- 未运行任何 Go 测试、构建、race、前端类型检查或组件测试（输入约束明确禁止），因此所有结论都是静态阅读结果，没有任何运行时复现。
- 未取得 OpenAI 官方文档正文：platform.openai.com 返回 403，raw.githubusercontent.com 拉取 openai-openapi 25s 超时，WebSearch 在本 provider 不可用。F1 里“上游在未 opt-in 时不回传 usage”这一外部前提未被证实，只作为条件性结论。
- 未读取实际数据库、日志或部署配置（约束禁止），因此给不出真实 usage 覆盖率、attempts 行数、孤儿行数量或任何延迟数字。
- 未评估 Codex manifest、逐模型能力、有状态 Responses、路由/熔断与探针（不在本范围），也不对其现状作任何判断。
- 未逐条核对 web 组件测试对 usage/attempt 展示的断言细节，只确认 DashboardPage.test.tsx 的 fixture 含 usage_count 字段。
- attempt 聚合的口径（是否把 cancelled、preflight、调度失败计入）在仓库内没有既有定义，需要方案阶段明确，本轮只给出“分开标注”的建议。
- 本轮按约束未运行任何测试、构建、race、压测或服务；所有结论为源码静态阅读+官方文档，不含运行时复现
- F6 的清理耗时与日志写超时概率未测量；没有真实 attempts/session 行数或数据库体积数据（约束禁止读取实际数据目录）
- F3 的"运行中 restore 静默丢写"未在 Linux/Windows 实机验证，依据是代码顺序、POSIX 文件语义与 SQLite 官方 busy 语义
- F5 未构造损坏数据库验证 restore 中止路径；F7 未实际执行 `gateway backup` 生成空备份
- adminapi/capture.go:57 的捕获清理 goroutine 使用 context.Background()，与 Store.Close 无同步；不在本切片 5 文件内，仅记录未展开
- 未决定产品上是否保留 revoked session 作为审计历史，这会影响 F2 若将来实施的删除条件
- 未检查 web/ 前端轮询频率对 touchSession 写放大的影响（超出本切片）
- 未评估备份文件轮转/离线保留的具体份数与加密要求，需用户决定
- 未运行任何测试、构建、race、压测或服务启动（按本轮约束），所有结论均为静态核对；未声称运行时复现。
- 未读取实际数据目录/数据库（按约束），因此不知道现存渠道数量、历史写入规模与 updated_at 的真实分布。
- 未在新版浏览器中验证渠道页渲染，channel.updated_at 未被使用这一判断来自 web/src 的 grep，而非运行时 DOM 观察。
- 触发器方案的实现细节只核对了 sqlite.org 该页：事务边界、递归触发行为、以及对 0002 已存在列的 UPDATE OF 兼容性均未实测；若要采用需单独做 spiek/实测。
- 未评估在 request_logs/attempts 之外是否还有可复用的观测查询层（仅看 schema 与写入列），「事后重建」的完整度未被量化。
- 未核对远端分支状态与更早的 Git 历史（本轮仅确认 HEAD=a202725 与工作树 clean）；历史计划中的评审意见只按 references 定位，未逐条重读全文。
- 未验证「4090/管理端并发双标签页覆盖」的真实行为（需运行服务与两个客户端），F5 仅为请求构造 + 服务端合并顺序的静态推断。
- 未评价变更历史是否属于产品必须项（需用户决定）；本结论只给出「按现有仓库状态延期」的建议与成本边界。
- 未运行任何测试、构建、race、npm、docker 或服务（按输入约束）；文中所有“通过”均为历史文档记录，不是本轮结果。
- 本机无 docker（command not found），Dockerfile 构建与 §11 容器项无法验证。
- 未联网、未访问 GitHub/Actions，无法确认远端可见性、推送能力或 CI 配额；未使用 gh/API。
- 未读取 gateway.env、data 目录、数据库或任何凭据；未验证 8080/8095-8097 运行状态。
- 未验证 Codex 子进程是否外联、是否发送 background/previous_response_id/conversation；F5 的影响是条件性推断。
- e2e 删除原因只有 commit subject “stale”，无 issue/说明；未独立判断旧 harness 的具体失效点。
- 未覆盖逐模型能力、同别名多候选共同能力、attempt/usage 完整性、配置变更追踪（超出本轮指定范围；comparison:275 已延期）。
- 未复现 go test -race 的 CGO 限制；仅引用 review:48。
- 仓库无顶层 LICENSE（仅 LICENSES.md 第三方清单，EV22），未评估发布授权影响。
- 未验证任何新入口实现（尚未写代码）；本轮未修改文件。
- 以下 static-confirmed findings 本批不实施，已从 findings 移出并在 notDoing 保留 ID/结论：A1-F6、A3-F5/F6/F7、A4-F4、A5-F4/F6、A7-F1..F9、A8-F1/F7、A9-F1..F5、A10-F1/F3/F4；这是范围决定，不是证据缺失。

## 7. 恢复说明

本方案由 `task-plan` 工作流生成，但工作流在发布阶段反复被“不可变材料”校验拒绝（草稿哈希与文件名字节一致、`preflight` 已通过，仍无法完成发布）。
因此本文是从已通过确定性校验的草稿渲染的可读版本，**没有生成 v3 contract**；若要按任务施工，需要重新走一次 `task-plan` 或按本文任务手工执行。

原始材料：
- 输入记录：`.zcode/task-plans/0bb444d022efdf175b4417803910ff3d0dd463b8628970620c5a72a31b45e714/input.json`
- 检查点：`.zcode/task-plans/0bb444d022efdf175b4417803910ff3d0dd463b8628970620c5a72a31b45e714/checkpoint.json`
- 方案草稿：`.zcode/task-plans/0bb444d022efdf175b4417803910ff3d0dd463b8628970620c5a72a31b45e714/drafts/d-8168da614f9f689c9a990c99ee726dfa75f9d3dd61777ea26794e424f1f65617.json`

## 8. 决定记录（2026-11-01，用户授权代为决定）

| 编号 | 决定 | 说明 |
| --- | --- | --- |
| D1 | 未声明模态默认 text+image | 任一候选显式声明 text 时别名收窄为 text 并在入口拒绝图片；未声明在管理端醒目标注 |
| D2 | 有状态请求单候选才放行 | background=true 一律 400 unsupported_parameter；previous_response_id/conversation/encrypted_content/item_reference 仅单候选别名放行并钉住渠道、maxAttempts=1；多候选 400 stateful_route_unavailable；GET/DELETE/cancel/Conversations 仍 404 并写入文档 |
| D3 | 仅管理员逐路由声明 | 不做自动探测/上游目录；首批含 input_modalities、context_window、reasoning_levels 三项；档位 none/low/medium/high/xhigh/max/ultra，未声明或交集为空回退 [none] |
| D4 | 不恢复真实客户端 E2E | 本批用生产装配契约测试覆盖 manifest 与边界；真实 Codex/ZCode 验证单独立项 |

这些决定消除了 D1–D4 的阻塞，但方案仍不是 v3 合同包：实施需要按本文任务手工执行，或重新运行 task-plan 生成可交给 task-run 的合同。
