# LLM API Gateway 对比与个人网关取舍

**建议保留当前 Go＋React＋SQLite 个人网关，优先补齐可靠性、状态一致性、恢复与验证边界；将 Bifrost 作为最贴近现有形态的成熟替代候选，而不是立即迁移。** 当前需求尚不要求复制企业控制面、分布式治理或语义缓存。若多提供商兼容、协议变化和团队治理的维护成本持续上升，再依据实际需求验证 Bifrost、LiteLLM Proxy、Portkey 或 Cloudflare AI Gateway，避免仅因功能表更长而更换系统。

- **研究基准日：2026-11-01。** 日期是本次研究记录日期，不代表全部功能的发布日期或某个已锁定版本。
- **证据性质：四个本地参考项目源码＋四种主流方案官方正文。** 未运行外部产品，不把静态分析或官方说明当作性能、可用性、故障恢复实测；不采用宣传数字排名。
- **本文用途：** 对比外部方案、记录实施前发现及已批准方向。实施与测试结果单独见 [可靠性验收记录](2026-11-01-reliability-verification.md)，不要把研究中的全部建议视为本批已完成。
- **研究边界：** 参考项目只读，外部只查公开资料；不向外部上传项目材料或密钥，不创建账户、付费测试或部署。自己的工作区按另行批准范围实施，未提交或推送。

## 1. 证据范围与阅读规则

本次 WebSearch 因工具不支持对应搜索接口而失败，没有使用搜索摘要作为核实依据。四方案主要依据 WebFetch 成功取得的官方站点正文及官方 GitHub 页面。失败的请求未通过绕过访问限制的方式重试。

OpenAI 的 conversation-state、prompt-caching、streaming-responses 官方页面返回 403，openai-python 响应类型原文返回 ECONNRESET。因此，本文不将这些未取得正文的 OpenAI 机制写成已独立核实的事实；尤其不由某个客户端字段推导会话自动恢复、流式续接或服务端缓存保证。

这些网页大多跟随当前文档更新，**本次没有把产品版本、文档 commit 与运行镜像锁在一起**。出现内容不完整或页面间口径差异时，本文保留未知项，不拼接出不存在的保证。例如：

- Bifrost 的 provider 配置页与 retries 专页给出的默认退避数字不同，不将其中任何一组数字作为稳定默认值。
- Bifrost 治理配置页使用 rolling window 用词，而预算/限流专页主要描述独立 reset 时间，没有足够依据还原精确限流算法。
- Portkey 功能比较表的部分标记在抓取正文中为空，空白不能证明某项能力包含或不包含。
- LiteLLM 的全局超时说明与逐次尝试配置可以同时读到，但未充分解释所有协议路径下重试、fallback 的累计 deadline 关系。

下文的“官方说明”只表示取得了相关文字；“建议”是针对个人网关的工程取舍；“未知”不表示产品一定没有该能力。

## 2. 总览：先比较边界，而不是功能数量

| 方案 | 官方定位及适用重心 | 自托管与额外依赖 | 对个人网关的意义 |
| --- | --- | --- | --- |
| LiteLLM Proxy | 多提供商兼容、密钥/团队/预算治理、路由与观测 | 可自托管；完整管理持久化使用 PostgreSQL，多实例协调使用 Redis；还有按功能触发的外联 | 当兼容与治理成为主要成本时值得评估；不是最轻的 SQLite 替代 |
| Bifrost（Maxim） | 统一 AI Gateway，提供 Go 集成、本地管理、路由与治理 | OSS 默认 SQLite，可配置 PostgreSQL 或 JSON；企业版不支持 SQLite，集群能力有额外边界 | 单节点、Go、本地存储最贴近当前方向，适合作为优先验证的替代候选 |
| Portkey | OSS 转发内核＋托管/企业 AI 管理平台 | OSS 可本地运行；企业混合部署保留 Portkey 控制面，涉及 Redis、日志后端和联网同步 | 要区分“替换转发层”与“引入完整平台”，不能把二者当同一个部署成本 |
| Cloudflare AI Gateway | 托管入口、路由、日志、缓存与流量控制 | 请求经过 Cloudflare；已取得正文没有提供 Gateway 自托管部署路径 | 接受第三方数据路径、希望减少运维时有价值，不是本地单文件网关的等价替代 |

上述概括对应各方案的部署、架构及功能正文，详细依据见后文和来源清单。没有针对同一负载运行任何基准，故不比较吞吐、附加延迟或稳定性名次。

## 3. 四个主流方案逐项对比

### 3.1 LiteLLM Proxy

#### 定位、部署和数据边界

官方部署页把 PostgreSQL 用于密钥、用户、团队、配置和支出持久化。特定部署可以省去数据库、只用 master key，但相应管理持久化能力不可用。Redis 对协调多实例的定位很明确：**“Required once you run more than one instance”**。缺少共享状态时，各进程独立跟踪额度和冷却，不应将这种部署称为严格统一限额。[L1]

自托管并不等于不外联。`Outbound Requests and Offline Mode` 列出价格目录、版本信息、分词器和部分页面资源等外联。离线模式主要阻止列明的内置下载/检查，不阻止配置的推理提供商、回调、guardrail、SSO 等；在无法本地验证许可证签名时，也可能联系许可证服务。因此“offline mode”不是网络出口防火墙。[L2]

使用量遥测和上述下载、推理请求、日志回调是不同通道。遥测页称 **“Every group is off unless you turn it on”**，还要选择目的地；没有默认接收端。它可以写本地表，也可以发到显式配置的 HTTP 地址。关闭遥测不能替代关闭 payload 日志或审计所有外联。[L3]

#### fallback、deadline、cooldown 和限流

可靠性页描述按顺序尝试备用 model group，区分普通错误、上下文窗口和内容策略错误，并支持重试与 deployment cooldown。示例把 `allowed_fails` 与 `cooldown_time` 组合，超过失败阈值后暂时排除 deployment；精确窗口算法未说明，不宜照抄示例数字作为系统契约。[L4]

超时页把全局超时描述为覆盖 **“entire length of the call”**，另有 per-attempt 配置优先级。不同流式路径涉及首 chunk 或等待后续 chunk 的时间限制；本次正文不足以证明所有入口、重试和 fallback 都严格共享同一累计 deadline。可靠性页明确讨论首次 chunk 之前的流式 fallback，但没有完整证明已输出内容后的无缝切换能力。[L4][L5]

限流文档比简单 RPM 开关更细：可按 key、user、team、model 限制 RPM/TPM/并发；token 先预留，结束后按 usage 核销。估得太小可能过量接纳，估得太大可能误拒绝。Redis 故障默认退回各进程内存计数，多实例总额度可能扩大；fail-closed 可在无法验证额度时返回 **503**，应与真正超额的 **429** 区分。模型级限流还可能触发 fallback，需要明确调用者额度与上游容量是否允许跨模型转移。[L6]

#### 缓存、日志和商业边界

Cache-Aware Routing 的 session affinity 使用 rendezvous hashing，作用在已选 deployment 的后端 pod 内；不能泛化为任意云 provider/API key 的粘性。多上层代理共享 cluster-affinity pin 时需要共享 Redis。它帮助后端复用 prompt prefix，不是存储答案。[L7]

日志可用 `turn_off_message_logging` 去除消息/响应正文并保留指标，外部回调另有控制。日志保留、用户标识脱敏、raw provider payload 等必须逐项配置，不能把“默认遥测关闭”当作“无敏感日志”。[L8]

成功取得的 LICENSE 正文是 MIT，但明确排除 `enterprise/`，该目录另有许可。企业页面列出 RBAC、审计保留策略、多区域和其他付费功能；不能把整个仓库和所有功能一概称为 MIT 免费能力。本次未核实 enterprise/LICENSE 的完整条款。[L9][L10]

**取舍：** 值得借鉴错误分类、额度预留/核销、显式 fail-open/fail-closed；个人阶段不复制组织层级、复杂回调矩阵和多实例治理设施。

### 3.2 Bifrost（Maxim）

#### 为什么最接近当前形态

设置页和 OSS 迁移页确认默认 SQLite，可使用 PostgreSQL 或 JSON 配置；本地日志使用 SQLite。企业迁移页明确：**“Enterprise does not support SQLite as a config or log store.”** 企业配置和日志要求 PostgreSQL 16+。因此，OSS 单节点起步不应被描述为必须先安装 PostgreSQL/Redis，但企业版也不能沿用“纯 SQLite”的部署想象。[B1][B2]

更关键的是，预算/限流页直接说明：**“Running multiple OSS Bifrost nodes with a Postgres backend is not supported.”** 运行中的配置和用量状态驻留内存，数据库并非天然的实时节点协调器；企业集群另有节点同步机制。共享 PostgreSQL 不等于共享一致性，SQLite 更不等于可直接横向扩容。[B3]

#### 重试、冷却和流式边界

官方 retries 专页描述：先主目标，再顺序尝试备用目标，每个 provider 有自己的重试机会。瞬时网络/服务错误通常在当前 key 重试；限流可轮换 key；永久凭据、权限或账单类错误可直接排除 key。provider timeout、首 token timeout 可以进入 fallback，但整体 deadline 过期不能继续尝试。[B4]

不要把请求内轮换等同于跨请求熔断。正文没有给普通 retry 机制一个通用、持久的跨请求 cooldown 保证；利用 provider retry hint 暂时排除 key 的部分明确关联企业 Adaptive Routing。[B4]

Azure 流式启动元数据可以被缓冲，令 HTTP 200 内嵌的启动错误在输出前进入 retry/fallback。内容输出、终止结果、不识别的事件或缓冲限制会结束该保护，之后仍是流式错误。这是特定启动阶段的保护，不是“任意半截输出都可自动续接”。[B4]

限流可涉及 provider 配置、virtual key、team、customer，并区分请求计数与输入＋输出 token。正文没有充分解释所有失败尝试的计数、原子性、token 预留和取消结算。预算示例允许一次请求把支出推过阈值、随后阻止下一次，不能宣传为严格零超支。[B3][B5]

#### 会话亲和、日志隐私和许可

Request Options 正文明确 `x-bf-session-id` 用于 provider/key stickiness，需要 KV store，且按 virtual key/user 归属隔离。路由先决定合格候选，亲和再偏向上次成功目标；显式 provider-qualified model 不会因此任意改道。成功 fallback 通常成为后续绑定；绑定 TTL 是保留时间，不是请求 deadline。这里的 KV store 要求也不能未经部署文档核对就等同于必须外接 Redis。[B6]

Prompt Caching 页面区分上游 prefix 复用与网关响应复用：前者仍调用上游，后者可直接返回已有答案。亲和只改善命中机会，不保证缓存命中。[B7]

内置观测默认记录 inputs/outputs，可通过 `disable_content_logging` 去掉本地正文而保留指标。Client Configuration 特别提示，该设置不控制独立观测连接器；BigQuery、Kafka、Pub/Sub、OTel、Datadog 等要各自配置。正文、工具参数、捕获的 headers、对象存储留存和请求级例外还需分别审计，不能用一个布尔值推导所有外发都安全。[B8][B9]

官方 README 声明 Apache 2.0，企业页面列出 Adaptive Routing、Clustering、OIDC、RBAC、Audit Logs 等。但 LICENSE 原文和 raw 请求失败，本文只记录 README 声明，不声称已审完所有许可及企业例外。取得的 security 页面也不足以证明任意部署默认没有遥测或厂商外联。[B10][B11][B13]

**取舍：** 可优先验证它是否能替代个人网关的 provider 适配和管理负担；值得借鉴单节点存储、provider/key 亲和和 attempt trail，不必引入企业集群或照搬其全部插件体系。

### 3.3 Portkey

#### OSS core 不等于完整平台

公开部署文档支持 npm、Bun、Node 构建运行和 Docker，并描述 OSS gateway 的 fallback、retry、load balancing。单独本地运行内核，与完整的密钥治理、持久日志、商业控制台不是一回事。公开安装页没有完整外联清单，因此不能仅凭一个启动命令保证无 Portkey 网络依赖。[P1][P2]

企业混合架构更清楚：Gateway、配置缓存及请求/响应存储可以在客户环境；管理 UI、配置管理和 analytics 控制面由 Portkey 托管。需要配置同步与遥测外联，架构包含 Redis 和选定的日志后端，不能按 SQLite 单文件系统估算成本。[P3]

模型名、token、latency 等元数据会发往控制面。本地存储日志也可能由 Portkey 按需读取用于展示，另有直接把加密日志放到 Portkey 的选择。因此，架构文中的“内容留在网络内”宣传不能覆盖调用外部模型、控制台读取或所选托管日志路径。功能比较页正文还称 fully isolated hosting 不再面向新部署提供；这属于所取得页面的商业状态，不是所有历史版本的永久结论。[P3][P4]

#### 可靠性和额度语义

Fallback 默认可由非 2xx 触发，按配置顺序执行，也可限定 `on_status_codes`。Request Timeout 用毫秒表示，超时返回 408，并可配置为 retry/fallback 条件。最重要的流式限制是：只要超时前收到至少一个 chunk，就不再触发该 timeout。这不是整流 deadline，也不是后续 chunk 的空闲超时。[P5][P6]

Circuit Breaker 有错误数、错误比例、最少请求数和等待期；全部目标都 open 时会绕过保护，正文未描述 half-open 探测保证。API key 请求/token 限流和预算页注明 Enterprise 与部分 Pro 客户可用，支持分钟/小时/天窗口；没有足够证据保证并发原子性、重试计数、流式核销或严格不超支。“所有 Portkey plan”这样的说明也不能自动推导为 OSS 内核包含全部平台能力。[P7][P8]

#### 缓存、隐私与开源边界

Simple Cache 和 Semantic Cache 都复用已有响应；自托管语义缓存需要 embedding provider 和 Milvus/Pinecone，页面限定部分 Enterprise 方案。缓存 namespace 不是会话粘性，也不能单独证明租户安全隔离。未取得明确 provider/key session affinity 正文。[P9]

本次仓库导航显示 MIT 标识，但 LICENSE 正文和 raw 均失败；不能据此声称完整商业平台、控制面及企业功能都属于同一开源许可。功能表抓取的空白项也未用于判断权益。[P2][P4]

**取舍：** 可以借鉴简洁的策略/目标配置；若要买完整平台，应接受并核对控制面、日志读取、网络连通和商业条款。个人项目不值得复制这套平台基础设施。

### 3.4 Cloudflare AI Gateway

#### 托管数据路径

接入文档要求 Cloudflare 账户、网关及鉴权，客户端调用 Cloudflare 入口，再转到所选 provider。BYOK 可以将 provider 凭据保存于 Cloudflare，也有请求头携带凭据等路径；统一计费另有支持范围。缓存命中可能不再调用上游。取得的正文没有 Gateway 自托管、离线运行或开源部署方法，不能把开源模型支持误写成 Gateway 开源。[C1]

#### fallback、timeout 和限流

Universal API 可用数组表达有序 fallback，错误或配置的请求超时可触发切换。样例支持 streaming，但没有明确已输出部分响应后的恢复保证。[C2]

Request Handling 页把 timeout 界定为等到 **“the first part of the response comes back”**；收到初始数据后继续等待。文档还说明最后一次 retry 不受该等待期限限制。没有足够正文证明首段等价于有效模型 token、每次尝试如何共享总预算、半流失败如何重试或 provider cooldown 的完整规则。Dynamic Routing 支持条件、概率分流和额度分支，但同样不能替代应用自己的总 deadline。[C3][C6]

网关限流按请求数，支持 fixed/sliding window，超限返回 429；未充分说明跨地域精确一致性或缓存命中是否计数。因此不把它描述为严格的全局 token 预算系统。[C4]

#### 缓存和日志

缓存默认关闭，按完整请求精确匹配；默认键包含 provider、endpoint、model、凭据头和 payload 的哈希。自定义 `cf-aig-cache-key` 会替换默认构造，相同自定义 key 可共享答案，必须自己考虑用户、租户、模型与参数隔离。缓存是易失的，同一时刻的重复请求仍可能都访问上游；这不是 exactly-once 或请求合并保证。[C5]

日志默认启用。`cf-aig-collect-log: false` 可停止整条记录，`cf-aig-collect-log-payload: false` 只去掉正文、保留元数据；这些头不影响 Logpush。正文区分按创建时间适用的新旧日志体系，实际保留期应按所用方案核对，不给出一个普遍有效的保留天数。[C7]

**取舍：** 若愿意让 Cloudflare 处理请求并希望减少运维，它可能比自建等价云设施划算；若目标是个人本地、自控数据路径，则不属于原位替代。

## 4. 三个容易混淆的“缓存”概念

| 概念 | 复用什么 | 是否仍调用模型 | 个人网关应如何处理 |
| --- | --- | --- | --- |
| 上游 prompt caching | 模型服务端对共同输入前缀的计算结果 | 是，通常仍生成新输出 | 保持前缀稳定，正确透传所支持参数，统计 provider 返回的 cache usage；不承诺命中 |
| 网关 response caching | 已生成的完整答案或可回放响应 | 命中时可能不调用 | 仅用于明确可复用场景；按身份、模型、消息、工具和采样参数隔离，敏感正文谨慎落盘 |
| 会话亲和/session affinity | 把会话稳定路由到 provider、key 或后端节点的绑定关系 | 是 | 在健康候选内保持稳定，失败可切换；不等于缓存、会话存储或断流续传 |

以上概念分别由 Bifrost Prompt Caching/Request Options、LiteLLM Cache-Aware Routing、Portkey Cache 和 Cloudflare Caching 正文支持。[B6][B7][L7][P9][C5] 不应以“同一个 session ID”推导服务端一定记住历史、所有 key 共享缓存，或失败后可从上一个 token 继续。

## 5. 当前项目：实施前源码发现与基线

### 5.1 实施前源码发现

以下为实施前当前工作树的源码确认项。确认源码行为不等于已复现线上事故；后续实现及验收结果单独记录。

| 发现 | 为什么需要优先处理 |
| --- | --- |
| SSE 没有有效 data，收到 completed 仍可能记为成功 | 终止标志不等于产生有效响应；会污染成功率、路由健康度和客户端结果 |
| 旧请求的成功可以清除后来建立的新熔断状态 | 并发完成顺序与状态代际不一致，旧结果可能覆盖新判断 |
| 缺少覆盖重试/fallback 的总预算，以及下游写 deadline | 单次上游 timeout 不能防止累计重试过长或慢客户端长期占用资源 |
| probe 可能仅凭 2xx 误通过 | HTTP 成功不保证返回有效模型响应或协议内容 |
| Codex manifest 使用统一图片、reasoning summary和上下文声明 | 没有逐模型能力证据；完整字段形状不等于能力真实。此项延期，不包含首批实现 |
| restore 缺少迁移 checksum 校验 | 迁移历史存在不等于迁移内容完整且匹配 |
| key 格式检查不能证明可解密 | 外形合法不等于当前密钥能恢复有效凭据 |
| 检查结果可能过期 | 历史通过不能替代当前配置、密钥和能力状态的验证 |

这些问题直接影响个人网关的可靠性，优先级高于添加更多路由算法、模型目录、语义缓存或企业管理页面。

### 5.2 已报告的新基线与未覆盖项

实施前实际执行并通过：

- Go 全套测试，使用 `-count=1`；
- `go vet`；
- 前端 typecheck；
- 85 个前端测试；
- diff check；存在 LF/CRLF 提醒，应与测试失败区分。

这些是**实施前新基线**的结果，不是本报告所述后续修复的验证结论。报告编写期间未再次执行任何测试或代码。

尚未运行真实客户端/E2E、browser、Docker 或真实上游验证。已有 4 个 E2E 文件的删除状态保持不变，本报告不恢复或改动它们。单元测试和静态检查通过，也不证明真实流式断连、慢下游、provider 错误格式、凭据失效和预算边界已经验证。

## 6. 已批准方向：借鉴机制，不复制平台

以下描述设计方向，不将具体实现及测试写成既成事实。

### 6.1 可靠性先建立统一契约

1. **明确什么算成功。** 将有效业务输出、协议终止、上游 HTTP 状态和传输结束分别处理；不能只凭 2xx 或 completed 判成功。probe 也应遵循对应的有效响应契约。
2. **一次请求只有一份总时间预算。** 覆盖排队、退避、所有上游尝试和 fallback；另行限制连接、首片段、流空闲和下游写等待。客户端取消应传递至上游，但取消不代表上游没有计费。
3. **提交响应后不做透明切换。** 在未向下游提交响应前可以按策略重试；一旦开始提交，不把另一模型的输出拼成同一次自然续传。否则可能产生重复文本、重复工具调用和不一致状态。
4. **区分故障归属。** 瞬时网络/5xx可以有限重试；上游429可结合 retry hint 冷却目标；凭据错误隔离并提示处理，不永久自动停用配置；调用者本地额度耗尽不能靠换 provider 绕过。
5. **状态更新必须识别代际。** 旧请求成功不能清除新建立的熔断。cooldown、恢复探测和并发请求结果应有明确的更新条件，而不是最后完成者覆盖一切。

### 6.2 保持单节点与可解释状态

SQLite 用于配置、凭据引用、迁移记录、用量及必要审计；单进程内管理实时状态，先明确重启和过期行为。不要因为切换 PostgreSQL 就宣称支持多实例，Bifrost 和 LiteLLM 文档都说明共享存储之外仍需协调机制。[B3][L1][L6]

首批使运行状态、检查结果与连接版本关联，恢复预检核对迁移checksum及外键，启动验证已有渠道密文可读。逐模型能力与Codex manifest需要另行设计共同能力及路由规则，已列为后续范围，不把本次生成探针当作能力认证。

### 6.3 观测默认不留正文

优先记录 request ID、attempt、目标、错误分类、TTFT、总时长、usage、取消和断流原因。正文调试应显式开启、短期保留并独立授权；provider key 不进入日志。每一个外部观测出口都单独审计，不假定一个本地脱敏开关可以覆盖插件、headers、工具参数和对象存储。[L8][B8][B9][C7]

### 6.4 暂不照搬的内容

- 企业 SSO/SCIM、复杂组织层级和多租户商业控制面；
- RAFT、多区域一致性及多套共享存储；
- 语义缓存、向量数据库及自动重写缓存 namespace；
- 尚未有稳定需求的“智能”路由、性能榜和大量插件；
- 任意流式中途 fallback、无证据的自动会话续接；
- 只增加开关、却没有成功语义、deadline、隐私及回归测试契约的功能堆叠。

## 7. 何时改用成熟方案更划算

保留自建不是长期拒绝迁移，而是避免当前阶段过早引入平台成本。以下情况出现时，应重新评估：

- **多协议/provider 兼容成为主要维护负担：** 优先验证 Bifrost 或 LiteLLM。Bifrost 更接近 Go 单节点形态；LiteLLM 的兼容与治理文档更广，但基础设施和配置面也更大。
- **需要团队密钥、精细预算、SSO、审计和专业支持：** 将购买商业能力与自行长期维护的成本比较，逐项确认 enterprise 权益和数据路径，而不是只比较开源仓库。
- **愿意采用外部控制面和运营平台：** Portkey 有明确的混合架构选择，但应先接受配置同步、元数据与日志展示的边界。
- **主要目标变成减少运维：** 接受 Cloudflare 作为第三方处理入口后，托管网关可能比自建同类云设施更合理。
- **真正需要多实例严格预算：** 不继续把单节点实现横向复制；先验证成熟方案在存储故障、网络分区、流式取消和额度核销时的实际行为。

未来替代验证应使用固定版本与合成数据，覆盖首 chunk 前失败、半流断开、慢下游、取消、429/5xx、超时叠加、额度超支和日志脱敏。本文没有执行这些验证，不预判结果。Envoy AI Gateway/Kong 本轮不展开，避免在没有补充正文与明确需求的情况下给出装饰性的功能排名。

## 8. 本地参考项目源码对照

本节来自四个本地快照的只读源码审查。未运行参考仓库脚本、构建或服务，不代表远端最新版或生产效果。源码锚点对应研究时快照。

| 项目 | 本地 commit | 许可证及部署取舍 |
| --- | --- | --- |
| sub2api | `a3298b46ebe239fb0720b068a1eb3383955bdac2` | LGPLv3 正文；Vue＋PostgreSQL/Redis，Account 才是主要上游连接单位 |
| new-api | `e449cc700eb569cdfc2199cb73e97de46944f43c` | AGPLv3＋NOTICE 署名条款；支持 SQLite、React，但带商业治理与多 Key 体系 |
| juhe-ai | `1f2e5beaeb275ea1e345840f1a9e1117d0ecf68a` | Apache-2.0；以实际 Go 执行路径为准，不把旧 Node 注释当当前行为 |
| claude-code-hub | `0c508eaac7e1f3c3717eb6a965b466e719f92b1e` | MIT；Next.js＋PostgreSQL/Redis，与当前单二进制底座不同 |

许可证仅用于界定借鉴边界，不是法律意见；本项目独立实现机制，不复制参考源码或资产。

### 8.1 sub2api：借故障作用域，不搬账号池复杂度

- Account 与 Channel 不是本项目同名概念：Channel 主要关联分组、价格、限制和映射。证据：`reference/sub2api/backend/internal/service/channel.go:49`。
- 调度会先考虑 previous_response 归属、会话绑定，再进入负载选择；评分综合优先级、负载、排队、错误、TTFT、额度和成本。个人版应保留明确优先级和必要亲和，不搬整个评分体系。证据：`reference/sub2api/backend/internal/service/openai_account_scheduler.go:396`、`:992`。
- 故障有 request/provider/account 作用域，模型不可用可以只冷却 account×model，避免误伤其他模型。证据：`reference/sub2api/backend/internal/service/gateway_service.go:673`、`reference/sub2api/backend/internal/service/ratelimit_service.go:2562`。
- API-key 池健康保护有失败窗口和临时摘除，但到期直接重新可调度，不等于单许可半开；明确的 half-open 状态机还出现在计费缓存中，不能误作上游恢复证据。证据：`reference/sub2api/backend/internal/service/openai_apikey_health_breaker.go:70`、`account.go:194`、`billing_cache_service.go:1129`。
- Redis Lua 原子并发槽是真机制，但带租约及Redis成本；单进程内存槽位更适合本项目。工具探针仅见 function_call 即通过，部分401/5xx甚至保守认为支持，不能当工具往返认证。证据：`reference/sub2api/backend/internal/repository/concurrency_cache.go:70`、`reference/sub2api/backend/internal/service/openai_apikey_responses_probe.go:318`。

### 8.2 new-api：借解释链，警惕配置改变策略语义

- 每次尝试可记录 action/reason/source、规则、耗时和健康动作；健康采样能区分 success/failure/ignored，和重试/禁用相互独立。这是最适合移植的运维设计。证据：`reference/new-api/service/request_policy.go:19`、`reference/new-api/pkg/perf_metrics/outcome.go:20`。
- **缓存开关可能改变调度语义**：内存路径按原 weight、全部零时等权；数据库路径用 weight＋10。因此“增加缓存”不是天然无行为变化。证据：`reference/new-api/model/channel_cache.go:188`、`reference/new-api/model/ability.go:153`。
- 多组选择可以重置 retry；外层 RetryTimes 不能直接当整条请求硬调用上限。RELAY_TIMEOUT 是每次 HTTP client 超时，所查路径未证明所有切换共享总deadline。证据：`reference/new-api/controller/relay.go:166`、`reference/new-api/service/channel_select.go:139`、`reference/new-api/service/http_client.go:124`。
- 模型请求限流实际主要按用户/组计数，不等于逐渠道HTTP并发；Redis成功计数先检查后记录，而内存路径有reservation，不能只按功能名称理解保障。证据：`reference/new-api/middleware/model-rate-limit.go:82`、`:181`。
- prompt_cache_key 可用于渠道亲和，但亲和命中不是上游缓存命中。Ability 表是路由关系，不是能力认证。证据：`reference/new-api/setting/operation_setting/channel_affinity_setting.go:117`、`reference/new-api/model/ability.go:18`。

### 8.3 juhe-ai：借配置代次和恢复证据，简化成两级状态

- 当前 Go 路径区分传输提交、语义提交；有8KiB/2秒预提交重放窗口及256KiB缓冲上限，媒体、桥接和副作用场景受限。这不构成已输出内容后的无缝续传，也不保证重试无重复计费。证据：`reference/juhe-ai/backend-go/projects/gateway/internal/gatewayresponse/precommit.go:3`、`stream.go:229`、`retrydecision.go:130`。
- account、key、protocol_model、key_model 四种故障范围，generation/dispatchRevision/lease约束恢复；半开需许可，恢复要覆盖相应范围，2xx之后等待正文结算。对本项目借鉴为渠道及渠道×协议×模型两级、配置版本和单恢复许可即可。证据：`reference/juhe-ai/backend-go/projects/gateway/internal/business/circuit_runtime/contract.go:23`、`runtime.go:588`、`:642`、`reference/juhe-ai/backend-go/projects/gateway/internal/gatewaydispatch/attemptoutcomes.go:98`。
- 忙账户不等于故障账户，排序与原子准入分开；请求有整体墙钟预算及最终响应保留时间。证据：`reference/juhe-ai/backend-go/projects/gateway/internal/gatewaydispatch/capacity.go:62`、`dispatchsingle.go:139`、`reference/juhe-ai/backend-go/projects/gateway/internal/gatewayrouting/routecoordination.go:302`。
- 探针要求协议完成和挑战答案，质量套件另测流式/JSON/工具；多Key任一成功不证明全池有效。被测模型自答自评不等于独立裁判，质量结果还能影响调度，个人版不宜默认引入此处罚。证据：`reference/juhe-ai/backend-go/shared/platform/accounttest/accountprobe/probe.go:711`、`reference/juhe-ai/backend-go/projects/gateway/internal/modelcheckprobe/quiz.go:82`、`reference/juhe-ai/backend-go/projects/gateway/internal/modelcheckowner/business_enforcement.go:107`。

### 8.4 claude-code-hub：借错误归属和决策链，不堆故障层次

- 区分客户端取消、本地DB/内存准入、传输错误、请求错误和供应商错误；实际重试不一定立即换供应商，不能只读枚举注释。流式门控还受enforce/高并发开关约束。证据：`reference/claude-code-hub/src/app/v1/_lib/proxy/errors.ts:993`、`forwarder.ts:2812`、`stream-gate/stream-content-gate.ts:16`。
- Provider/Endpoint/Vendor×Type 多层故障隔离已实现，但half-open准入函数本身未见独占许可；Vendor冷却到期直接closed。端点TCP探活或HTTP<500会成功并reset，不证明鉴权和模型推理正常。证据：`reference/claude-code-hub/src/lib/circuit-breaker.ts:475`、`endpoint-circuit-breaker.ts:308`、`vendor-type-circuit-breaker.ts:116`、`provider-endpoints/probe.ts:108`。
- 前缀亲和有generation/CAS/墓碑，避免迟到写回复活坏绑定；只是调度提示，不是缓存收益证明。个人版应先用显式session ID，确需时再做内容指纹。证据：`reference/claude-code-hub/src/app/v1/_lib/proxy/affinity/fingerprint.ts:5`、`affinity-store.ts:100`。
- 限流中有原子会话登记，但已有会话可继续、Redis故障可fail-open，不能等同硬HTTP并发。固定处理链和有限attempt解释值得借。证据：`reference/claude-code-hub/src/app/v1/_lib/proxy/guard-pipeline.ts:210`、`reference/claude-code-hub/src/lib/rate-limit/service.ts:763`、`:857`。

### 8.5 共同取舍

采用：有限尝试、总预算、可信终态、结构化错误范围、代次约束、受控恢复、明确检查证据及只读运行状态。

延期：状态域亲和、逐模型能力档案、attempt统计与配置历史；这些需要各自的客户端证据和验收，不由一次检查替代。

不照搬：商业计费、多租户、OAuth账号池补丁、Redis分布式租约、多层供应商端点抽象、并行竞速、自动质量评分处罚。复杂度应换来已证明的收益，而非仅增加开关。

## Sources：成功取得正文的官方资料

### LiteLLM

- **[L1] Deployment** — 章节：Architecture、Core configuration、Terraform 部署；PostgreSQL/Redis 与单实例边界。https://docs.litellm.ai/docs/proxy/deploy
- **[L2] Outbound Requests and Offline Mode** — 章节：Turn on offline mode、Requests offline mode stops、Requests offline mode doesn't stop。https://docs.litellm.ai/docs/proxy/outbound_requests.md
- **[L3] Usage Telemetry** — 遥测启用、目的地、字段及禁用控制。https://docs.litellm.ai/docs/proxy/telemetry.md
- **[L4] Reliability** — 章节：Fallbacks + Retries + Timeouts + Cooldowns、Disable Fallbacks (Per Request/Key)。https://docs.litellm.ai/docs/proxy/reliability
- **[L5] Timeouts** — 章节：Global Timeouts、Per attempt、streaming 相关说明。https://docs.litellm.ai/docs/proxy/timeout.md
- **[L6] Budgets, Rate Limits** — 章节：Set Rate Limits、Budget reservation、Hard rate limit enforcement (fail closed)。https://docs.litellm.ai/docs/proxy/users.md
- **[L7] Cache-Aware Routing** — 章节：Session affinity and retries；deployment 内后端选择与共享 Redis。https://docs.litellm.ai/docs/proxy/cache_aware_routing.md
- **[L8] Logging** — 章节：Redact Messages, Response Content、Turn off all tracking/logging、Redacting UserAPIKeyInfo。https://docs.litellm.ai/docs/proxy/logging.md
- **[L9] Enterprise** — 章节：What you get on top of open source、Full feature list。https://docs.litellm.ai/docs/enterprise.md
- **[L10] LICENSE** — 已取得许可正文，MIT 与 enterprise/ carve-out。https://github.com/BerriAI/litellm/blob/main/LICENSE

### Bifrost

- **[B1] Setting Up** — 本地启动、存储选择、鉴权与生产配置。https://docs.getbifrost.ai/quickstart/gateway/setting-up
- **[B2] Moving from OSS: Overview** — 章节：What carries over、What does not carry over: SQLite。https://docs.getbifrost.ai/enterprise/moving-from-oss/overview.md
- **[B3] Budget and Limits** — 额度检查、reset、运行状态与 OSS 多节点限制。https://docs.getbifrost.ai/features/governance/budget-and-limits.md
- **[B4] Retries and Fallbacks** — 章节：Fallbacks、Retries、Provider retry hints、Azure streaming errors before output。https://docs.getbifrost.ai/features/retries-and-fallbacks.md
- **[B5] Governance Configuration** — 章节：Rate Limits、Budgets、Model Limits、Routing Rules。https://docs.getbifrost.ai/deployment-guides/config-json/governance.md
- **[B6] Request Options** — session headers、provider/key 亲和、per-request prompt-cache 选项。https://docs.getbifrost.ai/providers/request-options.md
- **[B7] Prompt Caching** — 上游前缀复用与响应缓存区别；章节：Verifying that caching works。https://docs.getbifrost.ai/features/prompt-caching.md
- **[B8] Built-in Observability** — 章节：Configuration、Log Store Options、Logging Headers、Retry & Key Selection。https://docs.getbifrost.ai/features/observability/default.md
- **[B9] Client Configuration** — 章节：Request & Response Logging；本地正文设置与连接器分离。https://docs.getbifrost.ai/deployment-guides/config-json/client.md
- **[B10] Enterprise Overview** — Adaptive Routing、Clustering、OIDC、RBAC、Audit Logs 与部署选项。https://docs.getbifrost.ai/enterprise/overview.md
- **[B11] Official README** — 本地启动、Go SDK、Apache 2.0 声明与 enterprise 概览；不替代 LICENSE 全文。https://github.com/maximhq/bifrost
- **[B12] Provider Configuration** — Weighted Load Balancing、Managing Retries；默认退避数字未作为保证采用。https://docs.getbifrost.ai/quickstart/gateway/provider-configuration.md
- **[B13] Security at Bifrost** — 已取得正文，但不足以证明自托管零外联或遥测默认行为。https://docs.getbifrost.ai/security.md

### Portkey

- **[P1] Installation and Deployments** — 章节：Local Deployment、Managed Deployment、Enterprise Deployment。https://github.com/Portkey-AI/gateway/blob/main/docs/installation-deployments.md
- **[P2] Official Repository / README** — 本地启动、日志与 hosted/enterprise 标注；许可导航不替代 LICENSE 全文。https://github.com/Portkey-AI/gateway
- **[P3] Hybrid Enterprise Architecture** — 章节：Core Architecture Components、Data Flow Between Planes、Infrastructure Requirements、Log Management Options。https://portkey.ai/docs/self-hosting/hybrid-deployments/architecture.md
- **[P4] Feature Comparison** — Enterprise Deployment Options 等文字说明；未据空白表格单元推断权益。https://portkey.ai/docs/product/product-feature-comparison.md
- **[P5] Fallbacks** — 章节：Fallbacks、Trigger on Specific Status Codes。https://portkey.ai/docs/product/ai-gateway/fallbacks.md
- **[P6] Request Timeouts** — 章节：How timeouts work in nested Configs、Handling Request Timeouts、Caveats and Considerations。https://portkey.ai/docs/product/ai-gateway/request-timeouts.md
- **[P7] Circuit Breaker** — 章节：Config Schema、How It Works、Runtime Behavior。https://portkey.ai/docs/product/ai-gateway/circuit-breaker.md
- **[P8] Enforce Budget and Rate Limits** — 章节：Setting Up Rate Limits、Time Intervals、Budget Limits，以及方案限制说明。https://portkey.ai/docs/product/administration/enforce-budget-and-rate-limit.md
- **[P9] Simple and Semantic Cache** — 章节：Simple Cache、Semantic Cache、Cache Namespace、Set up semantic caching (self-hosted)。https://portkey.ai/docs/product/ai-gateway/cache-simple-and-semantic.md

### Cloudflare

- **[C1] Get Started** — 章节：Get your account ID and authentication token、REST API、Provider authentication。https://developers.cloudflare.com/ai-gateway/get-started/
- **[C2] Fallbacks** — 失败切换、数组顺序和 cf-aig-step。https://developers.cloudflare.com/ai-gateway/configuration/fallbacks/
- **[C3] Request Handling** — 章节：Request timeouts、Request retries。https://developers.cloudflare.com/ai-gateway/configuration/request-handling/
- **[C4] Rate Limiting** — 请求计数、fixed/sliding window、429。https://developers.cloudflare.com/ai-gateway/features/rate-limiting/
- **[C5] Caching** — 章节：Caching、Per-request caching、How the cache key works。https://developers.cloudflare.com/ai-gateway/features/caching/
- **[C6] Dynamic Routing** — 章节：Introduction、Core Concepts、Getting Started。https://developers.cloudflare.com/ai-gateway/features/dynamic-routing/
- **[C7] Logging** — 默认启用、请求级 payload 控制、Logpush 例外及日志体系适用说明。https://developers.cloudflare.com/ai-gateway/observability/logging/

### 未取得正文或仅部分核实的许可资料

以下请求失败，不作为许可正文依据；记录 URL 便于后续固定版本后复核。

- Bifrost LICENSE：https://github.com/maximhq/bifrost/blob/main/LICENSE
- Bifrost LICENSE raw：https://raw.githubusercontent.com/maximhq/bifrost/main/LICENSE
- Portkey LICENSE：https://github.com/Portkey-AI/gateway/blob/main/LICENSE
- Portkey LICENSE raw：https://raw.githubusercontent.com/Portkey-AI/gateway/main/LICENSE

未取得正文的协议补充资料：
- https://platform.openai.com/docs/guides/conversation-state （403）
- https://platform.openai.com/docs/guides/prompt-caching （403）
- https://platform.openai.com/docs/guides/streaming-responses （403）
- https://raw.githubusercontent.com/openai/openai-python/main/src/openai/types/responses/response_create_params.py （ECONNRESET）
- https://raw.githubusercontent.com/openai/openai-python/main/src/openai/types/responses/response_stream_event.py （ECONNRESET）

以上未获取内容不作为本报告结论依据。
