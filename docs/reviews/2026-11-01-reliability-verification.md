# AI Router 可靠性加固验收记录

日期：2026-11-01。范围：当前工作树第一批可靠性与诊断改造。方案比较见 [网关对比与取舍](2026-11-01-gateway-comparison.md)，配置和升级说明见 [部署文档](../deploy.md)。

## 实现范围

- Responses SSE 终态校验及 EOF 残帧拒绝；提交前可有限切换，提交后不拼接另一个上游。合法空完成与 incomplete 分开处理。
- 非流式/流式总预算覆盖候选等待与重试；独立首语义预算、可续期下游写截止、Flush错误传播。总预算超时和写超时不误记客户端取消。
- 渠道与渠道×协议×模型两级冷却；派发票据、配置版本、单恢复许可，阻止旧成功清新故障及短冷却覆盖长窗口。
- 生成探针校验限长JSON、错误、完成状态与有效输出；输出预算截断显示不确定，保留16输出token、单次请求。
- 管理端共享配置后的上游客户端及独立检查预算；分别展示启用、运行状态、最近检查、协议/模型/版本、响应头及总耗时。
- schema v2 连接版本及检查上下文；更新URL/协议/Key原子失效旧检查，迟到旧版本不能覆盖。
- 恢复前校验迁移名称/checksum/历史缺口及外键；启动前只读验证现有渠道密文可读。

## 自动化结果

| 检查 | 结果 |
| --- | --- |
| `go test -count=1 -timeout=180s ./...` | 通过 |
| `go vet ./...` | 通过 |
| `npm --prefix web run typecheck` | 通过 |
| `npm --prefix web run test` | 单独运行通过：9文件、89项 |
| `npm --prefix web run build` | 通过，生成 `index-T9G7BxmA.js`、`index-QuhxMAgG.css` 及主题初始化资源 |
| 构建后 `go test -count=1 ./internal/server ./internal/webassets` | 通过；webassets包无独立测试，嵌入服务行为由server测试覆盖 |
| `git diff --check` | 无空白错误；存在LF/CRLF转换提醒 |

前端与Go全套同时运行的一轮出现3项失败：两项超过5000ms，随后一项密码输入只得到首字母。停止并行测试负载后，以相同命令、相同断言及相同时限重跑前端全套，89项通过。未修改设置页逻辑、测试断言或全局超时；该次并行失败不计为通过，保留资源竞争/用例时序敏感性提示。

## 关键离线行为证据

- `internal/gateway/reliability_test.go`：独立严格SSE消费者、无data/畸形终态、合法空完成、incomplete、心跳首内容超时、提交后总预算、请求/模型故障范围、Retry-After及writer错误传播。
- `internal/gateway/failure_write_test.go`：最终错误响应最多1秒且不超过写预算；客户端取消不写响应，无法设置deadline时不继续无界写。
- `internal/gateway/slow_writer_test.go`：受控阻塞writer在写截止退出，取消假上游、归还槽位、不重试且不处罚渠道；与错误交付用例重复3次通过。补充后再次运行cmd/gateway、gateway、upstream、adminapi核心回归全部通过。
- `internal/routing/scheduler_test.go`：代次交错、单恢复许可、取消归还、模型隔离、配置复查及Retry-After；关键用例重复20次通过。
- `internal/upstream/probe_test.go`：HTML200、泛JSON、200error、空输出、无终态、超限、token截断及完整耗时。
- `internal/upstream/probe_record_test.go`、`internal/adminapi/debug_test.go`：检查超时仍能返回结构化结果，健康落库使用独立最多1秒预算；旧版本写回受CAS限制。
- `internal/store/channels_test.go`、`migrate_test.go`：原子版本更新、检查失效、坏checksum/名称/历史缺口/外键备份提前拒绝、合法/错误密钥及空库。
- `cmd/gateway/startup_test.go`：错误但格式合法的主密钥在HTTP启动前被拒绝，正确主密钥可ready。
- `cmd/gateway/http_contract_test.go`：使用生产 `buildServer` / `dataplane.Build`、真实随机本机HTTP连接、临时数据库和假上游，验证畸形SSE切换、实际冷却状态经管理API可见、统一请求ID日志关联、总预算504交付、上游取消和槽位释放。

HTTP契约测试首次运行中，测试登录缺Origin/使用错误路径被生产鉴权和路由拒绝，假上游未消费请求体导致取消等待不结束；修正测试请求与fixture后两项通过，未放宽生产安全检查。

## 明确边界

- 未重启8095/8096/8097预览服务，未替换或重启8080网关。临时httptest服务随测试结束关闭。
- 未读取实际部署密钥、未调用真实付费上游、未在实际数据库执行迁移或恢复。已有未提交改动与四个E2E删除状态保留，未提交或推送Git。
- 未运行真实Codex/ZCode E2E、浏览器视觉/点击验收、Docker镜像运行和真实部署恢复演练。独立组件测试与本地HTTP契约不替代这些验收。
- `go test -race` 因当前CGO关闭不可执行；未更改环境或声称race通过。
- 总转发预算不含入站请求体上传；最终固定错误JSON可额外使用最多1秒写窗口，日志/健康持久化有独立有限收尾预算。已提交流在总预算耗尽后不保证还能送出终结错误帧，客户端必须处理断流。
- 恢复仍要求停服；WAL checkpoint不是跨进程独占锁。启动解密检查限现有渠道，不等于捕获和历史备份全库审计。
- 逐模型能力manifest、有状态Responses生命周期、亲和、usage覆盖率、配置变更历史等不属于本批实现。
